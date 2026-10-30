package sourcify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// The statuses of a Lookup.
const (
	// Verified is the status of a contract that Sourcify verified.
	Verified = "verified"
	// Unverified is the status of a contract that Sourcify hasn't verified.
	Unverified = "unverified"
	// NoCode is the status of an account without code, which has nothing to verify.
	NoCode = "no-code"
)

// Repository fetches verified contracts, like a *Client.
type Repository interface {
	URL(chainID uint64, address ethrpc.Address, fields ...Field) (string, error)
	Contract(ctx context.Context, chainID uint64, address ethrpc.Address, fields ...Field) (*Contract, error)
}

// Lookup is the result of looking up a contract in Sourcify, as of its code at
// a block.
type Lookup struct {
	Address  ethrpc.Address `json:"address"`
	CodeHash ethrpc.Hash    `json:"codeHash"`
	CodeSize int            `json:"codeSize"`
	// Status is Verified, Unverified, or NoCode.
	Status string `json:"status"`
	// Source is the record of the contract in Sourcify, or nil for an account
	// without code, which isn't looked up.
	Source *Record `json:"source"`
}

// Record is the record of a contract in Sourcify.
type Record struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// ObservedAt is when the record was fetched.
	ObservedAt time.Time `json:"observedAt"`
	// The other fields are empty for a contract that Sourcify hasn't verified. See
	// Contract.
	Match        string     `json:"match,omitempty"`
	RuntimeMatch string     `json:"runtimeMatch,omitempty"`
	VerifiedAt   *time.Time `json:"verifiedAt,omitempty"`
	Contract     string     `json:"contract,omitempty"`
	Compiler     string     `json:"compiler,omitempty"`
}

// Fetch looks up the contract at address on the chain with the given ID, whose
// code is code, in repo, with fields. It returns the contract that repo
// verified at runtime, or nil if the account has no code or repo hasn't
// verified it. It fails if Sourcify only matched the creation code, or if the
// runtime code that repo verified isn't code, such as for a contract that was
// destroyed and created again since.
func Fetch(ctx context.Context, repo Repository, chainID uint64, address ethrpc.Address, code ethrpc.Bytes, fields ...Field) (*Lookup, *Contract, error) {
	lookup := &Lookup{Address: address, CodeHash: keccak256.Hash(code), CodeSize: len(code)}
	if len(code) == 0 {
		lookup.Status = NoCode
		return lookup, nil, nil
	}

	url, err := repo.URL(chainID, address, fields...)
	if err != nil {
		return nil, nil, err
	}
	lookup.Source = &Record{Name: "sourcify", URL: url, ObservedAt: time.Now().UTC().Truncate(time.Second)}
	contract, err := repo.Contract(ctx, chainID, address, fields...)
	if errors.Is(err, ErrNotFound) {
		lookup.Status = Unverified
		return lookup, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("fetching %s from Sourcify: %w", address, err)
	}
	if !bytes.Equal(contract.RuntimeCode, code) {
		return nil, nil, fmt.Errorf("the code of %s that Sourcify verified has hash %s, not the hash %s of its code at the block",
			address, ethrpc.Hash(keccak256.Hash(contract.RuntimeCode)), lookup.CodeHash)
	}
	if contract.RuntimeMatch != "match" && contract.RuntimeMatch != "exact_match" {
		return nil, nil, fmt.Errorf("Sourcify has no runtime code match for %s, so its ABI and sources aren't verified against the code at the block", address)
	}

	lookup.Status = Verified
	source := lookup.Source
	source.Match, source.RuntimeMatch = contract.Match, contract.RuntimeMatch
	source.VerifiedAt, source.Contract, source.Compiler = &contract.VerifiedAt, contract.Name, contract.Compiler
	return lookup, contract, nil
}
