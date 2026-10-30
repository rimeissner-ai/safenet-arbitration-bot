package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// sourcify is a Sourcify server that serves the contracts of a test.
type sourcify struct {
	*httptest.Server
	mu sync.Mutex
	// contracts are the contracts on Ethereum Mainnet, by address.
	contracts map[ethrpc.Address]contract
}

// contract is a contract that the Sourcify server verified.
type contract struct {
	abi     json.RawMessage
	sources map[string]string
}

// ServeSourcify serves the contract at address on the Mainnet node, with the
// JSON ABI abi and the source files sources, by path, from the Sourcify server
// that Arbot configures arbot to use, starting it if needed. The server
// verified the contract's code as of each request, as Sourcify does when it
// fetches the code.
func (n *Testnet) ServeSourcify(address ethrpc.Address, abi string, sources map[string]string) {
	n.tb.Helper()
	if n.sourcify == nil {
		s := &sourcify{contracts: make(map[ethrpc.Address]contract)}
		s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, found := strings.CutPrefix(r.URL.Path, "/v2/contract/1/")
			address, err := ethrpc.ParseAddress(path)
			s.mu.Lock()
			c, ok := s.contracts[address]
			s.mu.Unlock()
			if !found || err != nil || !ok {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"match": null, "creationMatch": null, "runtimeMatch": null}`))
				return
			}
			// The handler can't fail the test, which runs in another goroutine, so it fails
			// the request instead.
			var code ethrpc.Bytes
			if err := n.Mainnet.RawRequest(r.Context(), &code, "eth_getCode", address, "latest"); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			sources := make(map[string]any)
			for path, content := range c.sources {
				sources[path] = map[string]string{"content": content}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"abi":     c.abi,
				"sources": sources,
				"compilation": map[string]any{
					"fullyQualifiedName": "contracts/Test.sol:Test",
					"compilerVersion":    "0.7.6+commit.7338295f",
				},
				"runtimeBytecode": map[string]any{"onchainBytecode": code},
				"creationMatch":   "exact_match",
				"runtimeMatch":    "exact_match",
				"match":           "exact_match",
				"verifiedAt":      "2025-06-25T08:00:07Z",
			})
		}))
		n.tb.Cleanup(s.Close)
		n.sourcify = s
	}
	n.sourcify.mu.Lock()
	defer n.sourcify.mu.Unlock()
	n.sourcify.contracts[address] = contract{abi: json.RawMessage(abi), sources: sources}
}
