package events

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	alice = ethrpc.Address{0: 0xa1, 19: 1}
	bob   = ethrpc.Address{0: 0xb0, 19: 2}
	txID  = ethrpc.Hash{0: 0xaa, 31: 9}
)

// log returns a log with the topic of the event signature, followed by topics,
// and data.
func log(signature string, topics []ethrpc.Hash, data []byte) ethrpc.Log {
	return ethrpc.Log{Topics: append([]ethrpc.Hash{solabi.Event(signature)}, topics...), Data: data}
}

func word(x any) ethrpc.Hash {
	switch x := x.(type) {
	case ethrpc.Address:
		return solabi.Address(x)
	case uint64:
		return solabi.Uint64(x)
	}
	panic("unsupported")
}

func args(a ...Arg) []Arg { return a }

func TestDecode(t *testing.T) {
	const transfer = "Transfer(address,address,uint256)"
	tests := []struct {
		name string
		log  ethrpc.Log
		want *Event
	}{
		{
			"ERC-20 Transfer",
			log(transfer, []ethrpc.Hash{word(alice), word(bob)}, solabi.Encode(uint64(7))),
			&Event{"Transfer", transfer, args(Arg{"from", "address", alice}, Arg{"to", "address", bob}, Arg{"value", "uint256", big.NewInt(7)})},
		},
		{
			"ERC-721 Transfer",
			log(transfer, []ethrpc.Hash{word(alice), word(bob), word(uint64(5))}, nil),
			&Event{"Transfer", transfer, args(Arg{"from", "address", alice}, Arg{"to", "address", bob}, Arg{"tokenId", "uint256", big.NewInt(5)})},
		},
		{
			"ApprovalForAll",
			log("ApprovalForAll(address,address,bool)", []ethrpc.Hash{word(alice), word(bob)}, solabi.Encode(true)),
			&Event{"ApprovalForAll", "ApprovalForAll(address,address,bool)", args(Arg{"owner", "address", alice}, Arg{"operator", "address", bob}, Arg{"approved", "bool", true})},
		},
		{
			"Safe 1.3.0 ExecutionSuccess",
			log("ExecutionSuccess(bytes32,uint256)", nil, solabi.Encode(txID, uint64(3))),
			&Event{"ExecutionSuccess", "ExecutionSuccess(bytes32,uint256)", args(Arg{"txHash", "bytes32", txID}, Arg{"payment", "uint256", big.NewInt(3)})},
		},
		{
			"Safe 1.4.1 ExecutionFailure",
			log("ExecutionFailure(bytes32,uint256)", []ethrpc.Hash{txID}, solabi.Encode(uint64(3))),
			&Event{"ExecutionFailure", "ExecutionFailure(bytes32,uint256)", args(Arg{"txHash", "bytes32", txID}, Arg{"payment", "uint256", big.NewInt(3)})},
		},
		{
			"Safe 1.3.0 AddedOwner",
			log("AddedOwner(address)", nil, solabi.Encode(alice)),
			&Event{"AddedOwner", "AddedOwner(address)", args(Arg{"owner", "address", alice})},
		},
		{
			"Safe 1.4.1 AddedOwner",
			log("AddedOwner(address)", []ethrpc.Hash{word(alice)}, nil),
			&Event{"AddedOwner", "AddedOwner(address)", args(Arg{"owner", "address", alice})},
		},
		{
			"ApproveHash",
			log("ApproveHash(bytes32,address)", []ethrpc.Hash{txID, word(alice)}, nil),
			&Event{"ApproveHash", "ApproveHash(bytes32,address)", args(Arg{"approvedHash", "bytes32", txID}, Arg{"owner", "address", alice})},
		},
		{
			"SafeMultiSigTransaction",
			log("SafeMultiSigTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes,bytes)", nil, solabi.Encode(
				alice, uint64(1), []byte{0x12, 0x34}, uint64(1), uint64(2), uint64(3), uint64(4), bob, alice, []byte("sig"), []byte("info"),
			)),
			&Event{"SafeMultiSigTransaction", "SafeMultiSigTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes,bytes)", args(
				Arg{"to", "address", alice}, Arg{"value", "uint256", big.NewInt(1)}, Arg{"data", "bytes", ethrpc.Bytes{0x12, 0x34}},
				Arg{"operation", "uint8", uint64(1)}, Arg{"safeTxGas", "uint256", big.NewInt(2)}, Arg{"baseGas", "uint256", big.NewInt(3)},
				Arg{"gasPrice", "uint256", big.NewInt(4)}, Arg{"gasToken", "address", bob}, Arg{"refundReceiver", "address", alice},
				Arg{"signatures", "bytes", ethrpc.Bytes("sig")}, Arg{"additionalInfo", "bytes", ethrpc.Bytes("info")},
			)},
		},
		{
			"Safe 1.3.0 ProxyCreation",
			log("ProxyCreation(address,address)", nil, solabi.Encode(alice, bob)),
			&Event{"ProxyCreation", "ProxyCreation(address,address)", args(Arg{"proxy", "address", alice}, Arg{"singleton", "address", bob})},
		},
		{
			"Safe 1.4.1 ProxyCreation",
			log("ProxyCreation(address,address)", []ethrpc.Hash{word(alice)}, solabi.Encode(bob)),
			&Event{"ProxyCreation", "ProxyCreation(address,address)", args(Arg{"proxy", "address", alice}, Arg{"singleton", "address", bob})},
		},
	}
	for _, test := range tests {
		got := Decode(test.log)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: got %+v, want %+v", test.name, got, test.want)
		}
	}
}

func TestDecodeRejectsOtherLayouts(t *testing.T) {
	const transfer = "Transfer(address,address,uint256)"
	dirty := word(alice)
	dirty[0] = 1
	for name, l := range map[string]ethrpc.Log{
		"no topics":                        {},
		"unknown event":                    log("Unknown(uint256)", nil, solabi.Encode(uint64(1))),
		"Transfer without data":            log(transfer, []ethrpc.Hash{word(alice), word(bob)}, nil),
		"Transfer with more data":          log(transfer, []ethrpc.Hash{word(alice), word(bob)}, solabi.Encode(uint64(1), uint64(2))),
		"Transfer with one topic":          log(transfer, []ethrpc.Hash{word(alice)}, solabi.Encode(uint64(1))),
		"Transfer to a non-address":        log(transfer, []ethrpc.Hash{word(alice), dirty}, solabi.Encode(uint64(1))),
		"ExecutionSuccess without payment": log("ExecutionSuccess(bytes32,uint256)", nil, solabi.Encode(txID)),
		"truncated bytes":                  log("SafeModuleTransaction(address,address,uint256,bytes,uint8)", nil, solabi.Encode(alice, bob, uint64(1), []byte("data"), uint64(0))[:150]),
		"operation too large":              log("SafeModuleTransaction(address,address,uint256,bytes,uint8)", nil, solabi.Encode(alice, bob, uint64(1), []byte("data"), uint64(256))),
	} {
		if got := Decode(l); got != nil {
			t.Errorf("%s: got %+v, want nil", name, got)
		}
	}
}

func TestDefinitionsHaveUniqueLayouts(t *testing.T) {
	// A log must decode as one definition at most, or the output would depend on
	// their order.
	for topic, defs := range definitions {
		seen := map[int]bool{}
		for _, def := range defs {
			indexed := 0
			for _, p := range def.params {
				if p.indexed {
					indexed++
				}
			}
			if seen[indexed] {
				t.Errorf("%x: two definitions of %s have %d indexed parameters", topic, def.signature, indexed)
			}
			seen[indexed] = true
		}
	}
}
