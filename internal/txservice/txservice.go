// Package txservice is a client for the Safe Transaction Service, which indexes
// the Safe transactions that owners have signed and submitted, executed or not.
//
// The service is not trusted: it is an offchain source (Charter § 3.4). Callers
// check what it returns against onchain data, such as the hash of the Safe
// transaction.
package txservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// DefaultURLs are the base URLs of Safe's public transaction services, by chain
// ID, for the networks that the Charter covers. They need no API key.
var DefaultURLs = map[uint64]string{
	ethrpc.Mainnet:     "https://api.safe.global/tx-service/eth",
	ethrpc.ArbitrumOne: "https://api.safe.global/tx-service/arb1",
	ethrpc.Gnosis:      "https://api.safe.global/tx-service/gno",
}

// maxResponseSize is the largest response that the client accepts. A Safe
// transaction has calldata, which is bounded by the size of a block.
const maxResponseSize = 8 << 20

// ErrNotFound is the error for a transaction that the service doesn't have.
var ErrNotFound = errors.New("transaction not found")

// Client fetches Safe transactions from a Safe Transaction Service.
type Client struct {
	http    *http.Client
	baseURL string
	chainID *big.Int
}

// NewClient returns a client for the service at the base URL baseURL, such as
// "https://api.safe.global/tx-service/eth", which serves the Safes of the chain
// with the given ID.
func NewClient(baseURL string, chainID uint64) *Client {
	return &Client{http: httpretry.Client, baseURL: baseURL, chainID: new(big.Int).SetUint64(chainID)}
}

// Transaction returns the Safe transaction that the service has with the Safe
// transaction hash safeTxHash, or ErrNotFound. The transaction's fields are as
// the service reports them, so it may not have the hash it was asked for.
func (c *Client) Transaction(ctx context.Context, safeTxHash ethrpc.Hash) (*safenet.SafeTransaction, error) {
	u, err := url.JoinPath(c.baseURL, "api", "v1", "multisig-transactions", safeTxHash.String())
	if err != nil {
		return nil, err
	}
	// The service redirects paths without a trailing slash.
	var t transaction
	if err := c.get(ctx, u+"/", &t); err != nil {
		return nil, err
	}
	return t.safeTransaction(c.chainID), nil
}

// Executed is a Safe transaction that the service reports as executed.
type Executed struct {
	// SafeTxHash is the hash that the service has for the transaction, which may
	// not be its hash.
	SafeTxHash ethrpc.Hash
	// TxHash is the hash of the onchain transaction that the service says executed
	// it, or nil if it has none.
	TxHash *ethrpc.Hash
	// Transaction has the fields that the service has for the transaction.
	Transaction *safenet.SafeTransaction
}

// maxPages is the largest number of pages of executed transactions that
// Executed reads.
const maxPages = 100

// pageSize is the number of transactions that Executed asks for in each page.
const pageSize = 100

// Executed returns the Safe transactions of safe with a nonce below nonce that
// the service reports as executed, in the order of their nonces. None of what
// it returns is checked: the caller confirms it onchain.
func (c *Client) Executed(ctx context.Context, safe ethrpc.Address, nonce *big.Int) ([]Executed, error) {
	// The service wants the Safe's address with its EIP-55 checksum, which String
	// has.
	u, err := url.JoinPath(c.baseURL, "api", "v1", "safes", safe.String(), "multisig-transactions")
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"executed":  {"true"},
		"nonce__lt": {nonce.String()},
		"ordering":  {"nonce"},
		"limit":     {fmt.Sprint(pageSize)},
	}
	next := u + "/?" + query.Encode()
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}

	var executed []Executed
	for range maxPages {
		var page struct {
			Next    *string `json:"next"`
			Results []struct {
				transaction
				SafeTxHash      ethrpc.Hash  `json:"safeTxHash"`
				TransactionHash *ethrpc.Hash `json:"transactionHash"`
			} `json:"results"`
		}
		if err := c.get(ctx, next, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Results {
			executed = append(executed, Executed{
				SafeTxHash:  r.SafeTxHash,
				TxHash:      r.TransactionHash,
				Transaction: r.transaction.safeTransaction(c.chainID),
			})
		}
		if page.Next == nil || *page.Next == "" {
			return executed, nil
		}
		// The service isn't trusted, so it is only followed to pages of its own.
		n, err := url.Parse(*page.Next)
		if err != nil || n.Scheme != base.Scheme || n.Host != base.Host {
			return nil, fmt.Errorf("next page %q is not on the service", snippet([]byte(*page.Next)))
		}
		next = n.String()
	}
	return nil, fmt.Errorf("more than %d pages of executed transactions", maxPages)
}

// get fetches the JSON document at u and decodes it into v. It returns
// ErrNotFound for a 404 response.
func (c *Client) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %s: %q", resp.Status, snippet(body))
	case len(body) > maxResponseSize:
		return fmt.Errorf("response exceeds %d bytes", maxResponseSize)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// transaction is a Safe transaction as the service encodes it.
type transaction struct {
	Safe           ethrpc.Address `json:"safe"`
	To             ethrpc.Address `json:"to"`
	Value          number         `json:"value"`
	Data           *ethrpc.Bytes  `json:"data"`
	Operation      uint8          `json:"operation"`
	SafeTxGas      number         `json:"safeTxGas"`
	BaseGas        number         `json:"baseGas"`
	GasPrice       number         `json:"gasPrice"`
	GasToken       ethrpc.Address `json:"gasToken"`
	RefundReceiver ethrpc.Address `json:"refundReceiver"`
	Nonce          number         `json:"nonce"`
}

func (t *transaction) safeTransaction(chainID *big.Int) *safenet.SafeTransaction {
	tx := &safenet.SafeTransaction{
		ChainID:        new(big.Int).Set(chainID),
		Safe:           t.Safe,
		To:             t.To,
		Value:          t.Value.Int,
		Operation:      safenet.Operation(t.Operation),
		SafeTxGas:      t.SafeTxGas.Int,
		BaseGas:        t.BaseGas.Int,
		GasPrice:       t.GasPrice.Int,
		GasToken:       t.GasToken,
		RefundReceiver: t.RefundReceiver,
		Nonce:          t.Nonce.Int,
	}
	if t.Data != nil {
		tx.Data = *t.Data
	}
	return tx
}

// number is an integer that the service encodes as a JSON number or as a
// string, depending on the field.
type number struct {
	*big.Int
}

func (n *number) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		s = string(bytes.TrimSpace(data))
	}
	n.Int = new(big.Int)
	if _, ok := n.SetString(s, 10); !ok || n.Sign() < 0 {
		return fmt.Errorf("invalid integer %q", s)
	}
	return nil
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
