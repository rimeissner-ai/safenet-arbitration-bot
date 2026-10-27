package native

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	safe  = ethrpc.MustParseAddress("0x5afe000000000000000000000000000000000001")
	other = ethrpc.MustParseAddress("0x5afe000000000000000000000000000000000002")
	alice = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000001")
	bob   = ethrpc.MustParseAddress("0xb0b0000000000000000000000000000000000002")
)

// receivedLog returns a SafeReceived log of emitter in block.
func receivedLog(emitter, sender ethrpc.Address, value int64, block uint64, index uint64) ethrpc.Log {
	data := solabi.Uint(big.NewInt(value))
	return ethrpc.Log{
		Address:         emitter,
		Topics:          []ethrpc.Hash{safeReceivedEvent, solabi.Address(sender)},
		Data:            data[:],
		BlockNumber:     ethrpc.BlockNumber(block),
		TransactionHash: ethrpc.Hash{0: byte(block), 31: byte(index)},
		LogIndex:        ethrpc.Quantity(index),
	}
}

// node starts a node whose eth_getLogs answers with the logs that match the
// filter, and returns a client for it, and the filters that it was asked for.
func node(t *testing.T, logs []ethrpc.Log) (*ethrpc.Client, *[]ethrpc.LogFilter) {
	t.Helper()
	var filters []ethrpc.LogFilter
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_getLogs":
			var filter ethrpc.LogFilter
			json.Unmarshal(req.Params[0], &filter)
			filters = append(filters, filter)
			matching := []ethrpc.Log{}
			for _, log := range logs {
				if matches(filter, log) {
					matching = append(matching, log)
				}
			}
			res["result"] = matching
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Mainnet, server.URL)
	if err != nil {
		t.Fatalf("ethrpc.NewClient: %v", err)
	}
	return eth, &filters
}

// matches reports whether log is in the filter's block range and matches its
// addresses and topics.
func matches(filter ethrpc.LogFilter, log ethrpc.Log) bool {
	if log.BlockNumber < filter.FromBlock || log.BlockNumber > filter.ToBlock {
		return false
	}
	if len(filter.Addresses) > 0 && !slices.Contains(filter.Addresses, log.Address) {
		return false
	}
	for i, values := range filter.Topics {
		if len(values) > 0 && (i >= len(log.Topics) || !slices.Contains(values, log.Topics[i])) {
			return false
		}
	}
	return true
}

func TestReceivedBy(t *testing.T) {
	removed := receivedLog(safe, alice, 9, 105, 0)
	removed.Removed = true
	short := receivedLog(safe, alice, 9, 106, 0)
	short.Data = short.Data[:31]
	indexedValue := receivedLog(safe, alice, 0, 107, 0)
	indexedValue.Topics = append(indexedValue.Topics, ethrpc.Hash{31: 9})
	indexedValue.Data = nil

	logs := []ethrpc.Log{
		receivedLog(safe, alice, 10, 100, 0),
		receivedLog(other, alice, 20, 101, 0), // Logged by another contract.
		receivedLog(safe, bob, 30, 102, 3),
		receivedLog(safe, alice, 40, 104, 2),
		removed, short, indexedValue,
		receivedLog(safe, alice, 50, 109, 0), // After the range.
	}
	eth, filters := node(t, logs)

	got, err := ReceivedBy(t.Context(), eth, safe, nil, 90, 108)
	if err != nil {
		t.Fatalf("ReceivedBy: %v", err)
	}
	want := []Transfer{
		{100, ethrpc.Hash{0: 100}, 0, alice, safe, big.NewInt(10)},
		{102, ethrpc.Hash{0: 102, 31: 3}, 3, bob, safe, big.NewInt(30)},
		{104, ethrpc.Hash{0: 104, 31: 2}, 2, alice, safe, big.NewInt(40)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReceivedBy: got %+v, want %+v", got, want)
	}
	if len(*filters) != 1 || !slices.Equal((*filters)[0].Addresses, []ethrpc.Address{safe}) {
		t.Errorf("ReceivedBy: queried %+v, want one query of the Safe's logs", *filters)
	}

	got, err = ReceivedBy(t.Context(), eth, safe, &alice, 90, 108)
	if err != nil {
		t.Fatalf("ReceivedBy from alice: %v", err)
	}
	if want := []Transfer{want[0], want[2]}; !reflect.DeepEqual(got, want) {
		t.Errorf("ReceivedBy from alice: got %+v, want %+v", got, want)
	}
}

// TestSafeReceivedEvent checks the topic of the event against the signature
// that the Safe singletons log.
func TestSafeReceivedEvent(t *testing.T) {
	want := ethrpc.MustParseHash("0x3d0ce9bfc3ed7d6862dbb28b2dea94561fe714a1b4d019aa8af39730d1ad7c3d")
	if safeReceivedEvent != want {
		t.Errorf("SafeReceived topic: got %s, want %s", safeReceivedEvent, want)
	}
}
