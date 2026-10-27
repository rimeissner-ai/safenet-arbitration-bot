// Package httpretry retries the HTTP requests that a server rejects with 429
// Too Many Requests, as public RPC nodes, IPFS gateways, and services do when
// they rate limit clients, or with 502 Bad Gateway, 503 Service Unavailable, or
// 504 Gateway Timeout, as the load balancers in front of them do when a backend
// is briefly unavailable. The clients that use it only read, so a request can
// be sent again.
package httpretry

import (
	"cmp"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Client is the HTTP client that the clients of public services use. It retries
// requests that are rate limited, or that a gateway rejects, with the default
// settings of Transport.
var Client = &http.Client{Transport: &Transport{}}

// Default settings of a Transport.
const (
	DefaultRetries  = 4
	DefaultDelay    = 1 * time.Second
	DefaultMaxDelay = 30 * time.Second
)

// Transport is an http.RoundTripper that retries a request whose response is
// 429 Too Many Requests, 502 Bad Gateway, 503 Service Unavailable, or 504
// Gateway Timeout, with exponential backoff, until it gets another response or
// runs out of retries, when it returns the last of those responses.
//
// It waits for the time that the response's Retry-After header asks for, if it
// has one, and otherwise for Delay, doubling for each retry, up to MaxDelay,
// with random jitter so that clients that were rate limited together don't
// retry together. It doesn't retry if Retry-After asks for longer than
// MaxDelay, nor a request with a body that it can't send again (see
// http.Request.GetBody). It stops waiting when the request's context is done.
type Transport struct {
	// Base sends the requests, or http.DefaultTransport if nil.
	Base http.RoundTripper
	// Retries is the number of times that a request is retried, or DefaultRetries
	// if zero.
	Retries int
	// Delay is the time before the first retry, or DefaultDelay if zero.
	Delay time.Duration
	// MaxDelay is the longest time before a retry, or DefaultMaxDelay if zero.
	MaxDelay time.Duration
}

// RoundTrip sends req, retrying it while it is rate limited or rejected by a
// gateway.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	retries := cmp.Or(t.Retries, DefaultRetries)
	backoff := cmp.Or(t.Delay, DefaultDelay)
	maxDelay := cmp.Or(t.MaxDelay, DefaultMaxDelay)

	// Sending a request reads its body to the end, so a retry can only send the
	// body again with a new copy from GetBody. http.NewRequest sets GetBody for a
	// body that it can copy, such as a *bytes.Reader, but not for a stream, so a
	// request with a stream as its body is sent once, and not retried, unless the
	// caller sets GetBody. A request without a body, such as most GET requests, is
	// always retried.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return base.RoundTrip(req)
	}

	attempt := req
	for retry := 0; ; retry++ {
		resp, err := base.RoundTrip(attempt)
		if err != nil || !retryable(resp.StatusCode) || retry == retries {
			return resp, err
		}
		wait, ok := retryAfter(resp, time.Now())
		if !ok {
			wait = min(backoff, maxDelay)
			wait = wait/2 + rand.N(wait/2+1)
		}
		backoff = min(2*backoff, maxDelay)
		if wait > maxDelay {
			return resp, nil
		}

		// The connection can only be reused once the body is read to its end. The body
		// of a response that is retried is usually a short error message, so this reads
		// at most 4 KiB of it. If it is longer, closing it closes the connection, and
		// the retry opens a new one.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		timer := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}

		// A RoundTripper must not modify the request, so each retry sends a copy with a
		// new body.
		attempt = req.Clone(req.Context())
		if req.GetBody != nil {
			if attempt.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
	}
}

// retryable reports whether a response with status is retried: one that rate
// limits the client, or that a gateway answers when its backend is briefly
// unavailable.
func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryAfter returns the time to wait that the Retry-After header of resp asks
// for, as of now: a number of seconds, or an HTTP date.
func retryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
	value := resp.Header.Get("Retry-After")
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(date.Sub(now), 0), true
	}
	return 0, false
}
