package ethrpc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// useRPCList replaces the process-wide default RPC list with one served from
// body for the duration of the test, and returns a count of the requests made
// for it.
func useRPCList(t *testing.T, body string) *atomic.Int64 {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	previous := defaultRPCs
	defaultRPCs = &rpcList{url: server.URL, http: http.DefaultClient}
	t.Cleanup(func() { defaultRPCs = previous })
	return &requests
}

func TestNewClientWithDefaultRPC(t *testing.T) {
	node := serve(t,
		`{"jsonrpc": "2.0", "method": "eth_blockNumber", "params": []}`,
		`{"result": "0x2a"}`,
	)
	list, err := json.Marshal([]any{
		map[string]any{"chainId": 100, "rpc": []any{map[string]any{"url": "https://unused.example"}}},
		map[string]any{"chainId": testChainID, "rpc": []any{map[string]any{"url": node.url}, map[string]any{"url": "https://unused.example"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	listRequests := useRPCList(t, string(list))

	// Several requests, including from several clients, share one download of the
	// list.
	for range 2 {
		client, err := NewClient(t.Context(), testChainID, "")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		for range 2 {
			number, err := client.BlockNumber(t.Context())
			if err != nil {
				t.Fatalf("BlockNumber: %v", err)
			}
			if number != 42 {
				t.Errorf("BlockNumber: got %d, want 42", number)
			}
		}
	}
	if n := listRequests.Load(); n != 1 {
		t.Errorf("RPC list requests: got %d, want 1", n)
	}
}

// rpcListFor returns an RPC list with urls for testChainID.
func rpcListFor(t *testing.T, urls ...string) string {
	t.Helper()
	var rpcs []any
	for _, url := range urls {
		rpcs = append(rpcs, map[string]any{"url": url})
	}
	list, err := json.Marshal([]any{map[string]any{"chainId": testChainID, "rpc": rpcs}})
	if err != nil {
		t.Fatal(err)
	}
	return string(list)
}

// failingNode starts a server that rejects every request, and returns its URL
// along with a count of the requests it has served.
func failingNode(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server.URL, &requests
}

func TestNewClientTriesDefaultRPCsInOrder(t *testing.T) {
	// Failing nodes and nodes on another chain are skipped, and nodes after the
	// first good one are never contacted.
	failing, failingRequests := failingNode(t)
	wrongChain, wrongChainRequests := node(t, "0x64", func(w http.ResponseWriter, id uint64) {})
	good, goodRequests := node(t, "0x1", func(w http.ResponseWriter, id uint64) {})
	unused, unusedRequests := node(t, "0x1", func(w http.ResponseWriter, id uint64) {})

	useRPCList(t, rpcListFor(t, failing, wrongChain, good, unused))
	client, err := NewClient(t.Context(), testChainID, "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.url != good {
		t.Errorf("NewClient: picked %s, want %s", client.url, good)
	}
	for name, tc := range map[string]struct {
		requests *atomic.Int64
		want     int64
	}{
		"failing":     {failingRequests, 1},
		"wrong chain": {wrongChainRequests, 1},
		"good":        {goodRequests, 1},
		"unused":      {unusedRequests, 0},
	} {
		if n := tc.requests.Load(); n != tc.want {
			t.Errorf("%s node requests: got %d, want %d", name, n, tc.want)
		}
	}
}

func TestNewClientSkipsUnresponsiveDefaultRPC(t *testing.T) {
	previous := connectTimeout
	connectTimeout = 50 * time.Millisecond
	t.Cleanup(func() { connectTimeout = previous })

	// The server doesn't reliably notice the client giving up on the request, and
	// Close waits for in-flight requests, so release the handler before closing the
	// server (cleanups run in reverse order).
	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hanging.Close)
	t.Cleanup(func() { close(release) })
	good, _ := node(t, "0x1", func(w http.ResponseWriter, id uint64) {})

	useRPCList(t, rpcListFor(t, hanging.URL, good))
	client, err := NewClient(t.Context(), testChainID, "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.url != good {
		t.Errorf("NewClient: picked %s, want %s", client.url, good)
	}
}

func TestNewClientFailsIfNoDefaultRPCAnswers(t *testing.T) {
	failing, _ := failingNode(t)
	wrongChain, _ := node(t, "0x64", func(w http.ResponseWriter, id uint64) {})

	useRPCList(t, rpcListFor(t, failing, wrongChain))
	if _, err := NewClient(t.Context(), testChainID, ""); err == nil {
		t.Fatal("NewClient: expected an error")
	}
}

func TestNewClientWithDefaultRPCForUnknownChain(t *testing.T) {
	useRPCList(t, `[{"chainId": 1, "rpc": [{"url": "https://unused.example"}]}]`)
	if _, err := NewClient(t.Context(), 100, ""); err == nil {
		t.Fatal("NewClient: expected an error")
	}
}

func TestDefaultRPCsSkipsUnusableURLs(t *testing.T) {
	useRPCList(t, `[
		{
			"chainId": 1,
			"name": "Ethereum Mainnet",
			"rpc": [
				{"url": "wss://ethereum-rpc.publicnode.com", "tracking": "none"},
				{"url": "https://mainnet.infura.io/v3/${INFURA_API_KEY}"},
				{"url": "ethereum-rpc.publicnode.com"},
				{"url": "https://ethereum-rpc.publicnode.com", "tracking": "none"},
				{"url": "http://localhost:8545"}
			]
		},
		{"chainId": 2, "rpc": [{"url": "wss://only.websockets.example"}]}
	]`)

	urls, err := DefaultRPCs(t.Context(), 1)
	if err != nil {
		t.Fatalf("DefaultRPCs: %v", err)
	}
	if want := []string{"https://ethereum-rpc.publicnode.com", "http://localhost:8545"}; !slices.Equal(urls, want) {
		t.Errorf("DefaultRPCs: got %q, want %q", urls, want)
	}
	if _, err := DefaultRPCs(t.Context(), 2); err == nil {
		t.Error("DefaultRPCs: expected an error for a chain without usable URLs")
	}
}

func TestDefaultRPCsRetriesAfterFailure(t *testing.T) {
	requests := useRPCList(t, `[]`)
	for range 2 {
		if _, err := DefaultRPCs(t.Context(), 1); err == nil {
			t.Fatal("DefaultRPCs: expected an error")
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("RPC list requests: got %d, want 2", n)
	}
}

func TestDefaultRPCsRejectsInvalidList(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":         `<html>`,
		"not an array":     `{"chains": []}`,
		"empty":            `[]`,
		"no valid entries": `[{"chainId": 1, "rpc": [{"url": "wss://example.com"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			useRPCList(t, body)
			if _, err := DefaultRPCs(t.Context(), 1); err == nil {
				t.Fatal("DefaultRPCs: expected an error")
			}
		})
	}
}

func TestDefaultRPCsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	list := &rpcList{url: server.URL, http: http.DefaultClient}
	if _, err := list.get(t.Context()); err == nil {
		t.Fatal("get: expected an error")
	}
}
