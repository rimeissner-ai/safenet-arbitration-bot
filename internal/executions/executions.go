// Package executions finds the Safe transactions that a Safe executed onchain,
// and decodes them.
//
// A Safe logs an ExecutionSuccess or ExecutionFailure event with the hash of
// each Safe transaction it executes, but the event doesn't say what the
// transaction was. The package decodes it from the first of these sources that
// yields a transaction with that hash:
//
//  1. The SafeMultiSigTransaction event, which SafeL2 logs before executing.
//  2. The calldata of the onchain transaction, if it calls the Safe's
//     execTransaction directly.
//  3. A trace of the onchain transaction, for the calls to execTransaction that
//     it makes, such as by a relayer or a batch.
//  4. The Safe Transaction Service.
//
// The calldata and traces don't have the Safe transaction's nonce, so it is the
// one that the Safe Transaction Service has for the transaction, or else worked
// out from the Safe's nonce, since every execution increases it by one.
//
// The executions are found without scanning logs if possible: the Safe
// Transaction Service lists the transactions that the Safe executed with the
// onchain transactions that executed them, and the receipt of each onchain
// transaction has the Safe's logs. Only what the receipts show is reported, so
// the service can leave executions out, but not make them up. If the executions
// in the receipts have every nonce below the Safe's nonce, they are all of
// them, and Find doesn't scan logs. A decoded transaction is only ever reported
// if its EIP-712 hash is the one that the Safe logged, so a source can't
// misreport what was executed, however much it is trusted.
package executions

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// The events and function of Safe 1.3.0, 1.4.1, and 1.5.0 that the package
// reads. The signatures of ExecutionSuccess and ExecutionFailure are the same
// in all of them, but 1.3.0 doesn't index their transaction hash.
var (
	executionSuccessEvent = solabi.Event("ExecutionSuccess(bytes32,uint256)")
	executionFailureEvent = solabi.Event("ExecutionFailure(bytes32,uint256)")
	multiSigEvent         = solabi.Event("SafeMultiSigTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes,bytes)")
	execTransaction       = solabi.Selector("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)")
	nonceSelector         = solabi.Selector("nonce()")
)

// Source is where a transaction was decoded from.
type Source string

const (
	// SourceEvent is the SafeMultiSigTransaction event of SafeL2.
	SourceEvent Source = "event"
	// SourceCalldata is the calldata of the onchain transaction.
	SourceCalldata Source = "calldata"
	// SourceTrace is a trace of the onchain transaction.
	SourceTrace Source = "trace"
	// SourceService is the Safe Transaction Service.
	SourceService Source = "service"
)

// Execution is a Safe transaction that a Safe executed.
type Execution struct {
	Block uint64 `json:"block"`
	// TxHash is the hash of the onchain transaction that executed it, and LogIndex
	// the index in its block of the log that says so.
	TxHash   ethrpc.Hash `json:"txHash"`
	LogIndex uint64      `json:"logIndex"`
	// SafeTxHash is the hash of the Safe transaction, from the Safe's log.
	SafeTxHash ethrpc.Hash `json:"safeTxHash"`
	// Success is whether the Safe transaction's call succeeded. A Safe transaction
	// whose call fails is executed all the same, and uses its nonce.
	Success bool `json:"success"`
	// Payment is the refund that the Safe paid, in the gas token.
	Payment *big.Int `json:"payment"`
	// Source is where Transaction was decoded from. It is empty if the transaction
	// is unknown.
	Source Source `json:"source,omitempty"`
	// Transaction is nil if no source has a transaction with the hash SafeTxHash.
	Transaction *safenet.SafeTransaction `json:"transaction"`
}

// Finder finds the Safe transactions that a Safe executed on a chain.
type Finder struct {
	// Eth is a client for the chain. The node must serve eth_call at the last block
	// that Find searches for the calldata and trace sources, which are otherwise
	// skipped, and debug_traceTransaction for the trace source.
	Eth *ethrpc.Client
	// Service is a client for the chain's Safe Transaction Service, or nil to leave
	// it out.
	Service *txservice.Client
	// Warn is called with the errors of sources that failed for a reason other than
	// not having the transaction, such as an unreachable service. It may be nil.
	Warn func(error)
}

