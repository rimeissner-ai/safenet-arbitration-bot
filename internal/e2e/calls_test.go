package e2e_test

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// callsOutput is the JSON output of `arbot calls`.
type callsOutput struct {
	ChainID   uint64 `json:"chainId"`
	SafeBlock uint64 `json:"safeBlock"`
	Safe      struct {
		Address          ethrpc.Address `json:"address"`
		CodeHash         ethrpc.Hash    `json:"codeHash"`
		CodeSize         int            `json:"codeSize"`
		ProxyVersion     *string        `json:"proxyVersion"`
		Singleton        ethrpc.Address `json:"singleton"`
		SingletonVersion *string        `json:"singletonVersion"`
	} `json:"safe"`
	Target accountJSON `json:"target"`
	Calls  []callJSON  `json:"calls"`
}

type accountJSON struct {
	Address  ethrpc.Address `json:"address"`
	CodeHash ethrpc.Hash    `json:"codeHash"`
	CodeSize int            `json:"codeSize"`
	Self     bool           `json:"self"`
	Contract *struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"contract"`
}

type callJSON struct {
	Kind        string       `json:"kind"`
	Placeholder string       `json:"placeholder"`
	To          accountJSON  `json:"to"`
	Value       *big.Int     `json:"value"`
	Operation   string       `json:"operation"`
	Data        ethrpc.Bytes `json:"data"`
}

// TestCalls runs `arbot calls` on a proposal of a delegate call to the Safe
// 1.3.0 on the Mainnet node, which pays a refund in a token.
func TestCalls(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	tx := r.Transaction
	if tx.ChainID.Uint64() != ethrpc.Mainnet || tx.Operation != 1 || tx.To != tx.Safe || tx.GasPrice.Sign() == 0 {
		t.Fatalf("Safe transaction %+v isn't a delegate call to the Safe on Ethereum Mainnet with a refund", tx)
	}
	n.Mainnet.MineUntil(n.Header(r.Block).Time())

	arbot := n.Arbot()
	info := arbot.Run(t, "info", "-json", r.ID.String())
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, info).Proposal.SafeBlock

	got := decode[callsOutput](t, arbot.Run(t, "calls", "-json", "-request-file", path))
	if got.ChainID != ethrpc.Mainnet || got.SafeBlock != safeBlock {
		t.Errorf("calls: got chain %d and block %d, want %d and %d", got.ChainID, got.SafeBlock, ethrpc.Mainnet, safeBlock)
	}
	safe := got.Safe
	if safe.Address != tx.Safe || safe.CodeSize == 0 || safe.ProxyVersion == nil || *safe.ProxyVersion != "1.3.0" ||
		safe.Singleton != singleton130 || safe.SingletonVersion == nil || *safe.SingletonVersion != "1.3.0" {
		s, _ := json.Marshal(safe)
		t.Errorf("calls: got Safe %s, want a Safe 1.3.0 proxy of the Safe 1.3.0 singleton %s", s, singleton130)
	}
	self := accountJSON{Address: tx.Safe, CodeHash: safe.CodeHash, CodeSize: safe.CodeSize, Self: true}
	if !reflect.DeepEqual(got.Target, self) {
		t.Errorf("calls: got target %+v, want the Safe itself %+v", got.Target, self)
	}

	refund := new(big.Int).Add(tx.SafeTxGas, tx.BaseGas)
	refund.Mul(refund, tx.GasPrice)
	// The gas token has no code, so its account has the hash of empty code.
	emptyCodeHash := ethrpc.MustParseHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
	want := []callJSON{
		{Kind: "root", To: self, Value: tx.Value, Operation: "DELEGATECALL", Data: tx.Data},
		{
			Kind:      "refund",
			To:        accountJSON{Address: tx.GasToken, CodeHash: emptyCodeHash},
			Value:     new(big.Int),
			Operation: "CALL",
			Data:      solabi.Call(solabi.Selector("transfer(address,uint256)"), tx.RefundReceiver, refund),
		},
	}
	if !reflect.DeepEqual(got.Calls, want) {
		g, _ := json.Marshal(got.Calls)
		w, _ := json.Marshal(want)
		t.Errorf("calls:\ngot  %s\nwant %s", g, w)
	}

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "calls", "-request-file", path)
		for _, line := range []string{
			`Safe ` + tx.Safe.String() + ` on chain 1, as of block \d+`,
			`  Code       0x[0-9a-f]{64} \(\d+ bytes\), Safe proxy 1\.3\.0`,
			`  Singleton  ` + singleton130.String() + `, Safe singleton 1\.3\.0`,
			`Target ` + tx.Safe.String() + `, the Safe itself`,
			`1 +root +` + tx.Safe.String() + ` +the Safe itself +DELEGATECALL +1 +- +0x[0-9a-f]+\.\.\. \(\d+ bytes\)`,
			`2 +refund +` + tx.GasToken.String() + ` +no code +CALL +0 +transfer +0xa9059cbb[0-9a-f]+\.\.\. \(68 bytes\)`,
		} {
			if !regexp.MustCompile(`(?m)^` + line + `$`).Match(text) {
				t.Errorf("calls: output has no line matching %q:\n%s", line, text)
			}
		}
	})

	t.Run("missing request file", func(t *testing.T) {
		arbot.Fail(t, 2, "calls")
	})
}

// singleton130 is the Safe 1.3.0 singleton that the Safe of the test
// transactions delegates to.
var singleton130 = ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552")
