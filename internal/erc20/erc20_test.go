package erc20

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	safe   = ethrpc.MustParseAddress("0x5afe000000000000000000000000000000000001")
	other  = ethrpc.MustParseAddress("0x000000000000000000000000000000000000dEaD")
	alice  = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000001")
	bob    = ethrpc.MustParseAddress("0xb0b0000000000000000000000000000000000002")
	tokenA = ethrpc.MustParseAddress("0x7000000000000000000000000000000000000001")
	tokenB = ethrpc.MustParseAddress("0x7000000000000000000000000000000000000002")
)

// transferLog returns a Transfer log of token in block.
func transferLog(token, from, to ethrpc.Address, value int64, block uint64, index uint64) ethrpc.Log {
	data := solabi.Uint(big.NewInt(value))
	return ethrpc.Log{
		Address:         token,
		Topics:          []ethrpc.Hash{transferEvent, solabi.Address(from), solabi.Address(to)},
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
	if len(filter.Addresses) > 0 && !contains(filter.Addresses, log.Address) {
		return false
	}
	for i, values := range filter.Topics {
		if len(values) > 0 && (i >= len(log.Topics) || !contains(values, log.Topics[i])) {
			return false
		}
	}
	return true
}

func contains[T comparable](values []T, value T) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func TestTransfersFrom(t *testing.T) {
	erc721 := transferLog(tokenB, safe, alice, 0, 105, 1)
	erc721.Topics = append(erc721.Topics, ethrpc.Hash{31: 7})
	erc721.Data = nil
	removed := transferLog(tokenA, safe, alice, 9, 106, 0)
	removed.Removed = true
	short := transferLog(tokenA, safe, alice, 9, 107, 0)
	short.Data = short.Data[:31]

	logs := []ethrpc.Log{
		transferLog(tokenA, safe, alice, 10, 100, 0),
		transferLog(tokenB, other, alice, 20, 101, 0), // From another account.
		transferLog(tokenB, safe, bob, 30, 102, 3),
		transferLog(tokenA, alice, safe, 40, 103, 0), // To the Safe.
		transferLog(tokenA, safe, alice, 50, 104, 2),
		erc721, removed, short,
		transferLog(tokenA, safe, alice, 60, 109, 0), // After the range.
	}
	eth, filters := node(t, logs)

	got, err := TransfersFrom(t.Context(), eth, safe, nil, 90, 108)
	if err != nil {
		t.Fatalf("TransfersFrom: %v", err)
	}
	want := []Transfer{
		{100, ethrpc.Hash{0: 100}, 0, tokenA, safe, alice, big.NewInt(10)},
		{102, ethrpc.Hash{0: 102, 31: 3}, 3, tokenB, safe, bob, big.NewInt(30)},
		{104, ethrpc.Hash{0: 104, 31: 2}, 2, tokenA, safe, alice, big.NewInt(50)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TransfersFrom: got %+v, want %+v", got, want)
	}
	if len(*filters) != 1 || (*filters)[0].FromBlock != 90 || (*filters)[0].ToBlock != 108 {
		t.Errorf("TransfersFrom: queried %+v, want one query of blocks 90 to 108", *filters)
	}

	got, err = TransfersFrom(t.Context(), eth, safe, &alice, 90, 108)
	if err != nil {
		t.Fatalf("TransfersFrom to alice: %v", err)
	}
	if want := []Transfer{want[0], want[2]}; !reflect.DeepEqual(got, want) {
		t.Errorf("TransfersFrom to alice: got %+v, want %+v", got, want)
	}
}

func TestTransfersTo(t *testing.T) {
	erc721 := transferLog(tokenB, alice, safe, 0, 105, 1)
	erc721.Topics = append(erc721.Topics, ethrpc.Hash{31: 7})
	erc721.Data = nil

	logs := []ethrpc.Log{
		transferLog(tokenA, alice, safe, 10, 100, 0),
		transferLog(tokenA, safe, alice, 20, 101, 0), // From the Safe.
		transferLog(tokenB, bob, safe, 30, 102, 3),
		transferLog(tokenA, alice, other, 40, 103, 0), // To another account.
		transferLog(tokenB, alice, safe, 50, 104, 2),
		erc721,
		transferLog(tokenA, alice, safe, 60, 109, 0), // After the range.
	}
	eth, filters := node(t, logs)

	got, err := TransfersTo(t.Context(), eth, safe, nil, 90, 108)
	if err != nil {
		t.Fatalf("TransfersTo: %v", err)
	}
	want := []Transfer{
		{100, ethrpc.Hash{0: 100}, 0, tokenA, alice, safe, big.NewInt(10)},
		{102, ethrpc.Hash{0: 102, 31: 3}, 3, tokenB, bob, safe, big.NewInt(30)},
		{104, ethrpc.Hash{0: 104, 31: 2}, 2, tokenB, alice, safe, big.NewInt(50)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TransfersTo: got %+v, want %+v", got, want)
	}
	// The sender topic is left open, rather than matching only the zero address.
	if len(*filters) != 1 || len((*filters)[0].Topics) != 3 || (*filters)[0].Topics[1] != nil {
		t.Errorf("TransfersTo: queried %+v, want one query with any sender", *filters)
	}

	got, err = TransfersTo(t.Context(), eth, safe, &alice, 90, 108)
	if err != nil {
		t.Fatalf("TransfersTo from alice: %v", err)
	}
	if want := []Transfer{want[0], want[2]}; !reflect.DeepEqual(got, want) {
		t.Errorf("TransfersTo from alice: got %+v, want %+v", got, want)
	}
}

func TestTransfersFromWithoutTransfers(t *testing.T) {
	eth, _ := node(t, nil)
	got, err := TransfersFrom(t.Context(), eth, safe, nil, 1, 10)
	if err != nil {
		t.Fatalf("TransfersFrom: %v", err)
	}
	// It is an empty list rather than nil, so that JSON output has an array.
	if got == nil || len(got) != 0 {
		t.Errorf("TransfersFrom: got %#v, want an empty list", got)
	}
}
