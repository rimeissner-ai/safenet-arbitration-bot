package ipfs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// useGatewayList replaces the process-wide default gateway list with one served
// from body for the duration of the test, and returns a count of the requests
// made for it.
func useGatewayList(t *testing.T, body string) *atomic.Int64 {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	previous := defaultGateways
	defaultGateways = &gatewayList{url: server.URL, http: http.DefaultClient}
	t.Cleanup(func() { defaultGateways = previous })
	return &requests
}

func TestFetchUsesDefaultGateway(t *testing.T) {
	first, second := []byte("first"), []byte("second")
	upstream, gatewayRequests := gateway(t, first)
	missing, _ := gateway(t, nil)

	list, err := json.Marshal([]string{upstream.url, missing.url})
	if err != nil {
		t.Fatal(err)
	}
	listRequests := useGatewayList(t, string(list))

	client, _ := newClient(t, "")
	got, err := client.Fetch(t.Context(), ComputeCID(first))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Errorf("Fetch: got %q, want %q", got, first)
	}

	// Serving from the cache doesn't need a gateway, and later downloads, including
	// by other clients, reuse the downloaded list.
	if _, err := client.Fetch(t.Context(), ComputeCID(first)); err != nil {
		t.Fatalf("Fetch from cache: %v", err)
	}
	other, _ := newClient(t, "")
	if _, err := other.Fetch(t.Context(), ComputeCID(first)); err != nil {
		t.Fatalf("Fetch with another client: %v", err)
	}
	if _, err := other.Fetch(t.Context(), ComputeCID(second)); err == nil {
		t.Fatal("Fetch: expected an error for content the gateway doesn't have")
	}
	if n := listRequests.Load(); n != 1 {
		t.Errorf("gateway list requests: got %d, want 1", n)
	}
	if n := gatewayRequests.Load(); n != 3 {
		t.Errorf("gateway requests: got %d, want 3", n)
	}
}

// serve starts an HTTP gateway that answers every request with handler, and
// returns its URL along with a count of the requests it has served.
func serve(t *testing.T, handler http.HandlerFunc) (string, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL, &requests
}

// useGateways replaces the process-wide default gateway list with gateways for
// the duration of the test.
func useGateways(t *testing.T, gateways ...string) {
	t.Helper()
	list, err := json.Marshal(gateways)
	if err != nil {
		t.Fatal(err)
	}
	useGatewayList(t, string(list))
}

// hang starts an HTTP gateway that never answers, and returns its URL.
func hang(t *testing.T) string {
	t.Helper()
	// The server doesn't reliably notice the client giving up on the request, and
	// Close waits for in-flight requests, so release the handler before closing the
	// server (cleanups run in reverse order).
	release := make(chan struct{})
	url, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	return url
}

func TestFetchRacesDefaultGateways(t *testing.T) {
	// Unresponsive gateways, failing gateways, and gateways serving the wrong
	// content lose the race, whatever their position in the list.
	first, second := []byte("first"), []byte("second")
	failing, failingRequests := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	})
	tampering, tamperingRequests := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	})
	upstream, goodRequests := gateway(t, first)
	good := upstream.url

	useGateways(t, hang(t), failing, tampering, good)
	client, _ := newClient(t, "")
	got, err := client.Fetch(t.Context(), ComputeCID(first))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Errorf("Fetch: got %q, want %q", got, first)
	}
	if client.url != good {
		t.Errorf("Fetch: picked %s, want %s", client.url, good)
	}

	// Later downloads use the picked gateway only, even if it fails. The losing
	// gateways may not have received their request before the race was won, so they
	// served at most the first one.
	if _, err := client.Fetch(t.Context(), ComputeCID(second)); err == nil {
		t.Fatal("Fetch: expected an error for content the gateway doesn't have")
	}
	if n := goodRequests.Load(); n != 2 {
		t.Errorf("good gateway requests: got %d, want 2", n)
	}
	for name, requests := range map[string]*atomic.Int64{
		"failing":   failingRequests,
		"tampering": tamperingRequests,
	} {
		if n := requests.Load(); n > 1 {
			t.Errorf("%s gateway requests: got %d, want at most 1", name, n)
		}
	}
}

func TestFetchTimesOutIfNoDefaultGatewayAnswers(t *testing.T) {
	previous := gatewayTimeout
	gatewayTimeout = 50 * time.Millisecond
	t.Cleanup(func() { gatewayTimeout = previous })

	useGateways(t, hang(t), hang(t))
	client, _ := newClient(t, "")
	if _, err := client.Fetch(t.Context(), ComputeCID([]byte("content"))); err == nil {
		t.Fatal("Fetch: expected an error")
	}
	if client.url != "" {
		t.Errorf("Fetch: picked %s, want none", client.url)
	}
}

func TestFetchFailsIfNoDefaultGatewayServesContent(t *testing.T) {
	failing, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	})
	upstream, _ := gateway(t, nil)

	useGateways(t, failing, upstream.url)
	client, _ := newClient(t, "")
	if _, err := client.Fetch(t.Context(), ComputeCID([]byte("missing"))); err == nil {
		t.Fatal("Fetch: expected an error")
	}
	if client.url != "" {
		t.Errorf("Fetch: picked %s, want none", client.url)
	}
}

func TestFetchDoesNotDownloadGatewayListOnCacheHit(t *testing.T) {
	content := []byte("content")
	cached, _ := gateway(t, content)
	if _, err := cached.Fetch(t.Context(), ComputeCID(content)); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	listRequests := useGatewayList(t, `[]`)
	client := NewClient("")
	client.cache = cached.cache
	if _, err := client.Fetch(t.Context(), ComputeCID(content)); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := listRequests.Load(); n != 0 {
		t.Errorf("gateway list requests: got %d, want 0", n)
	}
}

func TestDefaultGatewaysSkipsInvalidEntries(t *testing.T) {
	useGatewayList(t, `["ipfs.filebase.io", "https://ipfs.filebase.io", "ftp://example.com", "http://localhost:8080"]`)
	gateways, err := DefaultGateways(t.Context())
	if err != nil {
		t.Fatalf("DefaultGateways: %v", err)
	}
	if want := []string{"https://ipfs.filebase.io", "http://localhost:8080"}; !slices.Equal(gateways, want) {
		t.Errorf("DefaultGateways: got %q, want %q", gateways, want)
	}
}

func TestDefaultGatewaysRetriesAfterFailure(t *testing.T) {
	requests := useGatewayList(t, `[]`)
	for range 2 {
		if _, err := DefaultGateways(t.Context()); err == nil {
			t.Fatal("DefaultGateways: expected an error")
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("gateway list requests: got %d, want 2", n)
	}
}

func TestDefaultGatewaysRejectsInvalidList(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":         `<html>`,
		"not an array":     `{"gateways": []}`,
		"empty":            `[]`,
		"no valid entries": `["ipfs.filebase.io", "ftp://ipfs.filebase.io"]`,
	} {
		t.Run(name, func(t *testing.T) {
			useGatewayList(t, body)
			if _, err := DefaultGateways(t.Context()); err == nil {
				t.Fatal("DefaultGateways: expected an error")
			}
		})
	}
}

func TestDefaultGatewaysHTTPError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	list := &gatewayList{url: server.URL, http: http.DefaultClient}
	if _, err := list.get(t.Context()); err == nil {
		t.Fatal("get: expected an error")
	}
}
