// Package transferhistory reads the transfers into and out of a Safe from the
// Safe Transaction Service, and checks each one against the Safe's chain.
//
// The service indexes a Safe's whole history, which a scan of the chain's logs
// can't reach on a public node in reasonable time. It is trusted like an RPC
// node to list the transfers, so a transfer that it leaves out isn't listed,
// but every transfer that it lists is checked against the receipt of its
// transaction, so it can't report one that didn't happen.
package transferhistory

import (
	"cmp"
	"context"
	"fmt"
	"math/big"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

var (
	// transferEvent is the Transfer event of ERC-20 and ERC-721 tokens, which index
	// the token ID of an ERC-721 transfer too.
	transferEvent = solabi.Event("Transfer(address,address,uint256)")
	// safeReceivedEvent is the event that the receive function of the Safe 1.3.0,
	// 1.4.1, and 1.5.0 singletons logs, with the caller as the sender.
	safeReceivedEvent = solabi.Event("SafeReceived(address,uint256)")
)

// Types of transfers.
const (
	ERC20  = "erc20"
	ERC721 = "erc721"
	Native = "native"
)

// Checks of a transfer against the chain.
const (
	// CheckLog is the check of a transfer that a log of its transaction's receipt
	// shows: a Transfer event of its token, or a SafeReceived event of the Safe for
	// the native currency, with its sender, recipient, and value.
	CheckLog = "log"
	// CheckTransaction is the check of a transfer of the native currency that no
	// log shows, such as the value of a call that the Safe makes: only that its
	// transaction succeeded in its block. The amount and the accounts are as the
	// service reports them, since they are in the transaction's trace, which public
	// nodes rarely serve.
	CheckTransaction = "transaction"
)

// Transfer is a transfer into or out of a Safe, checked against the Safe's
// chain.
type Transfer struct {
	// Type is ERC20, ERC721, or Native.
	Type   string
	Block  uint64
	TxHash ethrpc.Hash
	// LogIndex is the index of the log that shows the transfer, and nil if no log
	// does.
	LogIndex *uint64
	// Token is the contract of the token, and nil for the native currency.
	Token *ethrpc.Address
	From  ethrpc.Address
	To    ethrpc.Address
	// Value is the amount in the smallest unit of the token or the currency, and
	// nil for an ERC-721 transfer, which has a TokenID instead.
	Value   *big.Int
	TokenID *big.Int
	// Check is CheckLog or CheckTransaction.
	Check string
}

// FromService returns the transfers into and out of safe that service lists, in
// the blocks up to toBlock, each checked against the receipt of its transaction
// that eth returns, in the order of their blocks and logs. It leaves out the
// transfers of later blocks, and fails if the chain doesn't confirm a transfer,
// since the service would then misreport the Safe's history.
func FromService(ctx context.Context, service *txservice.Client, eth *ethrpc.Client, safe ethrpc.Address, toBlock uint64) ([]Transfer, error) {
	listed, err := service.Transfers(ctx, safe)
	if err != nil {
		return nil, err
	}
	receipts := map[ethrpc.Hash]ethrpc.Receipt{}
	transfers := []Transfer{}
	for _, t := range listed {
		if t.Block > toBlock {
			continue
		}
		receipt, ok := receipts[t.TxHash]
		if !ok {
			if receipt, err = eth.GetTransactionReceipt(ctx, t.TxHash); err != nil {
				return nil, fmt.Errorf("checking transfer in %s: %w", t.TxHash, err)
			}
			receipts[t.TxHash] = receipt
		}
		checked, err := check(t, safe, receipt)
		if err != nil {
			return nil, fmt.Errorf("the service lists a transfer in %s that the chain doesn't confirm: %w", t.TxHash, err)
		}
		transfers = append(transfers, checked)
	}
	slices.SortStableFunc(transfers, func(a, b Transfer) int {
		return cmp.Or(cmp.Compare(a.Block, b.Block), compareLogIndex(a.LogIndex, b.LogIndex))
	})
	return transfers, nil
}

// compareLogIndex orders transfers without a log after those with one.
func compareLogIndex(a, b *uint64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return cmp.Compare(*a, *b)
}

// check checks the transfer t of safe against the receipt of its transaction.
func check(t txservice.Transfer, safe ethrpc.Address, receipt ethrpc.Receipt) (Transfer, error) {
	if t.From != safe && t.To != safe {
		return Transfer{}, fmt.Errorf("transfer from %s to %s is neither from nor to the Safe", t.From, t.To)
	}
	if uint64(receipt.BlockNumber) != t.Block {
		return Transfer{}, fmt.Errorf("transaction is in block %d, not %d", receipt.BlockNumber, t.Block)
	}
	if receipt.Status != 1 {
		return Transfer{}, fmt.Errorf("transaction reverted")
	}
	for _, x := range []*big.Int{t.Value, t.TokenID} {
		// The service decodes amounts as non-negative integers of any size.
		if x != nil && x.BitLen() > 256 {
			return Transfer{}, fmt.Errorf("amount %d is wider than 256 bits", x)
		}
	}
	transfer := Transfer{Block: t.Block, TxHash: t.TxHash, Token: t.Token, From: t.From, To: t.To, Value: t.Value, TokenID: t.TokenID, Check: CheckLog}

	switch t.Type {
	case txservice.ERC20Transfer, txservice.ERC721Transfer:
		transfer.Type = ERC20
		want := []ethrpc.Hash{transferEvent, ethrpc.Hash(solabi.Address(t.From)), ethrpc.Hash(solabi.Address(t.To))}
		var data []byte
		if t.Type == txservice.ERC721Transfer {
			transfer.Type = ERC721
			want = append(want, ethrpc.Hash(solabi.Uint(t.TokenID)))
		} else {
			value := solabi.Uint(t.Value)
			data = value[:]
		}
		log := logAt(receipt, *t.LogIndex)
		if log == nil || log.Address != *t.Token || !slices.Equal(log.Topics, want) || string(log.Data) != string(data) {
			return Transfer{}, fmt.Errorf("log %d isn't the Transfer event of %s that the service reports", *t.LogIndex, t.Token)
		}
		transfer.LogIndex = t.LogIndex

	case txservice.EtherTransfer:
		transfer.Type = Native
		transfer.Check = CheckTransaction
		if t.To == safe {
			// A call with no data that sends the Safe the native currency logs
			// SafeReceived, which confirms the transfer.
			want := []ethrpc.Hash{safeReceivedEvent, ethrpc.Hash(solabi.Address(t.From))}
			value := solabi.Uint(t.Value)
			for _, log := range receipt.Logs {
				if !log.Removed && log.Address == safe && slices.Equal(log.Topics, want) && string(log.Data) == string(value[:]) {
					index := uint64(log.LogIndex)
					transfer.LogIndex, transfer.Check = &index, CheckLog
					break
				}
			}
		}

	default:
		return Transfer{}, fmt.Errorf("unknown transfer type %q", t.Type)
	}
	return transfer, nil
}

// logAt returns the log of receipt with the index in its block, or nil.
func logAt(receipt ethrpc.Receipt, index uint64) *ethrpc.Log {
	for i, log := range receipt.Logs {
		if uint64(log.LogIndex) == index && !log.Removed {
			return &receipt.Logs[i]
		}
	}
	return nil
}
