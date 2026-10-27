package executions

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

var (
	safe    = ethrpc.MustParseAddress("0x5afe000000000000000000000000000000000001")
	relayer = ethrpc.MustParseAddress("0x7e1a000000000000000000000000000000000002")
	target  = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000003")
)

// chain is a fake node for Ethereum Mainnet, with a Safe's logs and the data
// that the decoding sources read.
type chain struct {
	logs []ethrpc.Log
	// nonce is the Safe's nonce at the last block, or nil if reading it fails.
	nonce *big.Int
	// txs are the transactions by hash.
	txs map[ethrpc.Hash]ethrpc.Transaction
	// traces are the callTracer results by transaction hash. A transaction without
	// one fails to trace, like on a node that doesn't serve traces.
	traces map[ethrpc.Hash]any
}

// serve starts the node, and returns a client for it.
func (c *chain) serve(t *testing.T) *ethrpc.Client {
	t.Helper()
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
		fail := func(message string) { res["error"] = map[string]any{"code": -32000, "message": message} }
		switch req.Method {
		case "eth_chainId":
			res["result"] = "0x1"
		case "eth_getLogs":
			var filter ethrpc.LogFilter
			json.Unmarshal(req.Params[0], &filter)
			logs := []ethrpc.Log{}
			for _, log := range c.logs {
				if log.BlockNumber >= filter.FromBlock && log.BlockNumber <= filter.ToBlock &&
					slices.Contains(filter.Addresses, log.Address) && slices.Contains(filter.Topics[0], log.Topics[0]) {
					logs = append(logs, log)
				}
			}
			res["result"] = logs
		case "eth_call":
			var call ethrpc.CallRequest
			var block ethrpc.BlockNumber
			json.Unmarshal(req.Params[0], &call)
			json.Unmarshal(req.Params[1], &block)
			if c.nonce == nil || call.To != safe || block != 200 || string(call.Data) != string(solabi.Call(nonceSelector)) {
				fail("execution reverted")
				break
			}
			word := solabi.Uint(c.nonce)
			res["result"] = ethrpc.Bytes(word[:])
		case "eth_getTransactionByHash":
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			if tx, ok := c.txs[hash]; ok {
				res["result"] = tx
			} else {
				res["result"] = nil
			}
		case "debug_traceTransaction":
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			if trace, ok := c.traces[hash]; ok {
				res["result"] = trace
			} else {
				res["error"] = map[string]any{"code": -32601, "message": "the method debug_traceTransaction does not exist/is not available"}
			}
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
	return eth
}

// transaction returns a Safe transaction of the Safe, on Mainnet, that calls
// target with data and uses nonce.
func transaction(data string, nonce int64) *safenet.SafeTransaction {
	return &safenet.SafeTransaction{
		ChainID:        big.NewInt(1),
		Safe:           safe,
		To:             target,
		Value:          big.NewInt(nonce),
		Data:           []byte(data),
		Operation:      safenet.Operation(nonce % 2),
		SafeTxGas:      big.NewInt(3),
		BaseGas:        big.NewInt(4),
		GasPrice:       big.NewInt(5),
		GasToken:       ethrpc.Address{19: 6},
		RefundReceiver: ethrpc.Address{19: 7},
		Nonce:          big.NewInt(nonce),
	}
}

// fields returns the ABI values of a Safe transaction's fields, without its
// nonce, followed by extra.
func fields(tx *safenet.SafeTransaction, extra ...any) []any {
	return append([]any{
		tx.To, tx.Value, []byte(tx.Data), uint64(tx.Operation), tx.SafeTxGas, tx.BaseGas, tx.GasPrice, tx.GasToken, tx.RefundReceiver, []byte("signatures"),
	}, extra...)
}

// execCall returns the calldata of a call to execTransaction with tx.
func execCall(tx *safenet.SafeTransaction) ethrpc.Bytes {
	return solabi.Call(execTransaction, fields(tx)...)
}

// eventLog returns the SafeMultiSigTransaction log that SafeL2 logs for tx.
func eventLog(tx *safenet.SafeTransaction, hash ethrpc.Hash, block, index uint64) ethrpc.Log {
	nonce := solabi.Uint(tx.Nonce)
	info := append(nonce[:], make([]byte, 64)...)
	return ethrpc.Log{
		Address:         safe,
		Topics:          []ethrpc.Hash{multiSigEvent},
		Data:            solabi.Encode(fields(tx, info)...),
		BlockNumber:     ethrpc.BlockNumber(block),
		TransactionHash: hash,
		LogIndex:        ethrpc.Quantity(index),
	}
}

// executionLog returns the log that a Safe logs when it executes the
// transaction with the hash safeTxHash: with the hash as a topic like Safe
// 1.4.1 and later do, or in the data like 1.3.0.
func executionLog(event, safeTxHash, hash ethrpc.Hash, indexed bool, block, index uint64) ethrpc.Log {
	log := ethrpc.Log{
		Address:         safe,
		Topics:          []ethrpc.Hash{event},
		BlockNumber:     ethrpc.BlockNumber(block),
		TransactionHash: hash,
		LogIndex:        ethrpc.Quantity(index),
	}
	payment := solabi.Uint(big.NewInt(9))
	if indexed {
		log.Topics = append(log.Topics, safeTxHash)
		log.Data = payment[:]
	} else {
		log.Data = append(safeTxHash[:], payment[:]...)
	}
	return log
}

func hash(n byte) ethrpc.Hash { return ethrpc.Hash{0: 0xaa, 31: n} }

// service starts a Safe Transaction Service that has the transactions by Safe
// transaction hash, and fails requests for those in failing, and returns a
// client for it.
func service(t *testing.T, transactions map[ethrpc.Hash]*safenet.SafeTransaction, failing ...ethrpc.Hash) *txservice.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/multisig-transactions/")
		h, err := ethrpc.ParseHash(strings.TrimSuffix(id, "/"))
		tx := transactions[h]
		switch {
		case slices.Contains(failing, h):
			http.Error(w, "\x1b[31munavailable", http.StatusInternalServerError)
		case !ok || err != nil || tx == nil:
			http.NotFound(w, r)
		default:
			// Numbers are JSON numbers or strings, and data is null when empty.
			json.NewEncoder(w).Encode(map[string]any{
				"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
				"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.Uint64(), "baseGas": tx.BaseGas.Uint64(),
				"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
				"nonce": tx.Nonce.Uint64(),
			})
		}
	}))
	t.Cleanup(server.Close)
	return txservice.NewClient(server.URL, ethrpc.Mainnet)
}

