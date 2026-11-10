package txservice

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// safe is a Safe whose address has letters of both cases in its checksum.
var safe = ethrpc.MustParseAddress("0x7d8bfc367bc1f2ef50c72e1db2720d5916e3e1eb")

// serveTransfers starts a service that lists results as the transfers of safe,
// in pages, and returns a client for it.
func serveTransfers(t *testing.T, results []string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The service rejects an address without its checksum.
		if r.URL.Path != "/api/v1/safes/0x7D8Bfc367bc1F2ef50c72E1DB2720D5916e3E1Eb/transfers/" {
			http.NotFound(w, r)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end := min(offset+limit, len(results))
		page := results[min(offset, end):end]
		fmt.Fprintf(w, `{"count": %d, "next": "https://elsewhere.example/", "results": [%s]}`, len(results), strings.Join(page, ","))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, ethrpc.ArbitrumOne)
}

// erc20Transfer is a transfer of the service's list, with fields that the
// client doesn't use.
func erc20Transfer(block uint64, logIndex int) string {
	hash := ethrpc.Hash{0: 0xaa, 31: byte(block)}
	return fmt.Sprintf(`{
		"type": "ERC20_TRANSFER", "executionDate": "2026-11-09T16:51:34Z", "blockNumber": %d,
		"transactionHash": "%s", "to": "0x5D9EDfb7046B2eAe234ee19b8aC7f71A77305Ac7", "value": "50470714",
		"tokenId": null, "tokenAddress": "0xaf88d065e77c8cC2239327C5EDb3A432268e5831",
		"transferId": "e%s%d", "tokenInfo": {"symbol": "USDC"}, "from": "%s"
	}`, block, hash, strings.TrimPrefix(hash.String(), "0x"), logIndex, safe)
}

func TestTransfers(t *testing.T) {
	// More than a page of transfers, which the client reads with offsets rather
	// than the service's links.
	var results []string
	for i := range transfersPageSize + 50 {
		results = append(results, erc20Transfer(uint64(200-i), i))
	}
	got, err := serveTransfers(t, results).Transfers(t.Context(), safe)
	if err != nil {
		t.Fatalf("Transfers: %v", err)
	}
	if len(got) != len(results) {
		t.Fatalf("Transfers: got %d transfers, want %d", len(got), len(results))
	}
	token := ethrpc.MustParseAddress("0xaf88d065e77c8cC2239327C5EDb3A432268e5831")
	index := uint64(120)
	want := Transfer{
		Type: ERC20Transfer, Block: 80, TxHash: ethrpc.Hash{0: 0xaa, 31: 80}, LogIndex: &index, Token: &token,
		From: safe, To: ethrpc.MustParseAddress("0x5D9EDfb7046B2eAe234ee19b8aC7f71A77305Ac7"), Value: big.NewInt(50470714),
	}
	if !reflect.DeepEqual(got[120], want) {
		t.Errorf("Transfers: got %+v, want %+v", got[120], want)
	}
}

func TestTransfersOfOtherTypes(t *testing.T) {
	hash := "8d6c96160c1e97e34ffe0c5f94c333e65f92ac8ef44ffc145aa4627ff0ddf4c9"
	native := `{"type": "ETHER_TRANSFER", "blockNumber": 5, "transactionHash": "0x` + hash + `",
		"to": "0x7D8Bfc367bc1F2ef50c72E1DB2720D5916e3E1Eb", "value": "2986094567188386", "tokenId": null,
		"tokenAddress": null, "transferId": "i` + hash + `8", "from": "0x8C826F795466E39acbfF1BB4eEeB759609377ba1"}`
	nft := `{"type": "ERC721_TRANSFER", "blockNumber": 6, "transactionHash": "0x` + hash + `",
		"to": "0x7D8Bfc367bc1F2ef50c72E1DB2720D5916e3E1Eb", "value": null, "tokenId": "7",
		"tokenAddress": "0x0000000000000000000000000000000000000721", "transferId": "e` + hash + `3",
		"from": "0x8C826F795466E39acbfF1BB4eEeB759609377ba1"}`
	got, err := serveTransfers(t, []string{native, nft}).Transfers(t.Context(), safe)
	if err != nil {
		t.Fatalf("Transfers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Transfers: got %d transfers, want 2", len(got))
	}
	if got[0].Type != EtherTransfer || got[0].LogIndex != nil || got[0].Token != nil || got[0].Value.String() != "2986094567188386" {
		t.Errorf("Transfers: got native transfer %+v", got[0])
	}
	if got[1].Type != ERC721Transfer || *got[1].LogIndex != 3 || got[1].Value != nil || got[1].TokenID.Int64() != 7 {
		t.Errorf("Transfers: got ERC-721 transfer %+v", got[1])
	}
}

func TestTransfersInvalid(t *testing.T) {
	valid := erc20Transfer(1, 2)
	tests := []struct {
		name, transfer string
	}{
		{"ID of another transaction", strings.Replace(valid, `"transferId": "eaa`, `"transferId": "ebb`, 1)},
		{"ID without a log index", strings.Replace(valid, `012"`, `01"`, 1)},
		{"ID of another kind", strings.Replace(valid, `"transferId": "e`, `"transferId": "x`, 1)},
		{"unknown type", strings.Replace(valid, "ERC20_TRANSFER", "ERC1155_TRANSFER", 1)},
		{"ERC-20 transfer without a value", strings.Replace(valid, `"value": "50470714"`, `"value": null`, 1)},
		{"negative value", strings.Replace(valid, `"value": "50470714"`, `"value": "-1"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.transfer == valid {
				t.Fatal("test transfer is the valid one")
			}
			if _, err := serveTransfers(t, []string{test.transfer}).Transfers(t.Context(), safe); err == nil {
				t.Errorf("Transfers: got no error")
			}
		})
	}
}

func TestTransfersTooMany(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"count": maxTransfers + 1, "results": []any{}})
	}))
	t.Cleanup(server.Close)
	if _, err := NewClient(server.URL, ethrpc.ArbitrumOne).Transfers(t.Context(), safe); err == nil {
		t.Errorf("Transfers: got no error for a Safe with more than %d transfers", maxTransfers)
	}
}
