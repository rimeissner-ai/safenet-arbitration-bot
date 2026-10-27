package e2e_test

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/functions"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// executionsOutput is the JSON output of `arbot executions`.
type executionsOutput struct {
	Safe       ethrpc.Address  `json:"safe"`
	ChainID    uint64          `json:"chainId"`
	FromBlock  uint64          `json:"fromBlock"`
	ToBlock    uint64          `json:"toBlock"`
	Executions []executionJSON `json:"executions"`
}

type executionJSON struct {
	Block       uint64                   `json:"block"`
	TxHash      ethrpc.Hash              `json:"txHash"`
	SafeTxHash  ethrpc.Hash              `json:"safeTxHash"`
	Success     bool                     `json:"success"`
	Payment     *big.Int                 `json:"payment"`
	Source      string                   `json:"source"`
	Transaction *safenet.SafeTransaction `json:"transaction"`
}

// TestExecutions runs `arbot executions` against real Safe 1.3.0 contracts on
// anvil, with executions that it decodes from each source that anvil can serve:
// the calldata, a trace, and the SafeL2 event.
func TestExecutions(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	if r.Transaction.ChainID.Uint64() != ethrpc.Mainnet {
		t.Fatalf("Safe transaction is for chain %s, want Ethereum Mainnet", r.Transaction.ChainID)
	}
	mainnet, a := n.Mainnet, n.Artifacts
	safe := r.Transaction.Safe
	// The Mainnet node only keeps the states of its last 128 blocks, which a trace
	// needs, so its blocks are mined up to just before the proposal first, and the
	// transactions that follow are in the last of them.
	mainnet.MineUntil(n.Header(r.Block).Time().Add(-3 * time.Minute))
	// A second Safe, a SafeL2 that logs the transactions that it executes.
	safeL2 := ethrpc.Address{0: 0x5a, 19: 0xf2}
	owner := ethrpc.Address{0: 0x0e, 19: 1}
	forwarder := ethrpc.Address{0: 0xf0, 19: 1}
	target := ethrpc.Address{0: 0xa1, 19: 1}
	// A contract whose calls revert, with the code REVERT(0, 0).
	reverter := ethrpc.Address{0: 0xde, 19: 1}
	mainnet.RPC(nil, "anvil_setCode", reverter, "0x60006000fd")
	mainnet.SetBalance(owner, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))

	n.SetupSafe(mainnet, safe, a.SafeSingleton, owner, forwarder)
	n.SetupSafe(mainnet, safeL2, a.SafeL2Singleton, owner)
	n.InstallForwarder(mainnet, forwarder, safe)

	// exec has the owner execute a Safe transaction of the Safe, directly or
	// through the forwarder, and returns what arbot should report about it, given
	// the source that it should have decoded it from.
	exec := func(safe ethrpc.Address, tx safenet.SafeTransaction, via ethrpc.Address, source string, success bool) executionJSON {
		t.Helper()
		tx.ChainID, tx.Safe = big.NewInt(ethrpc.Mainnet), safe
		tx.Nonce = n.SafeNonce(mainnet, safe)
		sender := owner
		if via != (ethrpc.Address{}) {
			sender = forwarder
		}
		to := safe
		if via != (ethrpc.Address{}) {
			to = via
		}
		receipt := mainnet.Transact(owner, to, n.ExecCalldata(&tx, sender))
		return executionJSON{
			Block: uint64(receipt.BlockNumber), TxHash: receipt.TransactionHash, SafeTxHash: tx.Hash(),
			Success: success, Payment: new(big.Int), Source: source, Transaction: &tx,
		}
	}
	zero := func(tx safenet.SafeTransaction) safenet.SafeTransaction {
		tx.Value, tx.SafeTxGas, tx.BaseGas, tx.GasPrice = new(big.Int), new(big.Int), new(big.Int), new(big.Int)
		return tx
	}
	call := zero(safenet.SafeTransaction{To: target, Data: []byte{0x12, 0x34}})
	viaCalldata := exec(safe, call, ethrpc.Address{}, "calldata", true)

	relayed := zero(safenet.SafeTransaction{To: target, Data: []byte("relayed"), Operation: safenet.OperationCall})
	relayed.Value = big.NewInt(0)
	viaTrace := exec(safe, relayed, forwarder, "trace", true)

	// A call that reverts fails, and the Safe transaction still uses its nonce. It
	// needs a gas limit for the call, or the whole execution reverts.
	failing := zero(safenet.SafeTransaction{To: reverter})
	failing.SafeTxGas = big.NewInt(100_000)
	failed := exec(safe, failing, ethrpc.Address{}, "calldata", false)

	// A call whose data is a Safe's execTransaction of an ERC-20 transfer to the
	// owner, which -calls decodes. The target has no code, so the call succeeds.
	transfer := solabi.Call(solabi.Selector("transfer(address,uint256)"), owner, big.NewInt(5))
	nestedData := solabi.Call(solabi.Selector("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)"),
		target, new(big.Int), transfer, uint64(0), new(big.Int), new(big.Int), new(big.Int), ethrpc.Address{}, ethrpc.Address{}, []byte{})
	nested := exec(safe, zero(safenet.SafeTransaction{To: target, Data: nestedData}), ethrpc.Address{}, "calldata", true)

	viaEvent := exec(safeL2, call, ethrpc.Address{}, "event", true)

	// An execution after the proposal, which the evidence must not include.
	mainnet.MineUntil(n.Header(r.Block).Time())
	exec(safe, zero(safenet.SafeTransaction{To: target, Data: []byte("later")}), ethrpc.Address{}, "calldata", true)

	arbot := n.Arbot()
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	info := arbot.Run(t, "info", "-json", r.ID.String())
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	// The request of the SafeL2 is the same one with its Safe changed.
	var request map[string]any
	if err := json.Unmarshal(info, &request); err != nil {
		t.Fatal(err)
	}
	request["proposal"].(map[string]any)["transaction"].(map[string]any)["safe"] = safeL2.String()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	pathL2 := filepath.Join(dir, "request-l2.json")
	if err := os.WriteFile(pathL2, data, 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, info).Proposal.SafeBlock

	tests := []struct {
		name string
		path string
		args []string
		safe ethrpc.Address
		from uint64
		want []executionJSON
	}{
		{"calldata, trace, and failure", path, nil, safe, 0, []executionJSON{viaCalldata, viaTrace, failed, nested}},
		{"SafeL2 event", pathL2, nil, safeL2, 0, []executionJSON{viaEvent}},
		{"bounded range", path, []string{"-blocks", "1"}, safe, safeBlock, []executionJSON{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"executions", "-json", "-request-file", test.path}, test.args...)
			got := decode[executionsOutput](t, arbot.Run(t, args...))
			if got.Safe != test.safe || got.ChainID != ethrpc.Mainnet || got.FromBlock != test.from || got.ToBlock != safeBlock {
				t.Errorf("executions %v: got Safe %s on chain %d, in blocks %d to %d, want %s on chain 1, in blocks %d to %d",
					test.args, got.Safe, got.ChainID, got.FromBlock, got.ToBlock, test.safe, test.from, safeBlock)
			}
			if !reflect.DeepEqual(canonical(t, got.Executions), canonical(t, test.want)) {
				g, _ := json.MarshalIndent(got.Executions, "", "  ")
				w, _ := json.MarshalIndent(test.want, "", "  ")
				t.Errorf("executions %v:\ngot  %s\nwant %s", test.args, g, w)
			}
		})
	}

	t.Run("calls", func(t *testing.T) {
		type callJSON struct {
			Kind string `json:"kind"`
			To   struct {
				Address  ethrpc.Address `json:"address"`
				CodeSize int            `json:"codeSize"`
			} `json:"to"`
			Function *functions.Function `json:"function"`
		}
		type withCalls struct {
			Executions []struct {
				SafeTxHash ethrpc.Hash `json:"safeTxHash"`
				Target     *struct {
					Address ethrpc.Address `json:"address"`
				} `json:"target"`
				Calls []callJSON `json:"calls"`
			} `json:"executions"`
		}
		got := decode[withCalls](t, arbot.Run(t, "executions", "-json", "-calls", "-request-file", path))
		want := []struct {
			execution executionJSON
			to        ethrpc.Address
			function  string
		}{
			{viaCalldata, target, ""},
			{viaTrace, target, ""},
			{failed, reverter, ""},
			{nested, target, "execTransaction > transfer"},
		}
		if len(got.Executions) != len(want) {
			t.Fatalf("executions -calls: got %d executions, want %d", len(got.Executions), len(want))
		}
		for i, w := range want {
			x := got.Executions[i]
			if x.SafeTxHash != w.execution.SafeTxHash || x.Target == nil || x.Target.Address != w.to || len(x.Calls) != 1 {
				t.Errorf("execution %d: got %+v, want one call to %s", i, x, w.to)
				continue
			}
			c := x.Calls[0]
			function := ""
			if c.Function != nil {
				function = c.Function.String()
			}
			if c.Kind != "root" || c.To.Address != w.to || function != w.function {
				t.Errorf("execution %d: got call %+v (%q), want a root call to %s, %q", i, c, function, w.to, w.function)
			}
		}
		// The recipient of the inner transfer.
		calls := got.Executions[3].Calls
		if len(calls) != 1 || calls[0].Function == nil || len(calls[0].Function.Args) != 10 || calls[0].Function.Args[2].Call == nil {
			t.Fatalf("execution 3: got calls %+v, want one execTransaction with a decoded call", calls)
		}
		if inner := calls[0].Function.Args[2].Call; len(inner.Args) == 0 {
			t.Errorf("inner call: got %+v, want a transfer", inner)
		} else if to, _ := inner.Args[0].Value.(string); !strings.EqualFold(to, owner.String()) {
			t.Errorf("recipient of the inner transfer: got %v, want %s", inner.Args[0].Value, owner)
		}
		// Without -calls, the executions have no calls.
		if out := arbot.Run(t, "executions", "-json", "-request-file", path); strings.Contains(string(out), `"calls"`) {
			t.Errorf("executions without -calls: output has calls")
		}

		text := arbot.Run(t, "executions", "-calls", "-request-file", path)
		line := `(?m)^3 +1 +root +` + target.String() + ` +no code +CALL +0 +execTransaction > transfer$`
		if !regexp.MustCompile(line).Match(text) {
			t.Errorf("executions -calls: output has no line matching %q:\n%s", line, text)
		}
	})

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "executions", "-request-file", path)
		for _, want := range []string{
			`(?m)^Safe transactions executed by ` + safe.String() + ` on chain 1, in blocks 0 to \d+$`,
			`(?m)^\d+ +` + viaCalldata.TxHash.String() + ` +success +` + viaCalldata.SafeTxHash.String() + ` +calldata +0 +` + target.String() + ` +0 +CALL +0x1234 \(2 bytes\)$`,
			`(?m)^\d+ +` + viaTrace.TxHash.String() + ` +success +` + viaTrace.SafeTxHash.String() + ` +trace +1 +`,
			`(?m)^\d+ +` + failed.TxHash.String() + ` +failure +` + failed.SafeTxHash.String() + ` +calldata +2 +` + reverter.String() + ` +0 +CALL +-$`,
		} {
			if !regexp.MustCompile(want).Match(text) {
				t.Errorf("executions: output has no line matching %q:\n%s", want, text)
			}
		}
		if got := arbot.Run(t, "executions", "-request-file", path, "-blocks", "1"); !regexp.MustCompile(`(?m)^none$`).Match(got) {
			t.Errorf("executions -blocks 1: got output %q, want one saying none", got)
		}
	})
}

