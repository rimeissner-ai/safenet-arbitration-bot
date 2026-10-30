// Package sourcify is a client for Sourcify, a public repository of verified
// contracts: it compiles the source code that anyone submits for a contract,
// and records it if the result matches the contract's code onchain.
//
// Sourcify is an offchain source (Charter § 3.4). Its records are public,
// attributed to Sourcify and the compilation, and verifiable by compiling the
// source again. Callers check what it returns against onchain data, such as the
// contract's code.
package sourcify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// DefaultURL is the base URL of Sourcify's public server, which needs no API
// key.
const DefaultURL = "https://sourcify.dev/server"

// maxResponseSize is the largest response that the client accepts: a contract's
// code, ABI, and sources, which are larger than its code, but not by orders of
// magnitude.
const maxResponseSize = 32 << 20

// ErrNotFound is the error for a contract that Sourcify hasn't verified.
var ErrNotFound = errors.New("contract not verified")

// Contract is a contract that Sourcify verified.
type Contract struct {
	// Match is how well the compiled code matches the code onchain, either at
	// creation or at runtime: "exact_match" if the metadata hash matches too, and
	// "match" otherwise. RuntimeMatch is the same for the runtime code only, and is
	// empty if it doesn't match.
	Match, RuntimeMatch string
	// VerifiedAt is when Sourcify verified the contract.
	VerifiedAt time.Time
	// Name is the fully qualified name of the contract that was compiled, such as
	// "contracts/Safe.sol:Safe", and Compiler is its compiler version.
	Name, Compiler string
	// ABI is the contract's ABI, as the compiler wrote it, if it was fetched.
	ABI []solabi.ABIEntry
	// Sources are the source files that the contract was compiled from, in the
	// order of their paths, if they were fetched.
	Sources []File
	// RuntimeCode is the contract's runtime code onchain, as Sourcify fetched it.
	RuntimeCode ethrpc.Bytes
}

// File is a source file of a contract.
type File struct {
	// Path is the file's path in the compilation, such as "contracts/Safe.sol".
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Field is an optional field of a Contract, which Contract fetches if asked to.
type Field string

// The optional fields of a Contract.
const (
	ABI     Field = "abi"
	Sources Field = "sources"
)

// Client fetches verified contracts from Sourcify.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient returns a client for the Sourcify server at the base URL baseURL,
// such as DefaultURL.
func NewClient(baseURL string) *Client {
	return &Client{http: httpretry.Client, baseURL: baseURL}
}

// URL returns the URL that Contract fetches the contract at address on the
// chain with the given ID from, with fields.
func (c *Client) URL(chainID uint64, address ethrpc.Address, fields ...Field) (string, error) {
	u, err := url.JoinPath(c.baseURL, "v2", "contract", strconv.FormatUint(chainID, 10), address.String())
	if err != nil {
		return "", err
	}
	names := []string{"compilation.fullyQualifiedName", "compilation.compilerVersion", "runtimeBytecode.onchainBytecode"}
	for _, f := range fields {
		names = append(names, string(f))
	}
	return u + "?fields=" + strings.Join(names, ","), nil
}

// Contract returns the contract that Sourcify verified at address on the chain
// with the given ID, with fields, or ErrNotFound.
func (c *Client) Contract(ctx context.Context, chainID uint64, address ethrpc.Address, fields ...Field) (*Contract, error) {
	u, err := c.URL(chainID, address, fields...)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %s: %q", resp.Status, snippet(body))
	case len(body) > maxResponseSize:
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseSize)
	}

	var r struct {
		Match        *string           `json:"match"`
		RuntimeMatch *string           `json:"runtimeMatch"`
		VerifiedAt   time.Time         `json:"verifiedAt"`
		ABI          []solabi.ABIEntry `json:"abi"`
		Sources      map[string]struct {
			Content string `json:"content"`
		} `json:"sources"`
		Compilation struct {
			FullyQualifiedName string `json:"fullyQualifiedName"`
			CompilerVersion    string `json:"compilerVersion"`
		} `json:"compilation"`
		RuntimeBytecode struct {
			OnchainBytecode ethrpc.Bytes `json:"onchainBytecode"`
		} `json:"runtimeBytecode"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decoding contract: %w", err)
	}
	if r.Match == nil {
		return nil, ErrNotFound
	}
	contract := &Contract{
		Match:       *r.Match,
		VerifiedAt:  r.VerifiedAt,
		Name:        r.Compilation.FullyQualifiedName,
		Compiler:    r.Compilation.CompilerVersion,
		ABI:         r.ABI,
		RuntimeCode: r.RuntimeBytecode.OnchainBytecode,
	}
	if r.RuntimeMatch != nil {
		contract.RuntimeMatch = *r.RuntimeMatch
	}
	for _, path := range slices.Sorted(maps.Keys(r.Sources)) {
		contract.Sources = append(contract.Sources, File{Path: path, Content: r.Sources[path].Content})
	}
	return contract, nil
}

// snippet returns the start of a response body, for an error message, which
// quotes it since it isn't trusted.
func snippet(body []byte) string {
	const limit = 200
	if len(body) > limit {
		body = body[:limit]
	}
	return string(bytes.TrimSpace(body))
}
