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
	// receipts are the transaction receipts by hash.
	receipts map[ethrpc.Hash]ethrpc.Receipt
	// noLogs makes eth_getLogs fail, to show that Find doesn't scan logs.
	noLogs bool
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
			if c.noLogs {
				fail("logs are not available")
				break
			}
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
		case "eth_getTransactionReceipt":
			var hash ethrpc.Hash
			json.Unmarshal(req.Params[0], &hash)
			if receipt, ok := c.receipts[hash]; ok {
				res["result"] = receipt
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
	return listingService(t, transactions, nil, failing...)
}

// listed is a transaction that a service lists as executed by the onchain
// transaction with the hash txHash.
type listed struct {
	tx     *safenet.SafeTransaction
	txHash ethrpc.Hash
}

// encodeTransaction encodes a Safe transaction as the service does: numbers are
// JSON numbers or strings, and data is null when empty.
func encodeTransaction(tx *safenet.SafeTransaction) map[string]any {
	return map[string]any{
		"safe": tx.Safe, "to": tx.To, "value": tx.Value.String(), "data": tx.Data,
		"operation": uint8(tx.Operation), "safeTxGas": tx.SafeTxGas.Uint64(), "baseGas": tx.BaseGas.Uint64(),
		"gasPrice": tx.GasPrice.String(), "gasToken": tx.GasToken, "refundReceiver": tx.RefundReceiver,
		"nonce": tx.Nonce.Uint64(),
	}
}