func TestFind(t *testing.T) {
	const last = 200
	// Five executions, at the nonces 10 to 14, and a Safe whose nonce is 15 at the
	// last block.
	viaEvent := transaction("event", 10)
	viaCalldata := transaction("calldata", 11)
	viaTrace := transaction("trace", 12)
	viaService := transaction("service", 13)
	unknown := transaction("unknown", 14)

	// A transaction that a relayer makes, with a call to the Safe that reverted
	// before the one that succeeded, and calls that aren't to the Safe.
	trace := func(tx *safenet.SafeTransaction) map[string]any {
		call := func(to ethrpc.Address, input ethrpc.Bytes, errMsg string, calls ...any) map[string]any {
			return map[string]any{"type": "CALL", "to": to, "input": input, "error": errMsg, "calls": calls}
		}
		other := transaction("other", 99)
		return call(relayer, nil, "",
			call(safe, execCall(other), "execution reverted", call(safe, execCall(tx), "")),
			call(target, execCall(tx), ""),
			call(relayer, nil, "", call(safe, execCall(tx), "")),
		)
	}
	c := &chain{
		nonce: big.NewInt(15),
		txs: map[ethrpc.Hash]ethrpc.Transaction{
			hash(1): {To: &relayer, Input: []byte("not a Safe call")},
			hash(2): {To: &safe, Input: execCall(viaCalldata)},
			hash(3): {To: &relayer, Input: []byte("relay")},
			hash(4): {To: &relayer, Input: []byte("relay")},
			hash(5): {To: &safe, Input: execCall(transaction("another transaction", 14))},
		},
		traces: map[ethrpc.Hash]any{hash(3): trace(viaTrace)},
	}
	c.logs = []ethrpc.Log{
		// A Safe 1.4.1 L2.
		eventLog(viaEvent, hash(1), 100, 0),
		executionLog(executionSuccessEvent, viaEvent.Hash(), hash(1), true, 100, 1),
		// A Safe 1.3.0, which doesn't index the hash, and a failed transaction.
		executionLog(executionFailureEvent, viaCalldata.Hash(), hash(2), false, 110, 0),
		executionLog(executionSuccessEvent, viaTrace.Hash(), hash(3), true, 120, 3),
		executionLog(executionSuccessEvent, viaService.Hash(), hash(4), true, 130, 0),
		executionLog(executionSuccessEvent, unknown.Hash(), hash(5), true, 140, 0),
		// Logs that aren't executions: another Safe's, a removed one, and malformed
		// ones.
		func() ethrpc.Log {
			l := executionLog(executionSuccessEvent, hash(7), hash(6), true, 150, 0)
			l.Address = relayer
			return l
		}(),
		func() ethrpc.Log {
			l := executionLog(executionSuccessEvent, hash(7), hash(6), true, 150, 1)
			l.Removed = true
			return l
		}(),
		func() ethrpc.Log {
			l := executionLog(executionSuccessEvent, hash(7), hash(6), true, 150, 2)
			l.Data = nil
			return l
		}(),
		// An execution after the last block.
		executionLog(executionSuccessEvent, hash(8), hash(6), true, last+1, 0),
	}
	f := &Finder{
		Eth: c.serve(t),
		Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{
			viaService.Hash(): viaService,
			// A service that reports another transaction than the one asked for.
			unknown.Hash(): transaction("not what was executed", 14),
		}),
	}

	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }
	got, err := f.Find(t.Context(), safe, 0, last)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	want := []Execution{
		{100, hash(1), 1, viaEvent.Hash(), true, big.NewInt(9), SourceEvent, viaEvent},
		{110, hash(2), 0, viaCalldata.Hash(), false, big.NewInt(9), SourceCalldata, viaCalldata},
		{120, hash(3), 3, viaTrace.Hash(), true, big.NewInt(9), SourceTrace, viaTrace},
		{130, hash(4), 0, viaService.Hash(), true, big.NewInt(9), SourceService, viaService},
		{140, hash(5), 0, unknown.Hash(), true, big.NewInt(9), "", nil},
	}
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			if i >= len(want) || !reflect.DeepEqual(got[i], want[i]) {
				g, _ := json.Marshal(got[i])
				w, _ := json.Marshal(want[min(i, len(want)-1)])
				t.Errorf("execution %d:\ngot  %s\nwant %s", i, g, w)
			}
		}
		if len(got) != len(want) {
			t.Errorf("got %d executions, want %d", len(got), len(want))
		}
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
}

