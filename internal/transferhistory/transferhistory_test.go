package transferhistory

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

var (
	safe  = ethrpc.Address{0: 0x5a, 19: 1}
	alice = ethrpc.Address{0: 0xa1, 19: 2}
	token = ethrpc.Address{0: 0x70, 19: 3}
	nft   = ethrpc.Address{0: 0x72, 19: 4}
)

// chain is the receipts of a fake node, by transaction hash.
type chain map[ethrpc.Hash]ethrpc.Receipt

// node starts a node that serves the receipts of c, and returns a client for
// it.
func node(t *testing.T, c chain) *ethrpc.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64        `json:"id"`
			Method string        `json:"method"`
			Params []ethrpc.Hash `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0xa4b1"
		case "eth_getTransactionReceipt":
			if receipt, ok := c[req.Params[0]]; ok {
				res["result"] = receipt
			} else {
				res["result"] = nil
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	eth, err := ethrpc.NewClient(t.Context(), ethrpc.ArbitrumOne, server.URL)
	if err != nil {
		t.Fatalf("ethrpc.NewClient: %v", err)
	}
	return eth
}

// service starts a Safe Transaction Service that lists transfers as the
// transfers of safe, and returns a client for it.
func service(t *testing.T, transfers []map[string]any) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.URL.Path, "/api/v1/safes/"+safe.String()+"/transfers/") {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"count": len(transfers), "results": transfers})
	}))
	t.Cleanup(server.Close)
	return txservice.NewClient(server.URL, ethrpc.ArbitrumOne)
}

// listed is a transfer as the service lists it. A transfer with a log index has
// an ID of a log, and one without an ID of a trace.
func listed(kind string, block uint64, tx ethrpc.Hash, logIndex *uint64, tokenAddress *ethrpc.Address, from, to ethrpc.Address, value, tokenID *big.Int) map[string]any {
	id := "i" + strings.TrimPrefix(tx.String(), "0x") + "0"
	if logIndex != nil {
		id = fmt.Sprintf("e%s%d", strings.TrimPrefix(tx.String(), "0x"), *logIndex)
	}
	t := map[string]any{
		"type": kind, "blockNumber": block, "transactionHash": tx, "transferId": id,
		"tokenAddress": tokenAddress, "from": from, "to": to, "value": nil, "tokenId": nil,
	}
	if value != nil {
		t["value"] = value.String()
	}
	if tokenID != nil {
		t["tokenId"] = tokenID.String()
	}
	return t
}

func word(x int64) ethrpc.Bytes {
	w := solabi.Uint64(uint64(x))
	return w[:]
}

func topic(a ethrpc.Address) ethrpc.Hash { return ethrpc.Hash(solabi.Address(a)) }

func index(i uint64) *uint64 { return &i }

// history is a Safe's history: an ERC-20 transfer out of the Safe, an ERC-721
// transfer into it, native currency sent to it with a SafeReceived event, and
// native currency that it sent, which no log shows, then a transfer after the
// Safe block.
func history() (chain, []map[string]any) {
	tx := func(n byte) ethrpc.Hash { return ethrpc.Hash{0: 0x7a, 31: n} }
	c := chain{
		tx(1): {TransactionHash: tx(1), BlockNumber: 10, Status: 1, Logs: []ethrpc.Log{
			{Address: alice, Topics: []ethrpc.Hash{{1}}, LogIndex: 4},
			{Address: token, Topics: []ethrpc.Hash{transferEvent, topic(safe), topic(alice)}, Data: word(100), LogIndex: 5},
		}},
		tx(2): {TransactionHash: tx(2), BlockNumber: 11, Status: 1, Logs: []ethrpc.Log{
			{Address: nft, Topics: []ethrpc.Hash{transferEvent, topic(alice), topic(safe), ethrpc.Hash(solabi.Uint64(7))}, LogIndex: 0},
		}},
		tx(3): {TransactionHash: tx(3), BlockNumber: 12, Status: 1, Logs: []ethrpc.Log{
			{Address: safe, Topics: []ethrpc.Hash{safeReceivedEvent, topic(alice)}, Data: word(9), LogIndex: 2},
		}},
		tx(4): {TransactionHash: tx(4), BlockNumber: 12, Status: 1},
	}
	// The service lists the transfers newest first.
	transfers := []map[string]any{
		listed(txservice.ERC20Transfer, 21, tx(5), index(0), &token, safe, alice, big.NewInt(1), nil),
		listed(txservice.EtherTransfer, 12, tx(4), nil, nil, safe, alice, big.NewInt(3), nil),
		listed(txservice.EtherTransfer, 12, tx(3), nil, nil, alice, safe, big.NewInt(9), nil),
		listed(txservice.ERC721Transfer, 11, tx(2), index(0), &nft, alice, safe, nil, big.NewInt(7)),
		listed(txservice.ERC20Transfer, 10, tx(1), index(5), &token, safe, alice, big.NewInt(100), nil),
	}
	return c, transfers
}

func TestFromService(t *testing.T) {
	c, transfers := history()
	got, err := FromService(t.Context(), service(t, transfers), node(t, c), safe, 20)
	if err != nil {
		t.Fatalf("FromService: %v", err)
	}
	tx := func(n byte) ethrpc.Hash { return ethrpc.Hash{0: 0x7a, 31: n} }
	want := []Transfer{
		{ERC20, 10, tx(1), index(5), &token, safe, alice, big.NewInt(100), nil, CheckLog},
		{ERC721, 11, tx(2), index(0), &nft, alice, safe, nil, big.NewInt(7), CheckLog},
		{Native, 12, tx(3), index(2), nil, alice, safe, big.NewInt(9), nil, CheckLog},
		{Native, 12, tx(4), nil, nil, safe, alice, big.NewInt(3), nil, CheckTransaction},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Errorf("FromService:\ngot  %s\nwant %s", g, w)
	}
}

func TestFromServiceUnconfirmed(t *testing.T) {
	other := ethrpc.Address{0: 0x07}
	tests := []struct {
		name   string
		change func(chain, []map[string]any)
	}{
		{"value", func(_ chain, l []map[string]any) { l[4]["value"] = "101" }},
		{"recipient", func(_ chain, l []map[string]any) { l[4]["to"] = other }},
		{"token", func(_ chain, l []map[string]any) { l[4]["tokenAddress"] = other }},
		{"token ID", func(_ chain, l []map[string]any) { l[3]["tokenId"] = "8" }},
		{"block", func(_ chain, l []map[string]any) { l[4]["blockNumber"] = 9 }},
		// Log 4 of the transaction isn't a Transfer event.
		{"log", func(_ chain, l []map[string]any) { l[4]["transferId"] = l[4]["transferId"].(string)[:65] + "4" }},
		{"neither from nor to the Safe", func(_ chain, l []map[string]any) { l[1]["from"] = other }},
		{"reverted", func(c chain, _ []map[string]any) {
			r := c[ethrpc.Hash{0: 0x7a, 31: 4}]
			r.Status = 0
			c[r.TransactionHash] = r
		}},
		{"unknown transaction", func(c chain, _ []map[string]any) { delete(c, ethrpc.Hash{0: 0x7a, 31: 1}) }},
		{"removed log", func(c chain, _ []map[string]any) { c[ethrpc.Hash{0: 0x7a, 31: 1}].Logs[1].Removed = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, transfers := history()
			test.change(c, transfers)
			if got, err := FromService(t.Context(), service(t, transfers), node(t, c), safe, 20); err == nil {
				t.Errorf("FromService: got %+v, want an error", got)
			}
		})
	}
}

func TestFromServiceLaterBlocksOnly(t *testing.T) {
	c, transfers := history()
	// The transfers after the Safe block aren't checked, so one that the chain
	// doesn't confirm doesn't matter.
	got, err := FromService(t.Context(), service(t, transfers[:1]), node(t, c), safe, 20)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("FromService: got %+v, %v, want no transfers", got, err)
	}
}