// listingService is like service, but also lists the transactions in executed
// as the Safe's executed transactions, in pages of two.
func listingService(t *testing.T, transactions map[ethrpc.Hash]*safenet.SafeTransaction, executed []listed, failing ...ethrpc.Hash) *txservice.Client {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/safes/"+safe.String()+"/multisig-transactions/" {
			q := r.URL.Query()
			if q.Get("executed") != "true" || q.Get("ordering") != "nonce" {
				http.Error(w, "unexpected query", http.StatusBadRequest)
				return
			}
			below, _ := new(big.Int).SetString(q.Get("nonce__lt"), 10)
			var page []listed
			for _, x := range executed {
				if below != nil && x.tx.Nonce.Cmp(below) < 0 {
					page = append(page, x)
				}
			}
			offset := 0
			fmt.Sscan(q.Get("offset"), &offset)
			page = page[min(offset, len(page)):]
			var next any
			if len(page) > 2 {
				page = page[:2]
				q.Set("offset", fmt.Sprint(offset+2))
				next = server.URL + r.URL.Path + "?" + q.Encode()
			}
			results := []any{}
			for _, x := range page {
				m := encodeTransaction(x.tx)
				m["safeTxHash"], m["transactionHash"] = x.tx.Hash(), x.txHash
				results = append(results, m)
			}
			json.NewEncoder(w).Encode(map[string]any{"next": next, "results": results})
			return
		}
		id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/multisig-transactions/")
		h, err := ethrpc.ParseHash(strings.TrimSuffix(id, "/"))
		tx := transactions[h]
		switch {
		case slices.Contains(failing, h):
			http.Error(w, "\x1b[31munavailable", http.StatusInternalServerError)
		case !ok || err != nil || tx == nil:
			http.NotFound(w, r)
		default:
			json.NewEncoder(w).Encode(encodeTransaction(tx))
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
	found, err := f.Find(t.Context(), safe, 0, last)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found.Complete {
		t.Error("Find: got complete executions, want only those in the range")
	}
	got := found.Executions

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

	found, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := found.Executions
	if len(got) != 1 || got[0].Source != SourceService || !reflect.DeepEqual(got[0].Transaction, tx) {
		t.Errorf("Find: got %+v, want the transaction from the service", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "reading the nonce") {
		t.Errorf("warnings: got %v, want one about reading the nonce", warnings)
	}

	// Without a service either, the transaction is unknown.
	f.Service = nil
	if found, _ = f.Find(t.Context(), safe, 0, 200); len(found.Executions) != 1 || found.Executions[0].Transaction != nil || found.Executions[0].Source != "" {
		t.Errorf("Find without a service: got %+v, want an unknown transaction", found.Executions)
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
	found, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := found.Executions
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

	found, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := found.Executions
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
	found, err := f.Find(t.Context(), safe, 0, 200)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := found.Executions
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

func TestFindFromService(t *testing.T) {
	// Three executions, at the nonces 0 to 2, of a Safe whose nonce is 3 at the
	// last block: one decoded from its calldata, one from the SafeL2 event, and one
	// from the service, which a relayer executed.
	call := func(data string, nonce int64) *safenet.SafeTransaction {
		tx := transaction(data, nonce)
		tx.Operation = safenet.OperationCall
		return tx
	}
	first, second, third := call("calldata", 0), call("event", 1), call("service", 2)
	receipt := func(hash ethrpc.Hash, logs ...ethrpc.Log) ethrpc.Receipt {
		return ethrpc.Receipt{TransactionHash: hash, BlockNumber: logs[0].BlockNumber, Status: 1, Logs: logs}
	}
	executions := []ethrpc.Log{
		executionLog(executionSuccessEvent, first.Hash(), hash(1), true, 100, 0),
		executionLog(executionSuccessEvent, second.Hash(), hash(2), true, 110, 1),
		executionLog(executionFailureEvent, third.Hash(), hash(3), false, 120, 0),
	}
	newChain := func() *chain {
		return &chain{
			nonce: big.NewInt(3),
			txs: map[ethrpc.Hash]ethrpc.Transaction{
				hash(1): {To: &safe, Input: execCall(first)},
				hash(2): {To: &safe, Input: []byte("not a Safe call")},
				hash(3): {To: &relayer, Input: []byte("relay")},
			},
			receipts: map[ethrpc.Hash]ethrpc.Receipt{
				hash(1): receipt(hash(1), executions[0]),
				hash(2): receipt(hash(2), eventLog(second, hash(2), 110, 0), executions[1]),
				hash(3): receipt(hash(3), executions[2]),
				// A transaction that the service says executed one, whose receipt has no
				// execution of the Safe.
				hash(9): receipt(hash(9), executionLog(executionSuccessEvent, third.Hash(), hash(9), true, 201, 0)),
			},
		}
	}
	listing := []listed{{first, hash(1)}, {second, hash(2)}, {third, hash(3)}, {third, hash(9)}}
	want := []Execution{
		{100, hash(1), 0, first.Hash(), true, big.NewInt(9), SourceCalldata, first},
		{110, hash(2), 1, second.Hash(), true, big.NewInt(9), SourceEvent, second},
		{120, hash(3), 0, third.Hash(), false, big.NewInt(9), SourceService, third},
	}
	check := func(t *testing.T, got Found, complete bool) {
		t.Helper()
		if got.Complete != complete {
			t.Errorf("Find: got complete %t, want %t", got.Complete, complete)
		}
		// The transactions are compared as JSON, since the service's numbers are parsed
		// into big.Ints with other internals.
		if g, w := jsonString(got.Executions), jsonString(want); g != w {
			t.Errorf("Find:\ngot  %s\nwant %s", g, w)
		}
	}

	t.Run("complete", func(t *testing.T) {
		// The receipt of hash(9) is in a later block than the last one, 200. The
		// executions are complete, so no logs are scanned, which would fail.
		c := newChain()
		c.noLogs = true
		f := &Finder{Eth: c.serve(t), Service: listingService(t, nil, listing)}
		var warnings []error
		f.Warn = func(err error) { warnings = append(warnings, err) }
		got, err := f.Find(t.Context(), safe, 200, 200)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		check(t, got, true)
		if len(warnings) != 0 {
			t.Errorf("warnings: got %v, want none", warnings)
		}
	})

	t.Run("missing from the service", func(t *testing.T) {
		// The service doesn't list the second execution, so the logs are scanned, and
		// have it.
		c := newChain()
		c.logs = executions[1:2]
		f := &Finder{Eth: c.serve(t), Service: listingService(t, nil, []listed{listing[0], listing[2]})}
		got, err := f.Find(t.Context(), safe, 105, 200)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		// The event of the second execution is only in its receipt, which isn't read,
		// and its calldata isn't a Safe call, so it is unknown.
		want := slices.Clone(want)
		want[1].Source, want[1].Transaction = "", nil
		if g, w := jsonString(got.Executions), jsonString(want); got.Complete || g != w {
			t.Errorf("Find: got complete %t and\n%s\nwant incomplete and\n%s", got.Complete, g, w)
		}
	})

	t.Run("delegate call", func(t *testing.T) {
		// A delegate call could have changed the nonce, so the executions aren't known
		// to be complete, and the logs are scanned.
		c := newChain()
		c.noLogs = true
		delegate := call("calldata", 0)
		delegate.Operation = safenet.OperationDelegateCall
		c.txs[hash(1)] = ethrpc.Transaction{To: &safe, Input: execCall(delegate)}
		l := executionLog(executionSuccessEvent, delegate.Hash(), hash(1), true, 100, 0)
		c.receipts[hash(1)] = receipt(hash(1), l)
		f := &Finder{Eth: c.serve(t), Service: listingService(t, nil, []listed{{delegate, hash(1)}, listing[1], listing[2]})}
		if _, err := f.Find(t.Context(), safe, 105, 200); err == nil || !strings.Contains(err.Error(), "listing Safe transactions") {
			t.Errorf("Find: got error %v, want one from scanning the logs", err)
		}
	})
}

func jsonString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}