// Found is what Find found.
type Found struct {
	// Executions are the executions, in the order of their logs.
	Executions []Execution
	// Complete is whether Executions has every execution of the Safe up to the last
	// block, and not only those in the range of blocks that Find searched.
	Complete bool
}

// Find returns the Safe transactions that the Safe executed up to the block to,
// including it, in order.
//
// It first looks up the executions that the Safe Transaction Service reports in
// the receipts of their onchain transactions. If those have every nonce below
// the Safe's nonce at to, and none of them is a delegate call, they are all of
// the Safe's executions, since each one increases its nonce by one, and only a
// delegate call can change it otherwise. Find then returns them as complete.
// Otherwise it also scans the Safe's logs in the blocks from from to to, and
// returns the executions of both.
func (f *Finder) Find(ctx context.Context, safe ethrpc.Address, from, to ethrpc.BlockNumber) (Found, error) {
	d := &decoder{
		Finder: f, safe: safe, to: to,
		events:  make(map[ethrpc.Hash][]*safenet.SafeTransaction),
		service: make(map[ethrpc.Hash]*safenet.SafeTransaction),
	}
	seen := make(map[logID]bool)
	executions := []Execution{}
	add := func(log ethrpc.Log) {
		if log.Removed || log.Address != safe || len(log.Topics) == 0 || uint64(log.BlockNumber) > uint64(to) {
			return
		}
		id := logID{log.TransactionHash, uint64(log.LogIndex)}
		if seen[id] {
			return
		}
		seen[id] = true
		switch log.Topics[0] {
		case multiSigEvent:
			// The SafeMultiSigTransaction logs by onchain transaction, whose hash matches
			// the transaction of the execution that follows.
			if tx := f.decodeEvent(safe, log.Data); tx != nil {
				d.events[log.TransactionHash] = append(d.events[log.TransactionHash], tx)
			}
		case executionSuccessEvent, executionFailureEvent:
			if e, ok := decodeExecution(log); ok {
				executions = append(executions, e)
			}
		}
	}

	if f.Service != nil {
		if next := d.nextNonce(ctx); next != nil {
			for _, log := range f.serviceLogs(ctx, d, safe, next) {
				add(log)
			}
			d.setExecutions(executions)
			if found, ok := d.complete(ctx, next); ok {
				return found, nil
			}
		}
	}

	logs := f.Eth.ScanLogs(ctx, ethrpc.LogFilter{
		FromBlock: from,
		ToBlock:   to,
		Addresses: []ethrpc.Address{safe},
		Topics:    [][]ethrpc.Hash{{executionSuccessEvent, executionFailureEvent, multiSigEvent}},
	})
	for log, err := range logs {
		if err != nil {
			return Found{}, fmt.Errorf("listing Safe transactions of %s: %w", safe, err)
		}
		add(log)
	}
	d.setExecutions(executions)
	for i := range d.executions {
		e := &d.executions[i]
		e.Source, e.Transaction = d.decode(ctx, i)
	}
	return Found{Executions: d.executions}, nil
}

// logID identifies a log.
type logID struct {
	tx    ethrpc.Hash
	index uint64
}

// serviceLogs returns the logs of the onchain transactions that the Safe
// Transaction Service says executed the Safe's transactions with a nonce below
// next. It also gives d the service's transactions, to decode the executions.
func (f *Finder) serviceLogs(ctx context.Context, d *decoder, safe ethrpc.Address, next *big.Int) []ethrpc.Log {
	listed, err := f.Service.Executed(ctx, safe, next)
	if errors.Is(err, txservice.ErrNotFound) {
		return nil
	}
	if err != nil {
		d.warn(fmt.Errorf("listing the executed transactions of %s with the Safe Transaction Service: %w", safe, err))
		return nil
	}
	var logs []ethrpc.Log
	receipts := make(map[ethrpc.Hash]bool)
	for _, x := range listed {
		d.service[x.SafeTxHash] = x.Transaction
		if x.TxHash == nil || receipts[*x.TxHash] {
			continue
		}
		receipts[*x.TxHash] = true
		receipt, err := f.Eth.GetTransactionReceipt(ctx, *x.TxHash)
		if err != nil {
			d.warn(fmt.Errorf("getting the receipt of transaction %s: %w", *x.TxHash, err))
			continue
		}
		logs = append(logs, receipt.Logs...)
	}
	return logs
}

