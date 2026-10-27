package httpretry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// limited starts a server that rejects the first limit requests with 429 Too
// Many Requests and retryAfter as their Retry-After header, unless it is empty,
// then echoes the bodies of the requests. It returns its URL and a count of the
// requests it has served.
func limited(t *testing.T, limit int64, retryAfter string) (string, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= limit {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		io.Copy(w, r.Body)
	}))
	t.Cleanup(server.Close)
	return server.URL, &requests
}

// client returns a client that retries with t's settings, waiting at most a
// millisecond by default.
func client(t Transport) *http.Client {
	if t.Delay == 0 {
		t.Delay = time.Millisecond
	}
	if t.MaxDelay == 0 {
		t.MaxDelay = time.Millisecond
	}
	return &http.Client{Transport: &t}
}

func TestRetries(t *testing.T) {
	url, requests := limited(t, 2, "")
	resp, err := client(Transport{}).Post(url, "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Each retry sends the body again.
	if resp.StatusCode != http.StatusOK || string(body) != "body" {
		t.Errorf("got %s %q, want 200 OK and the body", resp.Status, body)
	}
	if got := requests.Load(); got != 3 {
		t.Errorf("got %d requests, want 3", got)
	}
}

// TestRetriesGatewayErrors checks that the responses of a gateway whose backend
// is briefly unavailable are retried, and other errors aren't.
func TestRetriesGatewayErrors(t *testing.T) {
	for status, retried := range map[int]bool{
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
		http.StatusGatewayTimeout:      true,
		http.StatusInternalServerError: false,
		http.StatusBadRequest:          false,
	} {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requests.Add(1) == 1 {
				http.Error(w, "unavailable", status)
				return
			}
			io.Copy(w, r.Body)
		}))
		resp, err := client(Transport{}).Post(server.URL, "text/plain", strings.NewReader("body"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		server.Close()
		want, wantRequests := status, int64(1)
		if retried {
			want, wantRequests = http.StatusOK, 2
		}
		if resp.StatusCode != want || requests.Load() != wantRequests {
			t.Errorf("first response %d: got %s after %d requests, want %d after %d", status, resp.Status, requests.Load(), want, wantRequests)
		}
	}
}

func TestRetriesRunOut(t *testing.T) {
	url, requests := limited(t, 10, "")
	resp, err := client(Transport{Retries: 2}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// The last response is returned as it is.
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), "rate limited") {
		t.Errorf("got %s %q, want the last 429 response", resp.Status, body)
	}
	if got := requests.Load(); got != 3 {
		t.Errorf("got %d requests, want 3", got)
	}
}

func TestRetryAfter(t *testing.T) {
	// The server asks for a second, which is longer than MaxDelay.
	url, requests := limited(t, 1, "1")
	resp, err := client(Transport{}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || requests.Load() != 1 {
		t.Errorf("got %s after %d requests, want 429 without retrying", resp.Status, requests.Load())
	}

	// A wait that is shorter than MaxDelay is retried after it.
	url, requests = limited(t, 1, "0")
	resp, err = client(Transport{}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || requests.Load() != 2 {
		t.Errorf("got %s after %d requests, want 200 OK after a retry", resp.Status, requests.Load())
	}
}

func TestRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"":                              -1,
		"soon":                          -1,
		"120":                           2 * time.Minute,
		"Fri, 16 Oct 2026 12:00:30 GMT": 30 * time.Second,
		"Fri, 16 Oct 2026 11:00:00 GMT": 0,
	} {
		resp := &http.Response{Header: http.Header{}}
		if value != "" {
			resp.Header.Set("Retry-After", value)
		}
		got, ok := retryAfter(resp, now)
		if !ok {
			got = -1
		}
		if got != want {
			t.Errorf("retryAfter(%q): got %v, want %v", value, got, want)
		}
	}
}

func TestBodyCannotBeSentAgain(t *testing.T) {
	// A body without GetBody can't be sent again, so it isn't retried.
	url, requests := limited(t, 1, "")
	req, err := http.NewRequest(http.MethodPost, url, io.MultiReader(bytes.NewReader([]byte("body"))))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client(Transport{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || requests.Load() != 1 {
		t.Errorf("got %s after %d requests, want 429 without retrying", resp.Status, requests.Load())
	}
}

func TestContextCanceled(t *testing.T) {
	url, requests := limited(t, 10, "")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = client(Transport{Delay: time.Hour, MaxDelay: time.Hour}).Do(req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got error %v, want the context's", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second || requests.Load() != 1 {
		t.Errorf("waited %v after %d requests, want to stop waiting when the context is done", elapsed, requests.Load())
	}
}
