// Package etherscan fetches the source code record that Etherscan verified for
// a contract: its name, compiler, ABI, and, for a proxy, the implementation
// that Etherscan has for it.
//
// Etherscan is an offchain source (Charter § 3.4). It verifies a contract by
// compiling its source into the contract's code, as Sourcify does, and verifies
// contracts that Sourcify hasn't. Its API needs a key.
package etherscan

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// DefaultAPI is the base URL of Etherscan's multichain API.
const DefaultAPI = "https://api.etherscan.io/v2/api"

// maxResponseSize bounds a response, which has a contract's source code.
const maxResponseSize = 32 << 20

// ErrNoKey is the error for a client without an API key.
var ErrNoKey = errors.New("Etherscan's API needs a key (see the etherscanKey setting)")

// Client fetches contracts from Etherscan.
type Client struct {
	http *http.Client
	API  string
	key  string
}

// NewClient returns a client with the API key.
func NewClient(key string) *Client {
	return &Client{http: httpretry.Client, API: DefaultAPI, key: key}
}

// Param is a parameter of a function, with its name in the source.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Function is a function of a contract.
type Function struct {
	Selector        ethrpc.Bytes `json:"selector"`
	Signature       string       `json:"signature"`
	StateMutability string       `json:"stateMutability"`
	Inputs          []Param      `json:"inputs"`
}

// Special is the fallback or receive function of a contract.
type Special struct {
	StateMutability string `json:"stateMutability"`
}

// Record is Etherscan's record of a contract.
type Record struct {
	// URL is the request that the client sent, without the key, and ObservedAt
	// when.
	URL        string    `json:"url"`
	ObservedAt time.Time `json:"observedAt"`
	// Verified is whether Etherscan verified the contract's source. If not, the
	// other fields are empty.
	Verified bool   `json:"verified"`
	Name     string `json:"contractName"`
	Compiler string `json:"compiler"`
	// Proxy is whether Etherscan treats the contract as a proxy, and Implementation
	// the implementation that it has for it, if any. Etherscan reads it now, not as
	// of a past block.
	Proxy          bool            `json:"proxy"`
	Implementation *ethrpc.Address `json:"implementation"`
	// Functions are the functions of the verified ABI, by selector.
	Functions []Function `json:"functions"`
	Fallback  *Special   `json:"fallback"`
	Receive   *Special   `json:"receive"`
}

// Contract returns Etherscan's record of the contract at address on the chain
// with the given ID.
func (c *Client) Contract(ctx context.Context, chainID uint64, address ethrpc.Address) (*Record, error) {
	if c.key == "" {
		return nil, ErrNoKey
	}
	query := url.Values{
		"chainid": {strconv.FormatUint(chainID, 10)},
		"module":  {"contract"},
		"action":  {"getsourcecode"},
		"address": {address.String()},
	}
	shown := c.API + "?" + query.Encode()
	query.Set("apikey", c.key)
	record := &Record{URL: shown, ObservedAt: time.Now().UTC().Truncate(time.Second)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error has the URL, with the key.
		return nil, errors.New(strings.ReplaceAll(err.Error(), c.key, "<key>"))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %s: %q", resp.Status, snippet(body))
	case len(body) > maxResponseSize:
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseSize)
	}

	var r struct {
		Status  string          `json:"status"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decoding the response: %w", err)
	}
	if r.Status != "1" {
		var text string
		_ = json.Unmarshal(r.Result, &text)
		return nil, fmt.Errorf("Etherscan: %q: %q", r.Message, text)
	}
	var results []struct {
		ABI             string `json:"ABI"`
		ContractName    string `json:"ContractName"`
		CompilerVersion string `json:"CompilerVersion"`
		Proxy           string `json:"Proxy"`
		Implementation  string `json:"Implementation"`
	}
	if err := json.Unmarshal(r.Result, &results); err != nil || len(results) != 1 {
		return nil, fmt.Errorf("decoding the result: %v (%d results)", err, len(results))
	}
	result := results[0]
	if !strings.HasPrefix(strings.TrimSpace(result.ABI), "[") {
		// An unverified contract's ABI is a message, such as "Contract source code not
		// verified".
		return record, nil
	}
	record.Verified = true
	record.Name, record.Compiler = result.ContractName, result.CompilerVersion
	record.Proxy = result.Proxy == "1"
	if result.Implementation != "" {
		implementation, err := ethrpc.ParseAddress(result.Implementation)
		if err != nil {
			return nil, fmt.Errorf("decoding the implementation: %w", err)
		}
		record.Implementation = &implementation
	}
	if err := record.decodeABI(result.ABI); err != nil {
		return nil, fmt.Errorf("ABI of %s: %w", address, err)
	}
	return record, nil
}

// decodeABI sets the functions of the record from the ABI.
func (r *Record) decodeABI(abi string) error {
	var entries []solabi.ABIEntry
	if err := json.Unmarshal([]byte(abi), &entries); err != nil {
		return err
	}
	// The names of the inputs, which solabi doesn't keep.
	var names []struct {
		Inputs []struct {
			Name string `json:"name"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(abi), &names); err != nil {
		return err
	}
	r.Functions = []Function{}
	for i, entry := range entries {
		switch entry.Type {
		case "function":
			signature, err := entry.Signature()
			if err != nil {
				return err
			}
			selector := solabi.Selector(signature)
			f := Function{Selector: selector[:], Signature: signature, StateMutability: entry.Mutability(), Inputs: []Param{}}
			for j, input := range entry.Inputs {
				// The canonical type of one input, as the signature of a function with only
				// that input has it.
				one, err := solabi.ABIEntry{Type: "function", Name: "f", Inputs: []solabi.ABIParam{input}}.Signature()
				if err != nil {
					return err
				}
				p := Param{Type: one[len("f(") : len(one)-1]}
				if j < len(names[i].Inputs) {
					p.Name = names[i].Inputs[j].Name
				}
				f.Inputs = append(f.Inputs, p)
			}
			r.Functions = append(r.Functions, f)
		case "fallback":
			r.Fallback = &Special{StateMutability: entry.Mutability()}
		case "receive":
			r.Receive = &Special{StateMutability: entry.Mutability()}
		}
	}
	slices.SortFunc(r.Functions, func(a, b Function) int {
		return cmp.Or(bytes.Compare(a.Selector, b.Selector), cmp.Compare(a.Signature, b.Signature))
	})
	return nil
}

// snippet returns the start of a response body, for an error message.
func snippet(body []byte) string {
	const limit = 200
	if len(body) > limit {
		body = body[:limit]
	}
	return string(bytes.TrimSpace(body))
}
