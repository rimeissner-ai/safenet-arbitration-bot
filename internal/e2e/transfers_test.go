package e2e_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
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
	Source    string          `json:"source"`
	Direction string          `json:"direction"`
	From      *ethrpc.Address `json:"from"`
	To        *ethrpc.Address `json:"to"`
	Transfers []transferJSON  `json:"transfers"`
}

type transferJSON struct {
	Type     string          `json:"type"`
	Block    uint64          `json:"block"`
	TxHash   ethrpc.Hash     `json:"txHash"`
	LogIndex *uint64         `json:"logIndex"`
	Token    *ethrpc.Address `json:"token"`
	From     ethrpc.Address  `json:"from"`
	To       ethrpc.Address  `json:"to"`
	Value    *big.Int        `json:"value"`
	Check    string          `json:"check"`
}

// TestTransfers runs `arbot transfers` against the fee token and the native
// currency on the Safe's chain, with transfers out of the Safe and into it,
// before and after a proposal, and between other accounts.
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

	// Transfers into the Safe before the proposal: the fee token from alice, and
	// ether from bob, which the Safe logs as received.
	tokenIn := n.TransferFeeToken(n.Mainnet, alice, safe, ether(6))
	n.Mainnet.SetBalance(bob, ether(8))
	etherIn := n.Mainnet.Receipt(n.Mainnet.Send(bob, safe, nil, ether(7)))

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
	transfer := func(receipt *e2e.Receipt, kind string, token *ethrpc.Address, from, to ethrpc.Address, value *big.Int) transferJSON {
		index := uint64(receipt.Logs[len(receipt.Logs)-1].LogIndex)
		return transferJSON{
			Type: kind, Block: uint64(receipt.BlockNumber), TxHash: receipt.TransactionHash,
			LogIndex: &index, Token: token, From: from, To: to, Value: value,
		}
	}
	all := []transferJSON{
		transfer(first, "erc20", &token, safe, alice, ether(1)),
		transfer(second, "erc20", &token, safe, bob, ether(2)),
		transfer(third, "erc20", &token, safe, alice, ether(3)),
	}
	in := []transferJSON{
		transfer(tokenIn, "erc20", &token, alice, safe, ether(6)),
		transfer(etherIn, "native", nil, bob, safe, ether(7)),
	}
	if safeBlock < in[1].Block {
		t.Fatalf("Safe block %d is before the last transfer, in block %d", safeBlock, in[1].Block)
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
		{"into the Safe from a token sender", []string{"-in", "-from", alice.String()}, 0, "in", &alice, nil, in[:1]},
		{"into the Safe from an ether sender", []string{"-in", "-from", bob.String()}, 0, "in", &bob, nil, in[1:]},
		{"into the Safe from an account without transfers", []string{"-in", "-from", someone.String()}, 0, "in", &someone, nil, []transferJSON{}},
		{"into the Safe in a bounded range", []string{"-in", "-blocks", fmt.Sprint(safeBlock - in[1].Block + 1)}, in[1].Block, "in", nil, nil, in[1:]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"transfers", "-json", "-request-file", path}, test.args...)
			got := decode[transfersOutput](t, arbot.Run(t, args...))
			want := transfersOutput{
				Safe: safe, ChainID: ethrpc.Mainnet, FromBlock: test.fromBlock, ToBlock: safeBlock, Source: "logs", Direction: test.direction,
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
		header = fmt.Sprintf(`(?m)^Transfers to %s on chain 1, in blocks 0 to %d$`, safe, safeBlock)
		tokenRow := fmt.Sprintf(`(?m)^%d +%s +erc20 +%s +%s +%s$`, in[0].Block, in[0].TxHash, token, alice, ether(6))
		etherRow := fmt.Sprintf(`(?m)^%d +%s +native +- +%s +%s$`, in[1].Block, in[1].TxHash, bob, ether(7))
		for _, want := range []string{header, tokenRow, etherRow} {
			if !regexp.MustCompile(want).Match(text) {
				t.Errorf("transfers -in: output has no line matching %q:\n%s", want, text)
			}
		}
	})
}