// TestExecutionsUnknown runs `arbot executions` for a Safe transaction that no
// source can decode: the onchain transaction is through a relayer, and by the
// time that arbot runs, the node has pruned the state that a trace of it needs.
func TestExecutionsUnknown(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	mainnet, safe := n.Mainnet, r.Transaction.Safe
	owner := ethrpc.Address{0: 0x0e, 19: 1}
	forwarder := ethrpc.Address{0: 0xf0, 19: 1}
	mainnet.SetBalance(owner, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))
	n.SetupSafe(mainnet, safe, n.Artifacts.SafeSingleton, owner, forwarder)
	n.InstallForwarder(mainnet, forwarder, safe)

	tx := safenet.SafeTransaction{
		ChainID: big.NewInt(ethrpc.Mainnet), Safe: safe, To: ethrpc.Address{0: 0xa1, 19: 1}, Data: []byte("relayed"),
		Value: new(big.Int), SafeTxGas: new(big.Int), BaseGas: new(big.Int), GasPrice: new(big.Int),
		Nonce: n.SafeNonce(mainnet, safe),
	}
	receipt := mainnet.Transact(owner, forwarder, n.ExecCalldata(&tx, forwarder))

	// Arbot mines the Mainnet node up to the proposal, which is far more than the
	// 128 blocks that it keeps states for.
	arbot := n.Arbot()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, arbot.Run(t, "info", "-json", r.ID.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	want := []executionJSON{{
		Block: uint64(receipt.BlockNumber), TxHash: receipt.TransactionHash, SafeTxHash: tx.Hash(), Success: true, Payment: new(big.Int),
	}}
	var raw struct {
		Executions []map[string]any `json:"executions"`
	}
	out := arbot.Run(t, "executions", "-json", "-request-file", path)
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	// The transaction is null, and the source is left out.
	if len(raw.Executions) != 1 {
		t.Fatalf("executions: got %s, want one", out)
	}
	if got, ok := raw.Executions[0]["transaction"]; !ok || got != nil {
		t.Errorf("executions: got transaction %v, want null", got)
	}
	if _, ok := raw.Executions[0]["source"]; ok {
		t.Errorf("executions: got source %v, want none", raw.Executions[0]["source"])
	}
	if got := decode[executionsOutput](t, out).Executions; !reflect.DeepEqual(canonical(t, got), canonical(t, want)) {
		t.Errorf("executions: got %+v, want %+v", got, want)
	}

	text := arbot.Run(t, "executions", "-request-file", path)
	line := `(?m)^\d+ +` + receipt.TransactionHash.String() + ` +success +` + tx.Hash().String() + ` +- +unknown transaction$`
	if !regexp.MustCompile(line).Match(text) {
		t.Errorf("executions: output has no line matching %q:\n%s", line, text)
	}

	// With -calls, an unknown transaction has no target or calls.
	var withCalls struct {
		Executions []map[string]any `json:"executions"`
	}
	if err := json.Unmarshal(arbot.Run(t, "executions", "-json", "-calls", "-request-file", path), &withCalls); err != nil {
		t.Fatal(err)
	}
	if x := withCalls.Executions; len(x) != 1 || x[0]["target"] != nil || x[0]["calls"] != nil {
		t.Errorf("executions -calls: got %v, want one execution without a target or calls", x)
	}
}
