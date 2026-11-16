package txservice

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

var safe = ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")

// serveMux starts a service that answers each path of the API with its handler,
// and returns a client for it.
func serveMux(t *testing.T, routes map[string]http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL+"/tx-service/eth", ethrpc.Mainnet)
}

func TestCreation(t *testing.T) {
	client := serveMux(t, map[string]http.HandlerFunc{
		"/tx-service/eth/api/v1/safes/" + safe.String() + "/creation/": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{
				"created": "2026-11-13T13:52:35Z",
				"creator": "0x1578eD833D986c1188D1a998aA5FEcD418beF5da",
				"transactionHash": "0xb320e86e4b990be115e14b71505cfab69ec7d9ccdbfb6d51355394949555c676",
				"factoryAddress": "0x14F2982D601c9458F93bd70B218933A6f8165e7b",
				"masterCopy": "0xFf51A5898e281Db6DfC7855790607438dF2ca44b",
				"setupData": "0xb63e800d",
				"saltNonce": "0",
				"dataDecoded": {"method": "setup", "parameters": [
					{"name": "_owners", "type": "address[]", "value": ["0x1578eD833D986c1188D1a998aA5FEcD418beF5da"]},
					{"name": "_threshold", "type": "uint256", "value": "1"}
				]}
			}`)
		},
	})
	got, err := client.Creation(t.Context(), safe)
	if err != nil {
		t.Fatalf("Creation: %v", err)
	}
	if got.Creator != ethrpc.MustParseAddress("0x1578eD833D986c1188D1a998aA5FEcD418beF5da") || !got.Created.Equal(time.Date(2026, 11, 13, 13, 52, 35, 0, time.UTC)) {
		t.Errorf("Creation = %+v", got)
	}
	if len(got.Owners) != 1 || got.Owners[0] != got.Creator || got.Threshold == nil || *got.Threshold != "1" {
		t.Errorf("owners %v, threshold %v; want the creator and 1", got.Owners, got.Threshold)
	}

	if _, err := client.Creation(t.Context(), ethrpc.Address{}); !errors.Is(err, ErrNotIndexed) {
		t.Errorf("Creation of an unindexed account: %v, want ErrNotIndexed", err)
	}
}

func TestTransfers(t *testing.T) {
	// Two pages, newest first, the first with a transfer after the last block.
	pages := []string{
		`{"count": 3, "results": [
			{"type": "ETHER_TRANSFER", "blockNumber": 11, "executionDate": "2026-11-14T19:24:11Z", "transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000001", "from": "` + safe.String() + `", "to": "0x0000000000000000000000000000000000000002", "value": "5", "tokenId": null, "tokenAddress": null, "tokenInfo": null},
			{"type": "ETHER_TRANSFER", "blockNumber": 10, "executionDate": "2026-11-14T19:21:35Z", "transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000002", "from": "0x0000000000000000000000000000000000000003", "to": "` + safe.String() + `", "value": "5", "tokenId": null, "tokenAddress": null, "tokenInfo": null}
		]}`,
		`{"count": 3, "results": [
			{"type": "ERC20_TRANSFER", "blockNumber": 9, "executionDate": "2026-11-14T19:17:47Z", "transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000003", "from": "0x0000000000000000000000000000000000000004", "to": "` + safe.String() + `", "value": "5", "tokenId": null, "tokenAddress": "0x0000000000000000000000000000000000000005", "tokenInfo": {"symbol": "WETH"}}
		]}`,
	}
	client := serveMux(t, map[string]http.HandlerFunc{
		"/tx-service/eth/api/v1/safes/" + safe.String() + "/transfers/": func(w http.ResponseWriter, r *http.Request) {
			page := map[string]int{"0": 0, "2": 1}[r.URL.Query().Get("offset")]
			fmt.Fprint(w, pages[page])
		},
	})
	pageSizeForTest(t, 2)
	got, err := client.Transfers(t.Context(), safe, 10)
	if err != nil {
		t.Fatalf("Transfers: %v", err)
	}
	if len(got) != 2 || got[0].Block != 10 || got[1].Block != 9 {
		t.Fatalf("Transfers = %+v, want the transfers in blocks 10 and 9", got)
	}
	if got[0].Token != nil || got[1].Token == nil || got[1].TokenSymbol == nil || *got[1].TokenSymbol != "WETH" {
		t.Errorf("tokens %v and %v, want none and WETH", got[0].Token, got[1].Token)
	}
}

// pageSizeForTest sets the page size for a test.
func pageSizeForTest(t *testing.T, size int) {
	old := pageSize
	pageSize = size
	t.Cleanup(func() { pageSize = old })
}
