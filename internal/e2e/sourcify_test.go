package e2e_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// signaturesOutput is the JSON output of `arbot signatures`.
type signaturesOutput struct {
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	Address   ethrpc.Address `json:"address"`
	CodeHash  ethrpc.Hash    `json:"codeHash"`
	CodeSize  int            `json:"codeSize"`
	Status    string         `json:"status"`
	Source    *struct {
		Name         string     `json:"name"`
		URL          string     `json:"url"`
		ObservedAt   time.Time  `json:"observedAt"`
		Match        string     `json:"match"`
		RuntimeMatch string     `json:"runtimeMatch"`
		VerifiedAt   *time.Time `json:"verifiedAt"`
		Contract     string     `json:"contract"`
		Compiler     string     `json:"compiler"`
	} `json:"source"`
	Functions []struct {
		Selector        ethrpc.Bytes `json:"selector"`
		Signature       string       `json:"signature"`
		StateMutability string       `json:"stateMutability"`
	} `json:"functions"`
	Fallback *struct {
		StateMutability string `json:"stateMutability"`
	} `json:"fallback"`
	Receive *struct {
		StateMutability string `json:"stateMutability"`
	} `json:"receive"`
}

// singletonSources are source files that the Sourcify server has for the Safe
// 1.3.0 singleton, one of which hides what it says with a bidirectional text
// control and clears the terminal.
var singletonSources = map[string]string{
	"contracts/GnosisSafe.sol":    "contract GnosisSafe {}\n",
	"contracts/base/Executor.sol": "/* \u202e */ \x1b[2J contract Executor {}",
}