func TestFindWithoutNonce(t *testing.T) {
	// Without the nonce, the calldata and traces can't be checked against the hash,
	// so they aren't used, and the service has the transaction.
	tx := transaction("calldata", 11)
	c := &chain{
		txs:  map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}},
		logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t), Service: service(t, map[ethrpc.Hash]*safenet.SafeTransaction{tx.Hash(): tx})}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }

	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].Source != SourceService || !reflect.DeepEqual(got[0].Transaction, tx) {
		t.Errorf("Find: got %+v, want the transaction from the service", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "reading the nonce") {
		t.Errorf("warnings: got %v, want one about reading the nonce", warnings)
	}

	// Without a service either, the transaction is unknown.
	f.Service = nil
	if got, _ = f.Find(t.Context(), safe, 0, 200); len(got) != 1 || got[0].Transaction != nil || got[0].Source != "" {
		t.Errorf("Find without a service: got %+v, want an unknown transaction", got)
	}
}

func TestFindWithNonceBeforeTheSearch(t *testing.T) {
	// A nonce that is lower than the number of executions can't be right.
	tx := transaction("calldata", 0)
	c := &chain{
		nonce: big.NewInt(0),
		txs:   map[ethrpc.Hash]ethrpc.Transaction{hash(1): {To: &safe, Input: execCall(tx)}},
		logs:  []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)},
	}
	f := &Finder{Eth: c.serve(t)}
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].Transaction != nil {
		t.Errorf("Find: got %+v, want an unknown transaction", got)
	}
}

