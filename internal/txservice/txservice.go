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

// ErrNotFound is the error for a transaction or Safe that the service doesn't
// have.
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
	u += "/"
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}

	var t struct {
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
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("decoding transaction: %w", err)
	}
	tx := &safenet.SafeTransaction{
		ChainID:        c.chainID,
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
	return tx, nil
}

// get fetches the JSON document at the URL u, and returns ErrNotFound if the
// service doesn't have it.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
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
	return body, nil
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