// TestTransfersFromService runs `arbot transfers -service` against a Safe
// Transaction Service that lists the Safe's transfers, before and after a
// proposal, each of which arbot checks against the chain.
func TestTransfersFromService(t *testing.T) {
	t.Parallel()
	n := e2e.NewTestnet(t)
	r := n.Propose(nil)
	safe, token := r.Transaction.Safe, n.Artifacts.FeeToken
	alice, bob := ethrpc.Address{0: 0xa1, 19: 1}, ethrpc.Address{0: 0xb0, 19: 2}
	ether := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

	n.InstallFeeToken(n.Mainnet)
	out := n.TransferFeeToken(n.Mainnet, safe, alice, ether(1))
	tokenIn := n.TransferFeeToken(n.Mainnet, bob, safe, ether(2))
	n.Mainnet.SetBalance(bob, ether(8))
	etherIn := n.Mainnet.Receipt(n.Mainnet.Send(bob, safe, nil, ether(3)))
	n.Mainnet.MineUntil(n.Header(r.Block).Time())
	late := n.TransferFeeToken(n.Mainnet, safe, alice, ether(4))

	// The service lists the transfers newest first, with the log of a token
	// transfer, and the trace of a native one, in their IDs.
	id := func(receipt *e2e.Receipt, log bool) string {
		hash := strings.TrimPrefix(receipt.TransactionHash.String(), "0x")
		if !log {
			return "i" + hash + "0"
		}
		return fmt.Sprintf("e%s%d", hash, receipt.Logs[len(receipt.Logs)-1].LogIndex)
	}
	listed := func(receipt *e2e.Receipt, kind string, token *ethrpc.Address, from, to ethrpc.Address, value *big.Int) map[string]any {
		return map[string]any{
			"type": kind, "blockNumber": uint64(receipt.BlockNumber), "transactionHash": receipt.TransactionHash,
			"transferId": id(receipt, kind != "ETHER_TRANSFER"), "tokenAddress": token, "tokenId": nil,
			"from": from, "to": to, "value": value.String(), "executionDate": "2026-11-09T16:51:34Z",
		}
	}
	transfers := []map[string]any{
		listed(late, "ERC20_TRANSFER", &token, safe, alice, ether(4)),
		listed(etherIn, "ETHER_TRANSFER", nil, bob, safe, ether(3)),
		listed(tokenIn, "ERC20_TRANSFER", &token, bob, safe, ether(2)),
		listed(out, "ERC20_TRANSFER", &token, safe, alice, ether(1)),
	}
	n.ServeTransfers(safe, transfers)

	arbot := n.Arbot()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, arbot.Run(t, "info", "-json", r.ID.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	safeBlock := decode[requestInfo](t, arbot.Run(t, "info", "-json", r.ID.String())).Proposal.SafeBlock
	if safeBlock >= uint64(late.BlockNumber) {
		t.Fatalf("Safe block %d isn't before the late transfer, in block %d", safeBlock, late.BlockNumber)
	}

	transfer := func(receipt *e2e.Receipt, kind string, token *ethrpc.Address, from, to ethrpc.Address, value *big.Int) transferJSON {
		index := uint64(receipt.Logs[len(receipt.Logs)-1].LogIndex)
		return transferJSON{
			Type: kind, Block: uint64(receipt.BlockNumber), TxHash: receipt.TransactionHash,
			LogIndex: &index, Token: token, From: from, To: to, Value: value, Check: "log",
		}
	}
	tests := []struct {
		name      string
		args      []string
		direction string
		from, to  *ethrpc.Address
		want      []transferJSON
	}{
		{"out of the Safe", nil, "out", nil, nil, []transferJSON{transfer(out, "erc20", &token, safe, alice, ether(1))}},
		{"recipient without transfers", []string{"-to", bob.String()}, "out", nil, &bob, []transferJSON{}},
		{"into the Safe", []string{"-in"}, "in", nil, nil, []transferJSON{
			transfer(tokenIn, "erc20", &token, bob, safe, ether(2)),
			// The Safe's SafeReceived event confirms the native transfer.
			transfer(etherIn, "native", nil, bob, safe, ether(3)),
		}},
		{"into the Safe from a sender without transfers", []string{"-in", "-from", alice.String()}, "in", &alice, nil, []transferJSON{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"transfers", "-json", "-service", "-request-file", path}, test.args...)
			got := decode[transfersOutput](t, arbot.Run(t, args...))
			want := transfersOutput{
				Safe: safe, ChainID: ethrpc.Mainnet, FromBlock: 0, ToBlock: safeBlock, Source: "service", Direction: test.direction,
				From: test.from, To: test.to, Transfers: test.want,
			}
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				t.Errorf("transfers -service %v:\ngot  %s\nwant %s", test.args, g, w)
			}
		})
	}

	t.Run("text", func(t *testing.T) {
		text := arbot.Run(t, "transfers", "-service", "-request-file", path)
		header := fmt.Sprintf(`(?m)^Transfers from %s on chain 1, in blocks 0 to %d$`, safe, safeBlock)
		source := `(?m)^Source: Safe Transaction Service http://\S+, observed at \S+, checked against the chain$`
		row := fmt.Sprintf(`(?m)^%d +%s +erc20 +%s +%s +%s +%s +log$`, out.BlockNumber, out.TransactionHash, token, safe, alice, ether(1))
		for _, want := range []string{header, source, row} {
			if !regexp.MustCompile(want).Match(text) {
				t.Errorf("transfers -service: output has no line matching %q:\n%s", want, text)
			}
		}
	})

	t.Run("unconfirmed", func(t *testing.T) {
		// A transfer that the chain doesn't confirm, with a value that its log doesn't
		// have, fails the command rather than be listed.
		forged := listed(out, "ERC20_TRANSFER", &token, safe, bob, ether(1))
		n.ServeTransfers(safe, []map[string]any{forged})
		defer n.ServeTransfers(safe, transfers)
		stderr := arbot.Fail(t, 1, "transfers", "-service", "-request-file", path)
		if !strings.Contains(string(stderr), "doesn't confirm") {
			t.Errorf("transfers -service: got error %q, want one that the chain doesn't confirm the transfer", stderr)
		}
	})

	t.Run("blocks", func(t *testing.T) {
		arbot.Fail(t, 2, "transfers", "-service", "-blocks", "10", "-request-file", path)
	})
}
