package signatures

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/sourcify"
)

var (
	address = ethrpc.Address{0: 0xc0, 19: 1}
	code    = ethrpc.Bytes{0x60, 0x80, 0x60, 0x40}
)

// fakeRepository serves contract for every address, or err.
type fakeRepository struct {
	contract *sourcify.Contract
	err      error
	// fetched counts the contracts that it served.
	fetched int
}

func (r *fakeRepository) URL(chainID uint64, address ethrpc.Address, fields ...sourcify.Field) (string, error) {
	return "https://sourcify.test/" + address.String(), nil
}

func (r *fakeRepository) Contract(ctx context.Context, chainID uint64, address ethrpc.Address, fields ...sourcify.Field) (*sourcify.Contract, error) {
	r.fetched++
	return r.contract, r.err
}

// abi decodes a JSON ABI.
func abi(t *testing.T, s string) []solabi.ABIEntry {
	t.Helper()
	var entries []solabi.ABIEntry
	if err := json.Unmarshal([]byte(s), &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestLookup(t *testing.T) {
	verifiedAt := time.Date(2025, 6, 25, 8, 0, 7, 0, time.UTC)
	repo := &fakeRepository{contract: &sourcify.Contract{
		Match: "exact_match", RuntimeMatch: "match", VerifiedAt: verifiedAt,
		Name: "contracts/C.sol:C", Compiler: "0.7.6+commit.7338295f", RuntimeCode: code,
		ABI: abi(t, `[
			{"type": "constructor", "inputs": [{"type": "address"}]},
			{"type": "function", "name": "transfer", "inputs": [{"type": "address"}, {"type": "uint256"}], "stateMutability": "nonpayable"},
			{"type": "event", "name": "Transfer", "inputs": [{"type": "address"}, {"type": "address"}, {"type": "uint256"}]},
			{"type": "error", "name": "Oops", "inputs": []},
			{"type": "function", "name": "balanceOf", "inputs": [{"type": "address"}], "constant": true},
			{"type": "fallback", "payable": true},
			{"type": "receive", "stateMutability": "payable"}
		]`),
	}}
	got, err := Lookup(t.Context(), repo, ethrpc.Mainnet, address, code)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != sourcify.Verified || got.CodeSize != len(code) || got.Address != address {
		t.Errorf("Lookup: got status %q, code size %d, and address %s", got.Status, got.CodeSize, got.Address)
	}
	if s := got.Source; s == nil || s.Name != "sourcify" || s.URL != "https://sourcify.test/"+address.String() ||
		s.ObservedAt.IsZero() || s.Match != "exact_match" || s.RuntimeMatch != "match" || !s.VerifiedAt.Equal(verifiedAt) ||
		s.Contract != "contracts/C.sol:C" || s.Compiler != "0.7.6+commit.7338295f" {
		t.Errorf("Lookup: got source %+v", s)
	}
	// The functions are in the order of their selectors, without the constructor,
	// events, and errors.
	want := []Function{
		{Selector: ethrpc.Bytes{0x70, 0xa0, 0x82, 0x31}, Signature: "balanceOf(address)", StateMutability: "view"},
		{Selector: ethrpc.Bytes{0xa9, 0x05, 0x9c, 0xbb}, Signature: "transfer(address,uint256)", StateMutability: "nonpayable"},
	}
	g, _ := json.Marshal(got.Functions)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("Lookup: got functions %s, want %s", g, w)
	}
	if got.Fallback == nil || got.Fallback.StateMutability != "payable" || got.Receive == nil || got.Receive.StateMutability != "payable" {
		t.Errorf("Lookup: got fallback %+v and receive %+v, want both payable", got.Fallback, got.Receive)
	}
}

func TestLookupNoFallback(t *testing.T) {
	repo := &fakeRepository{contract: &sourcify.Contract{Match: "match", RuntimeMatch: "match", RuntimeCode: code, ABI: abi(t, `[]`)}}
	got, err := Lookup(t.Context(), repo, ethrpc.Mainnet, address, code)
	if err != nil {
		t.Fatal(err)
	}
	// A verified contract without functions has an empty list, not an unknown one.
	if got.Functions == nil || len(got.Functions) != 0 || got.Fallback != nil || got.Receive != nil {
		t.Errorf("Lookup: got functions %v, fallback %v, and receive %v; want none", got.Functions, got.Fallback, got.Receive)
	}
}

func TestLookupNoCode(t *testing.T) {
	repo := &fakeRepository{err: errors.New("unexpected fetch")}
	got, err := Lookup(t.Context(), repo, ethrpc.Mainnet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != sourcify.NoCode || got.Source != nil || got.Functions == nil || len(got.Functions) != 0 || repo.fetched != 0 {
		t.Errorf("Lookup: got %+v after %d fetches, want no code, no source, and no functions without fetching", got, repo.fetched)
	}
}

func TestLookupUnverified(t *testing.T) {
	repo := &fakeRepository{err: sourcify.ErrNotFound}
	got, err := Lookup(t.Context(), repo, ethrpc.Mainnet, address, code)
	if err != nil {
		t.Fatal(err)
	}
	// The functions of an unverified contract are unknown, and the source says
	// where and when they were looked up.
	if got.Status != sourcify.Unverified || got.Functions != nil || got.Source == nil || got.Source.URL == "" ||
		got.Source.ObservedAt.IsZero() || got.Source.Match != "" || got.Source.VerifiedAt != nil {
		t.Errorf("Lookup: got %+v with source %+v", got, got.Source)
	}
}

func TestLookupFails(t *testing.T) {
	for name, repo := range map[string]*fakeRepository{
		"fetch fails": {err: errors.New("HTTP 500")},
		// Sourcify verified other code at the address, such as before the contract was
		// destroyed and created again.
		"other code": {contract: &sourcify.Contract{Match: "match", RuntimeMatch: "match", RuntimeCode: ethrpc.Bytes{0x60}}},
		"invalid ABI": {contract: &sourcify.Contract{Match: "match", RuntimeMatch: "match", RuntimeCode: code, ABI: abi(t, `[
			{"type": "function", "name": "f\u001b[2J", "inputs": []}
		]`)}},
		// Valid Solidity has no two functions with the same selector.
		"same selector": {contract: &sourcify.Contract{Match: "match", RuntimeMatch: "match", RuntimeCode: code, ABI: abi(t, `[
			{"type": "function", "name": "f", "inputs": []},
			{"type": "function", "name": "f", "inputs": []}
		]`)}},
	} {
		if got, err := Lookup(t.Context(), repo, ethrpc.Mainnet, address, code); err == nil {
			t.Errorf("%s: Lookup = %+v, want an error", name, got)
		}
	}
}
