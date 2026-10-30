package sourcify

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var address = ethrpc.Address{0: 0xc0, 19: 1}

// serve starts a server that answers requests for address on Ethereum Mainnet
// with status and body, and returns a client for it.
func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server/v2/contract/1/"+address.String() || r.URL.Query().Get("fields") == "" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL + "/server")
}

func TestContract(t *testing.T) {
	// A response of Sourcify, with fields that the client doesn't use.
	client := serve(t, http.StatusOK, `{
		"abi": [
			{"type": "constructor", "inputs": [{"name": "x", "type": "address"}], "stateMutability": "nonpayable"},
			{"type": "function", "name": "f", "inputs": [{"name": "a", "type": "uint256", "internalType": "uint256"}], "outputs": [], "stateMutability": "view"},
			{"type": "fallback", "stateMutability": "payable"}
		],
		"compilation": {"fullyQualifiedName": "contracts/C.sol:C", "compilerVersion": "0.7.6+commit.7338295f", "name": "C"},
		"runtimeBytecode": {"onchainBytecode": "0x6080"},
		"sources": {"contracts/C.sol": {"content": "contract C {}"}, "contracts/B.sol": {"content": "library B {}"}},
		"matchId": "1",
		"creationMatch": "exact_match",
		"runtimeMatch": "match",
		"verifiedAt": "2025-06-25T08:00:07Z",
		"match": "exact_match",
		"chainId": "1",
		"address": "0xC000000000000000000000000000000000000001"
	}`)
	got, err := client.Contract(t.Context(), ethrpc.Mainnet, address, ABI, Sources)
	if err != nil {
		t.Fatal(err)
	}
	want := &Contract{
		Match:        "exact_match",
		RuntimeMatch: "match",
		VerifiedAt:   time.Date(2025, 6, 25, 8, 0, 7, 0, time.UTC),
		Name:         "contracts/C.sol:C",
		Compiler:     "0.7.6+commit.7338295f",
		ABI: []solabi.ABIEntry{
			{Type: "constructor", Inputs: []solabi.ABIParam{{Type: "address"}}, StateMutability: "nonpayable"},
			{Type: "function", Name: "f", Inputs: []solabi.ABIParam{{Type: "uint256"}}, StateMutability: "view"},
			{Type: "fallback", StateMutability: "payable"},
		},
		// The sources are in the order of their paths.
		Sources:     []File{{Path: "contracts/B.sol", Content: "library B {}"}, {Path: "contracts/C.sol", Content: "contract C {}"}},
		RuntimeCode: ethrpc.Bytes{0x60, 0x80},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Contract:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestContractNoRuntimeMatch(t *testing.T) {
	client := serve(t, http.StatusOK, `{"abi": [], "match": "match", "creationMatch": "match", "runtimeMatch": null}`)
	got, err := client.Contract(t.Context(), ethrpc.Mainnet, address)
	if err != nil {
		t.Fatal(err)
	}
	if got.Match != "match" || got.RuntimeMatch != "" {
		t.Errorf("Contract: got match %q and runtime match %q, want \"match\" and none", got.Match, got.RuntimeMatch)
	}
}

func TestFetchRejectsCreationOnlyMatch(t *testing.T) {
	client := serve(t, http.StatusOK, `{
		"match": "exact_match",
		"creationMatch": "exact_match",
		"runtimeMatch": null,
		"runtimeBytecode": {"onchainBytecode": "0x6080"},
		"abi": [{"type": "function", "name": "misleading", "inputs": []}],
		"sources": {"C.sol": {"content": "contract C {}"}}
	}`)
	lookup, contract, err := Fetch(t.Context(), client, ethrpc.Mainnet, address, ethrpc.Bytes{0x60, 0x80}, ABI, Sources)
	if err == nil || !strings.Contains(err.Error(), "no runtime code match") || lookup != nil || contract != nil {
		t.Errorf("Fetch = %+v, %+v, %v; want an error for a creation-only match", lookup, contract, err)
	}
}

func TestContractNotFound(t *testing.T) {
	for name, client := range map[string]*Client{
		// Sourcify answers 404 with the matches null for a contract that it hasn't
		// verified.
		"404":        serve(t, http.StatusNotFound, `{"match": null, "creationMatch": null, "runtimeMatch": null}`),
		"null match": serve(t, http.StatusOK, `{"match": null}`),
	} {
		if _, err := client.Contract(t.Context(), ethrpc.Mainnet, address); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: got error %v, want ErrNotFound", name, err)
		}
	}
}

func TestContractError(t *testing.T) {
	for name, client := range map[string]*Client{
		"status":  serve(t, http.StatusInternalServerError, `oops`),
		"invalid": serve(t, http.StatusOK, `{"match": "match", "abi": "x"}`),
	} {
		if _, err := client.Contract(t.Context(), ethrpc.Mainnet, address); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s: got error %v, want a failure", name, err)
		}
	}
}

func TestURL(t *testing.T) {
	client := NewClient("https://sourcify.test/server")
	got, err := client.URL(ethrpc.Mainnet, address, Sources)
	want := "https://sourcify.test/server/v2/contract/1/" + address.String() +
		"?fields=compilation.fullyQualifiedName,compilation.compilerVersion,runtimeBytecode.onchainBytecode,sources"
	if got != want || err != nil {
		t.Errorf("URL = %q, %v; want %q", got, err, want)
	}
}