func TestFindServiceFailure(t *testing.T) {
	// A service that fails leaves the transaction unknown, with a warning that
	// doesn't have its response as is.
	tx := transaction("service", 1)
	c := &chain{logs: []ethrpc.Log{executionLog(executionSuccessEvent, tx.Hash(), hash(1), true, 100, 0)}}
	f := &Finder{Eth: c.serve(t), Service: service(t, nil, tx.Hash())}
	var warnings []error
	f.Warn = func(err error) { warnings = append(warnings, err) }

	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].Transaction != nil {
		t.Errorf("Find: got %+v, want an unknown transaction", got)
	}
	for _, w := range warnings {
		if strings.Contains(w.Error(), "\x1b") {
			t.Errorf("warning %q has a terminal escape sequence", w)
		}
	}
	if !slices.ContainsFunc(warnings, func(w error) bool { return strings.Contains(w.Error(), "500") }) {
		t.Errorf("warnings: got %v, want one with the HTTP status", warnings)
	}
}

func TestFindReturnsEmptyList(t *testing.T) {
	f := &Finder{Eth: (&chain{}).serve(t)}
	got, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("Find: got %#v, want an empty list", got)
	}
}

func TestFindLogsFailure(t *testing.T) {
	f := &Finder{Eth: (&chain{}).serve(t)}
	f.Eth = brokenClient(t)
	if _, err := f.Find(t.Context(), safe, 0, 200); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("listing Safe transactions of %s", safe)) {
		t.Errorf("Find: got error %v, want one about listing Safe transactions", err)
	}
}

// brokenClient returns a client for a node that answers every request but the
// chain ID with an error.
func brokenClient(t *testing.T) *ethrpc.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "eth_chainId" {
			res["result"] = "0x1"
		} else {
			res["error"] = map[string]any{"code": -32000, "message": "broken"}
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	eth, err := ethrpc.NewClient(t.Context(), ethrpc.Mainnet, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return eth
}

func TestDecodeCall(t *testing.T) {
	tx := transaction("calldata", 3)
	tx.Nonce = nil
	got := decodeCall(safe, 1, execCall(transaction("calldata", 3)))
	if !reflect.DeepEqual(got, tx) {
		t.Errorf("decodeCall: got %+v, want %+v", got, tx)
	}
	if selector := fmt.Sprintf("%x", execTransaction); selector != "6a761202" {
		t.Errorf("execTransaction selector: got %s, want 6a761202", selector)
	}

	call := execCall(tx)
	for name, input := range map[string][]byte{
		"empty":           nil,
		"other function":  solabi.Call(solabi.Selector("transfer(address,uint256)"), target, uint64(1)),
		"truncated":       call[:len(call)-40],
		"invalid address": append(call[:4:4], append(make([]byte, 0), call[4:]...)...),
	} {
		if name == "invalid address" {
			input[4] = 1
		}
		if got := decodeCall(safe, 1, input); got != nil {
			t.Errorf("decodeCall(%s): got %+v, want nil", name, got)
		}
	}
}

func TestMatches(t *testing.T) {
	tx := transaction("data", 5)
	if !matches(tx, tx.Hash()) {
		t.Error("matches: a transaction doesn't match its hash")
	}
	if matches(tx, hash(1)) || matches(nil, tx.Hash()) {
		t.Error("matches: a transaction matches another hash, or nil matches")
	}
	for name, change := range map[string]func(*safenet.SafeTransaction){
		"no nonce":          func(tx *safenet.SafeTransaction) { tx.Nonce = nil },
		"no value":          func(tx *safenet.SafeTransaction) { tx.Value = nil },
		"wide gas price":    func(tx *safenet.SafeTransaction) { tx.GasPrice = new(big.Int).Lsh(big.NewInt(1), 256) },
		"negative base gas": func(tx *safenet.SafeTransaction) { tx.BaseGas = big.NewInt(-1) },
		"unknown operation": func(tx *safenet.SafeTransaction) { tx.Operation = 2 },
	} {
		tx := transaction("data", 5)
		change(tx)
		if matches(tx, transaction("data", 5).Hash()) {
			t.Errorf("matches(%s): got true, want false", name)
		}
	}
}
