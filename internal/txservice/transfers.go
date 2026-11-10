package txservice

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// Transfer types, as the service reports them.
const (
	ERC20Transfer  = "ERC20_TRANSFER"
	ERC721Transfer = "ERC721_TRANSFER"
	EtherTransfer  = "ETHER_TRANSFER"
)

// transfersPageSize is how many transfers the client asks for in one request,
// and maxTransfers how many it reads in all, which bounds the requests that a
// Safe with a long history takes.
const (
	transfersPageSize = 100
	maxTransfers      = 10_000
)

// Transfer is a transfer into or out of a Safe, as the service reports it.
type Transfer struct {
	// Type is ERC20Transfer, ERC721Transfer, or EtherTransfer, for a transfer of
	// the chain's native currency.
	Type   string
	Block  uint64
	TxHash ethrpc.Hash
	// LogIndex is the index of the log of the transfer in its block, for a transfer
	// that the service read from a log, and nil for one that it read from a trace,
	// such as a transfer of the native currency.
	LogIndex *uint64
	// Token is the contract of the token, and nil for the native currency.
	Token *ethrpc.Address
	From  ethrpc.Address
	To    ethrpc.Address
	// Value is the amount in the smallest unit of the token or the currency, and
	// nil for an ERC-721 transfer, which has a TokenID instead.
	Value   *big.Int
	TokenID *big.Int
}

// Transfers returns the transfers into and out of safe that the service has
// indexed, newest first, as it reports them. It fails if the Safe has more than
// maxTransfers, rather than return part of its history.
//
// The service indexes the transfers of the Safes that it knows of, from logs
// and traces, so it may leave some out, such as those of a token that it
// doesn't index, and it reports them as it sees fit: callers check them against
// the chain.
func (c *Client) Transfers(ctx context.Context, safe ethrpc.Address) ([]Transfer, error) {
	// The service rejects an address without its EIP-55 checksum.
	base, err := url.JoinPath(c.baseURL, "api", "v1", "safes", safe.String(), "transfers")
	if err != nil {
		return nil, err
	}
	transfers := []Transfer{}
	for offset := 0; ; offset += transfersPageSize {
		// The service redirects paths without a trailing slash. The client pages with
		// offsets rather than following the service's links, so that it only requests
		// URLs that it made.
		u := fmt.Sprintf("%s/?limit=%d&offset=%d", base, transfersPageSize, offset)
		body, err := c.get(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("listing transfers of %s: %w", safe, err)
		}
		var page struct {
			Count   int               `json:"count"`
			Results []json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding transfers of %s: %w", safe, err)
		}
		if page.Count > maxTransfers {
			return nil, fmt.Errorf("%s has %d transfers, more than the %d that are read", safe, page.Count, maxTransfers)
		}
		for _, raw := range page.Results {
			t, err := decodeTransfer(raw)
			if err != nil {
				return nil, fmt.Errorf("decoding transfer of %s: %w", safe, err)
			}
			transfers = append(transfers, t)
		}
		if len(page.Results) == 0 || offset+len(page.Results) >= page.Count {
			return transfers, nil
		}
	}
}

// decodeTransfer decodes a transfer of the service's transfers list.
func decodeTransfer(raw []byte) (Transfer, error) {
	var t struct {
		Type            string          `json:"type"`
		BlockNumber     uint64          `json:"blockNumber"`
		TransactionHash ethrpc.Hash     `json:"transactionHash"`
		TransferID      string          `json:"transferId"`
		TokenAddress    *ethrpc.Address `json:"tokenAddress"`
		From            ethrpc.Address  `json:"from"`
		To              ethrpc.Address  `json:"to"`
		Value           *number         `json:"value"`
		TokenID         *number         `json:"tokenId"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return Transfer{}, err
	}
	transfer := Transfer{Type: t.Type, Block: t.BlockNumber, TxHash: t.TransactionHash, Token: t.TokenAddress, From: t.From, To: t.To}
	if t.Value != nil {
		transfer.Value = t.Value.Int
	}
	if t.TokenID != nil {
		transfer.TokenID = t.TokenID.Int
	}

	// The transfer's ID is "e" for a log or "i" for a trace, then the hex
	// transaction hash without 0x, then the log index in decimal or the trace's
	// address.
	hash := strings.TrimPrefix(t.TransactionHash.String(), "0x")
	if len(t.TransferID) < 1+len(hash) || !strings.EqualFold(t.TransferID[1:1+len(hash)], hash) {
		return Transfer{}, fmt.Errorf("transfer ID %q isn't of transaction %s", t.TransferID, t.TransactionHash)
	}
	switch t.TransferID[0] {
	case 'e':
		index, err := strconv.ParseUint(t.TransferID[1+len(hash):], 10, 64)
		if err != nil {
			return Transfer{}, fmt.Errorf("transfer ID %q has no log index", t.TransferID)
		}
		transfer.LogIndex = &index
	case 'i':
	default:
		return Transfer{}, fmt.Errorf("transfer ID %q is neither of a log nor of a trace", t.TransferID)
	}

	switch t.Type {
	case ERC20Transfer:
		if transfer.Token == nil || transfer.Value == nil || transfer.LogIndex == nil {
			return Transfer{}, fmt.Errorf("ERC-20 transfer %q has no token, value, or log", t.TransferID)
		}
	case ERC721Transfer:
		if transfer.Token == nil || transfer.TokenID == nil || transfer.LogIndex == nil {
			return Transfer{}, fmt.Errorf("ERC-721 transfer %q has no token, token ID, or log", t.TransferID)
		}
		transfer.Value = nil
	case EtherTransfer:
		if transfer.Token != nil || transfer.Value == nil {
			return Transfer{}, fmt.Errorf("native transfer %q has a token or no value", t.TransferID)
		}
	default:
		return Transfer{}, fmt.Errorf("transfer %q has unknown type %q", t.TransferID, t.Type)
	}
	return transfer, nil
}
