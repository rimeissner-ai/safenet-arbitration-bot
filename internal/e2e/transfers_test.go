package e2e_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/e2e"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// transfersOutput is the JSON output of `arbot transfers`.
type transfersOutput struct {
	Safe      ethrpc.Address  `json:"safe"`
	ChainID   uint64          `json:"chainId"`
	FromBlock uint64          `json:"fromBlock"`
	ToBlock   uint64          `json:"toBlock"`
	Direction string          `json:"direction"`
	From      *ethrpc.Address `json:"from"`
	To        *ethrpc.Address `json:"to"`
	Transfers []transferJSON  `json:"transfers"`
}

type transferJSON struct {
	Type     string         `json:"type"`
	Block    uint64         `json:"block"`
	TxHash   ethrpc.Hash    `json:"txHash"`
	LogIndex uint64         `json:"logIndex"`
	Token    ethrpc.Address `json:"token"`
	From     ethrpc.Address `json:"from"`
	To       ethrpc.Address `json:"to"`
	Value    *big.Int       `json:"value"`
}

// TestTransfers runs `arbot transfers` against the fee token on the Safe's
// chain, with transfers out of the Safe and into it, before and after a
// proposal, and between other accounts.
func TestTransfers(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	if r.Transaction.ChainID.Uint64() != ethrpc.Mainnet {
		t.Fatalf("Safe transaction is for chain %s, want Ethereum Mainnet", r.Transaction.ChainID)
	}
	safe, token := r.Transaction.Safe, n.Artifacts.FeeToken
	alice, bob := ethrpc.Address{0: 0xa1, 19: 1}, ethrpc.Address{0: 0xb0, 19: 2}
	ether := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

	// The Safe's transfers before the proposal, and one from another account. The
	// Mainnet node's blocks are earlier than the proposal until it mines up to it.
	n.InstallFeeToken(n.Mainnet)
	first := n.TransferFeeToken(n.Mainnet, safe, alice, ether(1))
	second := n.TransferFeeToken(n.Mainnet, safe, bob, ether(2))
	third := n.TransferFeeToken(n.Mainnet, safe, alice, ether(3))
	n.TransferFeeToken(n.Mainnet, n.Sponsor, alice, ether(5))

	// A transfer into the Safe before the proposal.
	tokenIn := n.TransferFeeToken(n.Mainnet, alice, safe, ether(6))

	// Transfers after the proposal, which the evidence must not include.
	n.Mainnet.MineUntil(n.Header(r.Block).Time())
	n.TransferFeeToken(n.Mainnet, safe, alice, ether(4))
	n.TransferFeeToken(n.Mainnet, alice, safe, ether(9))

	arbot := n.Arbot()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, arbot.Run(t, "info", "-json", r.ID.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, arbot.Run(t, "info", "-json", r.ID.String())).Proposal.SafeBlock
	transfer := func(receipt *e2e.Receipt, from, to ethrpc.Address, value *big.Int) transferJSON {
		return transferJSON{
			Type: "erc20", Block: uint64(receipt.BlockNumber), TxHash: receipt.TransactionHash,
			LogIndex: uint64(receipt.Logs[len(receipt.Logs)-1].LogIndex), Token: token, From: from, To: to, Value: value,
		}
	}
	all := []transferJSON{
		transfer(first, safe, alice, ether(1)),
		transfer(second, safe, bob, ether(2)),
		transfer(third, safe, alice, ether(3)),
	}
	in := []transferJSON{transfer(tokenIn, alice, safe, ether(6))}
	if safeBlock < in[0].Block {
		t.Fatalf("Safe block %d is before the last transfer, in block %d", safeBlock, in[0].Block)
	}

	someone := ethrpc.Address{19: 9}
	tests := []struct {
		name      string
		args      []string
		fromBlock uint64
		direction string
		from, to  *ethrpc.Address
		want      []transferJSON
	}{
		{"default range", nil, 0, "out", nil, nil, all},
		{"recipient", []string{"-to", bob.String()}, 0, "out", nil, &bob, all[1:2]},
		{"recipient without transfers", []string{"-to", someone.String()}, 0, "out", nil, &someone, []transferJSON{}},
		{"bounded range", []string{"-blocks", fmt.Sprint(safeBlock - all[2].Block + 1)}, all[2].Block, "out", nil, nil, all[2:]},
		{"only the Safe block", []string{"-blocks", "1"}, safeBlock, "out", nil, nil, []transferJSON{}},
		{"into the Safe", []string{"-in"}, 0, "in", nil, nil, in},
		{"into the Safe from a sender", []string{"-in", "-from", alice.String()}, 0, "in", &alice, nil, in},
		{"into the Safe from an account without transfers", []string{"-in", "-from", someone.String()}, 0, "in", &someone, nil, []transferJSON{}},
		{"into the Safe in a bounded range", []string{"-in", "-blocks", fmt.Sprint(safeBlock - in[0].Block + 1)}, in[0].Block, "in", nil, nil, in},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"transfers", "-json", "-request-file", path}, test.args...)
			got := decode[transfersOutput](t, arbot.Run(t, args...))
			want := transfersOutput{
				Safe: safe, ChainID: ethrpc.Mainnet, FromBlock: test.fromBlock, ToBlock: safeBlock, Direction: test.direction,
				From: test.from, To: test.to, Transfers: test.want,
			}
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				t.Errorf("transfers %v:\ngot  %s\nwant %s", test.args, g, w)
			}
		})
	}

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "transfers", "-request-file", path)
		header := fmt.Sprintf(`(?m)^ERC-20 transfers from %s on chain 1, in blocks 0 to %d$`, safe, safeBlock)
		row := fmt.Sprintf(`(?m)^%d +%s +%s +%s +%s$`, all[1].Block, all[1].TxHash, token, bob, ether(2))
		for _, want := range []string{header, row} {
			if !regexp.MustCompile(want).Match(text) {
				t.Errorf("transfers: output has no line matching %q:\n%s", want, text)
			}
		}
		if got := arbot.Run(t, "transfers", "-request-file", path, "-blocks", "1"); !regexp.MustCompile(`(?m)^none$`).Match(got) {
			t.Errorf("transfers -blocks 1: got output %q, want one saying none", got)
		}

		text = arbot.Run(t, "transfers", "-in", "-request-file", path)
		header = fmt.Sprintf(`(?m)^ERC-20 transfers to %s on chain 1, in blocks 0 to %d$`, safe, safeBlock)
		row = fmt.Sprintf(`(?m)^%d +%s +%s +%s +%s$`, in[0].Block, in[0].TxHash, token, alice, ether(6))
		for _, want := range []string{header, row} {
			if !regexp.MustCompile(want).Match(text) {
				t.Errorf("transfers -in: output has no line matching %q:\n%s", want, text)
			}
		}
	})
}