// contractSourceOutput is the JSON output of `arbot contract-source`.
type contractSourceOutput struct {
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	Address   ethrpc.Address `json:"address"`
	Status    string         `json:"status"`
	Source    *struct {
		URL      string `json:"url"`
		Match    string `json:"match"`
		Contract string `json:"contract"`
	} `json:"source"`
	Files []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"files"`
}

// TestSourcify runs `arbot signatures` and `arbot contract-source` on the Safe
// 1.3.0 singleton of the Mainnet node, which Sourcify verified, on the SafeL2
// singleton, which it didn't, on an account without code, and on a contract
// whose code changed after the proposal, which Sourcify verified.
func TestSourcify(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	if r.Transaction.ChainID.Uint64() != ethrpc.Mainnet {
		t.Fatalf("Safe transaction is for chain %s, want Ethereum Mainnet", r.Transaction.ChainID)
	}
	mainnet, a := n.Mainnet, n.Artifacts
	// A contract whose code changes after the proposal, from REVERT(0, 0) to STOP.
	changed := ethrpc.Address{0: 0xc4, 19: 1}
	mainnet.RPC(nil, "anvil_setCode", changed, "0x60006000fd")
	// Part of the singleton's ABI, which is enough to check the output.
	n.ServeSourcify(a.SafeSingleton, `[
		{"type": "constructor", "inputs": [], "stateMutability": "nonpayable"},
		{"type": "function", "name": "nonce", "inputs": [], "outputs": [{"type": "uint256"}], "stateMutability": "view"},
		{"type": "function", "name": "changeThreshold", "inputs": [{"name": "_threshold", "type": "uint256"}], "outputs": [], "stateMutability": "nonpayable"},
		{"type": "event", "name": "ChangedThreshold", "inputs": [{"name": "threshold", "type": "uint256", "indexed": false}]},
		{"type": "fallback", "stateMutability": "nonpayable"},
		{"type": "receive", "stateMutability": "payable"}
	]`, singletonSources)
	n.ServeSourcify(changed, `[]`, nil)
	mainnet.MineUntil(n.Header(r.Block).Time())
	mainnet.RPC(nil, "anvil_setCode", changed, "0x00")
	mainnet.MineUntil(n.Header(r.Block).Time().Add(time.Minute))

	arbot := n.Arbot()
	info := arbot.Run(t, "info", "-json", r.ID.String())
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, info).Proposal.SafeBlock

	got := decode[signaturesOutput](t, arbot.Run(t, "signatures", "-json", "-request-file", path, a.SafeSingleton.String()))
	if got.ChainID != ethrpc.Mainnet || got.SafeBlock != safeBlock || got.Address != a.SafeSingleton ||
		got.CodeSize != len(a.Accounts[a.SafeSingleton].Code) || got.Status != "verified" {
		t.Errorf("signatures: got %+v", got)
	}
	if s := got.Source; s == nil || s.Name != "sourcify" || s.ObservedAt.IsZero() || s.Match != "exact_match" ||
		s.RuntimeMatch != "exact_match" || s.VerifiedAt == nil || s.Contract != "contracts/Test.sol:Test" || s.Compiler != "0.7.6+commit.7338295f" {
		t.Errorf("signatures: got source %+v", s)
	}
	// The functions are in the order of their selectors.
	if len(got.Functions) != 2 ||
		got.Functions[0].Selector.String() != "0x694e80c3" || got.Functions[0].Signature != "changeThreshold(uint256)" || got.Functions[0].StateMutability != "nonpayable" ||
		got.Functions[1].Selector.String() != "0xaffed0e0" || got.Functions[1].Signature != "nonce()" || got.Functions[1].StateMutability != "view" {
		t.Errorf("signatures: got functions %+v", got.Functions)
	}
	if got.Fallback == nil || got.Fallback.StateMutability != "nonpayable" || got.Receive == nil || got.Receive.StateMutability != "payable" {
		t.Errorf("signatures: got fallback %+v and receive %+v", got.Fallback, got.Receive)
	}

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "signatures", "-request-file", path, a.SafeSingleton.String())
		for _, line := range []string{
			`Contract ` + a.SafeSingleton.String() + ` on chain 1, as of block \d+`,
			`  Status    verified`,
			`  Contract  "contracts/Test.sol:Test", compiled with "0\.7\.6\+commit\.7338295f"`,
			`  Match     exact_match, runtime exact_match, verified 2025-06-25T08:00:07Z`,
			`  Fallback  nonpayable`,
			`  Receive   payable`,
			`0x694e80c3  changeThreshold\(uint256\)  nonpayable`,
			`0xaffed0e0  nonce\(\)                   view`,
		} {
			if !regexp.MustCompile(`(?m)^` + line + `$`).Match(text) {
				t.Errorf("signatures: output has no line matching %q:\n%s", line, text)
			}
		}
	})

	t.Run("unverified", func(t *testing.T) {
		got := decode[signaturesOutput](t, arbot.Run(t, "signatures", "-json", "-request-file", path, a.SafeL2Singleton.String()))
		if got.Status != "unverified" || got.Functions != nil || got.Source == nil || got.Source.URL == "" || got.Source.VerifiedAt != nil {
			t.Errorf("signatures: got %+v with source %+v", got, got.Source)
		}
	})

	t.Run("no code", func(t *testing.T) {
		got := decode[signaturesOutput](t, arbot.Run(t, "signatures", "-json", "-request-file", path, ethrpc.Address{0: 0xe0, 19: 1}.String()))
		if got.Status != "no-code" || got.CodeSize != 0 || got.Functions == nil || len(got.Functions) != 0 || got.Source != nil {
			t.Errorf("signatures: got %+v", got)
		}
	})

	t.Run("code changed", func(t *testing.T) {
		stderr := arbot.Fail(t, 1, "signatures", "-request-file", path, changed.String())
		if !regexp.MustCompile(`Sourcify verified`).Match(stderr) {
			t.Errorf("signatures: got error %s, want one about the code that Sourcify verified", stderr)
		}
	})

	t.Run("invalid arguments", func(t *testing.T) {
		for _, command := range []string{"signatures", "contract-source"} {
			arbot.Fail(t, 2, command, a.SafeSingleton.String())
			arbot.Fail(t, 2, command, "-request-file", path)
			arbot.Fail(t, 2, command, "-request-file", path, "0x1234")
		}
	})

	t.Run("contract source", func(t *testing.T) {
		got := decode[contractSourceOutput](t, arbot.Run(t, "contract-source", "-json", "-request-file", path, a.SafeSingleton.String()))
		if got.ChainID != ethrpc.Mainnet || got.SafeBlock != safeBlock || got.Address != a.SafeSingleton || got.Status != "verified" ||
			got.Source == nil || got.Source.Match != "exact_match" || got.Source.Contract != "contracts/Test.sol:Test" {
			t.Errorf("contract-source: got %+v with source %+v", got, got.Source)
		}
		// The files are in the order of their paths, as they are.
		if len(got.Files) != 2 ||
			got.Files[0].Path != "contracts/GnosisSafe.sol" || got.Files[0].Content != singletonSources["contracts/GnosisSafe.sol"] ||
			got.Files[1].Path != "contracts/base/Executor.sol" || got.Files[1].Content != singletonSources["contracts/base/Executor.sol"] {
			t.Errorf("contract-source: got files %+v", got.Files)
		}

		one := decode[contractSourceOutput](t, arbot.Run(t, "contract-source", "-json", "-file", "contracts/GnosisSafe.sol", "-request-file", path, a.SafeSingleton.String()))
		if len(one.Files) != 1 || one.Files[0].Path != "contracts/GnosisSafe.sol" {
			t.Errorf("contract-source -file: got files %+v", one.Files)
		}
		arbot.Fail(t, 1, "contract-source", "-file", "contracts/Missing.sol", "-request-file", path, a.SafeSingleton.String())

		// The text output escapes the characters that a terminal doesn't print.
		text := string(arbot.Run(t, "contract-source", "-request-file", path, a.SafeSingleton.String()))
		want := "\n==> contracts/GnosisSafe.sol <==\ncontract GnosisSafe {}\n" +
			"\n==> contracts/base/Executor.sol <==\n/* \\u202e */ \\x1b[2J contract Executor {}\n"
		if !strings.HasSuffix(text, want) {
			t.Errorf("contract-source: got output\n%s\nwant it to end with\n%s", text, want)
		}

		unverified := decode[contractSourceOutput](t, arbot.Run(t, "contract-source", "-json", "-request-file", path, a.SafeL2Singleton.String()))
		if unverified.Status != "unverified" || unverified.Files != nil {
			t.Errorf("contract-source: got %+v for an unverified contract", unverified)
		}
		noCode := decode[contractSourceOutput](t, arbot.Run(t, "contract-source", "-json", "-request-file", path, ethrpc.Address{0: 0xe0, 19: 1}.String()))
		if noCode.Status != "no-code" || noCode.Files == nil || len(noCode.Files) != 0 {
			t.Errorf("contract-source: got %+v for an account without code", noCode)
		}
		arbot.Fail(t, 1, "contract-source", "-request-file", path, changed.String())
	})
}
