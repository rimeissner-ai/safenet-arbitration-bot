package txservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// pageSize is the number of results that the client asks for in each page of a
// list, and maxPages bounds the pages it reads, so that a Safe with a huge
// history can't make it read forever.
var pageSize = 100

const maxPages = 200

// ErrNotIndexed is the error for an account that the service doesn't index,
// such as one that isn't a Safe.
var ErrNotIndexed = errors.New("account not indexed by the service")

// Creation is the service's record of the creation of a Safe.
type Creation struct {
	// Created is when the Safe was created, as the time of the block of its
	// creation transaction.
	Created         time.Time       `json:"created"`
	Creator         ethrpc.Address  `json:"creator"`
	TransactionHash ethrpc.Hash     `json:"transactionHash"`
	Factory         *ethrpc.Address `json:"factory"`
	Singleton       *ethrpc.Address `json:"singleton"`
	SetupData       *ethrpc.Bytes   `json:"setupData"`
	// Owners and Threshold are those that the Safe was set up with, as the service
	// decodes them from its setup data, or empty if it doesn't.
	Owners    []ethrpc.Address `json:"owners"`
	Threshold *string          `json:"threshold"`
}

// Creation returns the service's record of the creation of the Safe at address,
// or ErrNotIndexed.
func (c *Client) Creation(ctx context.Context, address ethrpc.Address) (*Creation, error) {
	var r struct {
		Created         time.Time       `json:"created"`
		Creator         ethrpc.Address  `json:"creator"`
		TransactionHash ethrpc.Hash     `json:"transactionHash"`
		FactoryAddress  *ethrpc.Address `json:"factoryAddress"`
		MasterCopy      *ethrpc.Address `json:"masterCopy"`
		SetupData       *ethrpc.Bytes   `json:"setupData"`
		DataDecoded     *struct {
			Method     string `json:"method"`
			Parameters []struct {
				Name  string          `json:"name"`
				Value json.RawMessage `json:"value"`
			} `json:"parameters"`
		} `json:"dataDecoded"`
	}
	if err := c.get(ctx, []string{"safes", address.String(), "creation"}, nil, &r); err != nil {
		return nil, err
	}
	creation := &Creation{
		Created: r.Created, Creator: r.Creator, TransactionHash: r.TransactionHash,
		Factory: r.FactoryAddress, Singleton: r.MasterCopy, SetupData: r.SetupData,
		Owners: []ethrpc.Address{},
	}
	if d := r.DataDecoded; d != nil && d.Method == "setup" {
		for _, p := range d.Parameters {
			switch p.Name {
			case "_owners":
				if err := json.Unmarshal(p.Value, &creation.Owners); err != nil {
					return nil, fmt.Errorf("decoding owners of the setup: %w", err)
				}
			case "_threshold":
				var s string
				if err := json.Unmarshal(p.Value, &s); err != nil {
					return nil, fmt.Errorf("decoding threshold of the setup: %w", err)
				}
				creation.Threshold = &s
			}
		}
	}
	return creation, nil
}

// Transfer is a transfer into or out of a Safe that the service indexed. The
// service finds native transfers in traces, so it lists those made by internal
// calls too.
type Transfer struct {
	// Type is the service's type of the transfer, such as "ETHER_TRANSFER",
	// "ERC20_TRANSFER", or "ERC721_TRANSFER".
	Type            string          `json:"type"`
	Block           uint64          `json:"block"`
	ExecutionDate   time.Time       `json:"executionDate"`
	TransactionHash ethrpc.Hash     `json:"txHash"`
	From            ethrpc.Address  `json:"from"`
	To              ethrpc.Address  `json:"to"`
	Token           *ethrpc.Address `json:"token"`
	// TokenSymbol is the symbol that the service has for the token, which anyone
	// deploying a token chooses, so it is data.
	TokenSymbol *string `json:"tokenSymbol"`
	Value       *string `json:"value"`
	TokenID     *string `json:"tokenId"`
}

// Transfers returns the transfers into and out of the Safe at address that the
// service indexed in blocks up to toBlock, newest first, or ErrNotIndexed.
func (c *Client) Transfers(ctx context.Context, address ethrpc.Address, toBlock uint64) ([]Transfer, error) {
	transfers := []Transfer{}
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("more than %d transfers", maxPages*pageSize)
		}
		var r struct {
			Count   int `json:"count"`
			Results []struct {
				Type            string          `json:"type"`
				BlockNumber     uint64          `json:"blockNumber"`
				ExecutionDate   time.Time       `json:"executionDate"`
				TransactionHash ethrpc.Hash     `json:"transactionHash"`
				From            ethrpc.Address  `json:"from"`
				To              ethrpc.Address  `json:"to"`
				Value           *string         `json:"value"`
				TokenID         *string         `json:"tokenId"`
				TokenAddress    *ethrpc.Address `json:"tokenAddress"`
				TokenInfo       *struct {
					Symbol string `json:"symbol"`
				} `json:"tokenInfo"`
			} `json:"results"`
		}
		query := url.Values{"limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(page * pageSize)}}
		if err := c.get(ctx, []string{"safes", address.String(), "transfers"}, query, &r); err != nil {
			return nil, err
		}
		for _, t := range r.Results {
			if t.BlockNumber > toBlock {
				continue
			}
			transfer := Transfer{
				Type: t.Type, Block: t.BlockNumber, ExecutionDate: t.ExecutionDate, TransactionHash: t.TransactionHash,
				From: t.From, To: t.To, Token: t.TokenAddress, Value: t.Value, TokenID: t.TokenID,
			}
			if t.TokenInfo != nil {
				transfer.TokenSymbol = &t.TokenInfo.Symbol
			}
			transfers = append(transfers, transfer)
		}
		if len(r.Results) < pageSize || (page+1)*pageSize >= r.Count {
			return transfers, nil
		}
	}
}

// OwnerSafes returns the Safes that the service lists with owner as an owner,
// as of now, not of any past block.
func (c *Client) OwnerSafes(ctx context.Context, owner ethrpc.Address) ([]ethrpc.Address, error) {
	var r struct {
		Safes []ethrpc.Address `json:"safes"`
	}
	if err := c.get(ctx, []string{"owners", owner.String(), "safes"}, nil, &r); err != nil {
		return nil, err
	}
	if r.Safes == nil {
		r.Safes = []ethrpc.Address{}
	}
	return r.Safes, nil
}

// get fetches the JSON document at the path of the API, with the query, and
// decodes it into v. A 404 response is ErrNotIndexed.
func (c *Client) get(ctx context.Context, path []string, query url.Values, v any) error {
	u, err := url.JoinPath(c.baseURL, append([]string{"api", "v1"}, path...)...)
	if err != nil {
		return err
	}
	// The service redirects paths without a trailing slash.
	u += "/"
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
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
		return ErrNotIndexed
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %s: %q", resp.Status, snippet(body))
	case len(body) > maxResponseSize:
		return fmt.Errorf("response exceeds %d bytes", maxResponseSize)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding %s: %w", u, err)
	}
	return nil
}

// BaseURL returns the base URL of the service.
func (c *Client) BaseURL() string {
	return c.baseURL
}