// decodeExecution decodes an ExecutionSuccess or ExecutionFailure log. Safe
// 1.3.0 has its transaction hash and payment in the data, and later versions
// have the transaction hash as a topic, and the payment in the data.
func decodeExecution(log ethrpc.Log) (Execution, bool) {
	e := Execution{
		Block:    uint64(log.BlockNumber),
		TxHash:   log.TransactionHash,
		LogIndex: uint64(log.LogIndex),
		Success:  log.Topics[0] == executionSuccessEvent,
	}
	switch {
	case len(log.Topics) == 1 && len(log.Data) == 64:
		e.SafeTxHash = ethrpc.Hash(log.Data[:32])
		e.Payment = new(big.Int).SetBytes(log.Data[32:])
	case len(log.Topics) == 2 && len(log.Data) == 32:
		e.SafeTxHash = log.Topics[1]
		e.Payment = new(big.Int).SetBytes(log.Data)
	default:
		return Execution{}, false
	}
	return e, true
}

// decodeEvent decodes the data of a SafeMultiSigTransaction log of the Safe,
// which has the Safe transaction's fields, its signatures, and the nonce, the
// sender, and the threshold as additional information. It returns nil if the
// data is malformed.
func (f *Finder) decodeEvent(safe ethrpc.Address, data []byte) *safenet.SafeTransaction {
	d := solabi.NewDecoder(data)
	tx := decodeFields(d, safe, f.Eth.ChainID())
	info := d.Bytes(10)
	if d.Err() != nil || len(info) < 32 {
		return nil
	}
	tx.Nonce = new(big.Int).SetBytes(info[:32])
	return tx
}

// decodeFields decodes the first nine values of a tuple of the Safe
// transaction's fields, as they are in the arguments of execTransaction and in
// the data of SafeMultiSigTransaction: the transaction's target, value, data,
// and operation, and its gas and refund parameters. It leaves the nonce unset.
func decodeFields(d *solabi.Decoder, safe ethrpc.Address, chainID uint64) *safenet.SafeTransaction {
	tx := &safenet.SafeTransaction{ChainID: new(big.Int).SetUint64(chainID), Safe: safe}
	tx.To = d.Address(0)
	tx.Value = d.Uint(1)
	tx.Data = d.Bytes(2)
	operation := d.Uint64(3)
	tx.SafeTxGas = d.Uint(4)
	tx.BaseGas = d.Uint(5)
	tx.GasPrice = d.Uint(6)
	tx.GasToken = d.Address(7)
	tx.RefundReceiver = d.Address(8)
	if operation > uint64(safenet.OperationDelegateCall) && d.Err() == nil {
		// The Safe rejects the operation, which is an enum.
		tx.Operation = safenet.Operation(255)
	} else {
		tx.Operation = safenet.Operation(operation)
	}
	return tx
}

// decodeCall decodes the calldata of a call to execTransaction on the Safe. It
// returns nil if the calldata is for another function or is malformed. The
// nonce is unset.
func decodeCall(safe ethrpc.Address, chainID uint64, input []byte) *safenet.SafeTransaction {
	if len(input) < 4 || [4]byte(input[:4]) != execTransaction {
		return nil
	}
	d := solabi.NewDecoder(input[4:])
	tx := decodeFields(d, safe, chainID)
	d.Bytes(9)
	if d.Err() != nil {
		return nil
	}
	return tx
}

// matches reports whether tx is a Safe transaction with the hash want. It
// doesn't hash a transaction with a missing or oversized field, which the ABI
// encoding of the hash doesn't allow.
func matches(tx *safenet.SafeTransaction, want ethrpc.Hash) bool {
	if tx == nil || tx.Operation > safenet.OperationDelegateCall {
		return false
	}
	for _, n := range []*big.Int{tx.ChainID, tx.Value, tx.SafeTxGas, tx.BaseGas, tx.GasPrice, tx.Nonce} {
		if n == nil || n.Sign() < 0 || n.BitLen() > 256 {
			return false
		}
	}
	return tx.Hash() == want
}
