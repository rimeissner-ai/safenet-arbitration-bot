// Package native reads the native currency that a Safe received, such as ether
// on Ethereum Mainnet, from the Safe's logs.
package native

import (
	"context"
	"fmt"
	"math/big"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// safeReceivedEvent is the event that the receive function of the Safe 1.3.0,
// 1.4.1, and 1.5.0 singletons logs, with the caller as the sender.
var safeReceivedEvent = solabi.Event("SafeReceived(address,uint256)")

// Transfer is a transfer of the native currency to a Safe, as a SafeReceived
// event of the Safe shows it.
type Transfer struct {
	Block    uint64
	TxHash   ethrpc.Hash
	LogIndex uint64
	From     ethrpc.Address
	To       ethrpc.Address
	// Value is the amount in the native currency's smallest unit, such as wei.
	Value *big.Int
}

// ReceivedBy returns the transfers of the native currency to safe that its
// SafeReceived events show, in the blocks fromBlock to toBlock, in order. It
// only includes those from the address from, unless from is nil.
//
// The Safe logs the event when a call with no data sends it the native
// currency, with the caller as the sender. Native currency that reaches the
// Safe otherwise, such as the value of an execTransaction call, a block reward,
// a withdrawal, a self-destruct, or a bridge that mints it, has no event, so it
// isn't listed. A delegate call that the Safe makes can log an event with the
// same signature, and logs that don't have the event's shape are left out.
func ReceivedBy(ctx context.Context, eth *ethrpc.Client, safe ethrpc.Address, from *ethrpc.Address, fromBlock, toBlock ethrpc.BlockNumber) ([]Transfer, error) {
	topics := [][]ethrpc.Hash{{safeReceivedEvent}}
	if from != nil {
		topics = append(topics, []ethrpc.Hash{solabi.Address(*from)})
	}
	filter := ethrpc.LogFilter{FromBlock: fromBlock, ToBlock: toBlock, Addresses: []ethrpc.Address{safe}, Topics: topics}

	transfers := []Transfer{}
	for log, err := range eth.ScanLogs(ctx, filter) {
		if err != nil {
			return nil, fmt.Errorf("listing native transfers to %s: %w", safe, err)
		}
		if log.Removed || len(log.Topics) != 2 || len(log.Data) != 32 {
			continue
		}
		transfers = append(transfers, Transfer{
			Block:    uint64(log.BlockNumber),
			TxHash:   log.TransactionHash,
			LogIndex: uint64(log.LogIndex),
			From:     ethrpc.Address(log.Topics[1][12:]),
			To:       safe,
			Value:    new(big.Int).SetBytes(log.Data),
		})
	}
	return transfers, nil
}
