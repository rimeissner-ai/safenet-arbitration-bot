// Package signatures lists the functions of a contract, with their signatures
// and selectors, from the contract's ABI that Sourcify verified against its
// code.
package signatures

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
	"github.com/safe-research/safenet-arbitration-bot/internal/sourcify"
)

// Report is the functions of a contract.
type Report struct {
	*sourcify.Lookup
	// Functions are the contract's functions, by selector: none for an account
	// without code, and nil for a contract that isn't verified, whose functions are
	// unknown.
	Functions []Function `json:"functions"`
	// Fallback and Receive are the contract's fallback and receive functions, or
	// nil if it has none or isn't verified.
	Fallback *Special `json:"fallback"`
	Receive  *Special `json:"receive"`
}

// Function is a function of a contract.
type Function struct {
	Selector        ethrpc.Bytes `json:"selector"`
	Signature       string       `json:"signature"`
	StateMutability string       `json:"stateMutability"`
}

// Special is the fallback or receive function of a contract.
type Special struct {
	StateMutability string `json:"stateMutability"`
}

// Lookup returns the functions of the contract at address on the chain with the
// given ID, whose code is code, from the ABI that repo verified. It fails if
// the code that repo verified isn't code, or if the ABI is invalid.
func Lookup(ctx context.Context, repo sourcify.Repository, chainID uint64, address ethrpc.Address, code ethrpc.Bytes) (*Report, error) {
	lookup, contract, err := sourcify.Fetch(ctx, repo, chainID, address, code, sourcify.ABI)
	if err != nil {
		return nil, err
	}
	report := &Report{Lookup: lookup}
	switch lookup.Status {
	case sourcify.NoCode:
		report.Functions = []Function{}
		return report, nil
	case sourcify.Unverified:
		return report, nil
	}

	report.Functions = []Function{}
	for _, entry := range contract.ABI {
		switch entry.Type {
		case "function":
			signature, err := entry.Signature()
			if err != nil {
				return nil, fmt.Errorf("ABI of %s: %w", address, err)
			}
			selector := solabi.Selector(signature)
			report.Functions = append(report.Functions, Function{
				Selector:        selector[:],
				Signature:       signature,
				StateMutability: entry.Mutability(),
			})
		case "fallback":
			report.Fallback = &Special{StateMutability: entry.Mutability()}
		case "receive":
			report.Receive = &Special{StateMutability: entry.Mutability()}
		}
	}
	slices.SortFunc(report.Functions, func(a, b Function) int {
		return cmp.Or(bytes.Compare(a.Selector, b.Selector), cmp.Compare(a.Signature, b.Signature))
	})
	for i := 1; i < len(report.Functions); i++ {
		if a, b := report.Functions[i-1], report.Functions[i]; bytes.Equal(a.Selector, b.Selector) {
			return nil, fmt.Errorf("ABI of %s: functions %s and %s have the same selector", address, a.Signature, b.Signature)
		}
	}
	return report, nil
}
