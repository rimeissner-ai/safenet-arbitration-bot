package txservice

import (
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

var hash = ethrpc.Hash{0: 0xaa, 31: 1}

// serve starts a service that answers requests for hash with status and body,
// and returns a client for it.
func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The path is versioned, and has a trailing slash like the service wants.
		if r.URL.Path != "/tx-service/eth/api/v1/multisig-transactions/"+hash.String()+"/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL+"/tx-service/eth", ethrpc.Mainnet)
}

func TestTransaction(t *testing.T) {
	// A response of the service, whose numbers are strings or numbers, with fields
	// that the client doesn't use.
	client := serve(t, http.StatusOK, `{
		"safe": "0x5afE000000000000000000000000000000000001",
		"to": "0xa11ce00000000000000000000000000000000003",
		"value": "1000000000000000000000000000000",
		"data": "0x1234",
		"operation": 1,
		"safeTxGas": 3,
		"baseGas": 4,
		"gasPrice": "5",
		"gasToken": "0x0000000000000000000000000000000000000006",
		"refundReceiver": "0x0000000000000000000000000000000000000007",
		"nonce": 8,
		"executionDate": "2026-09-28T09:24:11Z",
		"dataDecoded": {"method": "transfer"}
	}`)
	got, err := client.Transaction(t.Context(), hash)
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	value, _ := new(big.Int).SetString("1000000000000000000000000000000", 10)
	want := &safenet.SafeTransaction{
		ChainID:        big.NewInt(1),
		Safe:           ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001"),
		To:             ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000003"),
		Value:          value,
		Data:           []byte{0x12, 0x34},
		Operation:      safenet.OperationDelegateCall,
		SafeTxGas:      big.NewInt(3),
		BaseGas:        big.NewInt(4),
		GasPrice:       big.NewInt(5),
		GasToken:       ethrpc.Address{19: 6},
		RefundReceiver: ethrpc.Address{19: 7},
		Nonce:          big.NewInt(8),
	}
	if g, w := got.Hash(), want.Hash(); g != w {
		t.Errorf("Transaction: got %+v with hash %s, want %+v with hash %s", got, g, want, w)
	}
}

func TestTransactionWithoutData(t *testing.T) {
	client := serve(t, http.StatusOK, `{
		"safe": "0x5afE000000000000000000000000000000000001",
		"to": "0xa11ce00000000000000000000000000000000003",
		"value": "0", "data": null, "operation": 0, "safeTxGas": 0, "baseGas": 0, "gasPrice": "0",
		"gasToken": "0x0000000000000000000000000000000000000000",
		"refundReceiver": "0x0000000000000000000000000000000000000000",
		"nonce": 0
	}`)
	got, err := client.Transaction(t.Context(), hash)
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if len(got.Data) != 0 {
		t.Errorf("Transaction: got data %x, want none", got.Data)
	}
}

func TestTransactionErrors(t *testing.T) {
	if _, err := serve(t, http.StatusNotFound, `{"detail": "Not found."}`).Transaction(t.Context(), hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("Transaction of an unknown hash: got error %v, want ErrNotFound", err)
	}

	// The body of an error is quoted, as it isn't trusted.
	_, err := serve(t, http.StatusInternalServerError, "\x1b[31munavailable").Transaction(t.Context(), hash)
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "\x1b") {
		t.Errorf("Transaction of an unavailable service: got error %q, want one with the status and no escape sequence", err)
	}

	for name, body := range map[string]string{
		"not JSON":        `<html>`,
		"invalid address": `{"safe": "0x12", "value": "0"}`,
		"negative value":  `{"value": "-1"}`,
		"invalid number":  `{"value": "one"}`,
	} {
		if _, err := serve(t, http.StatusOK, body).Transaction(t.Context(), hash); err == nil {
			t.Errorf("Transaction(%s): expected an error", name)
		}
	}

	big := serve(t, http.StatusOK, `{"data": "0x`+strings.Repeat("00", maxResponseSize)+`"}`)
	if _, err := big.Transaction(t.Context(), hash); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("Transaction with a large response: got error %v, want one about its size", err)
	}
}

func TestDefaultURLs(t *testing.T) {
	for _, chainID := range []uint64{ethrpc.Mainnet, ethrpc.ArbitrumOne, ethrpc.Gnosis} {
		if !strings.HasPrefix(DefaultURLs[chainID], "https://") {
			t.Errorf("DefaultURLs[%d]: got %q, want an HTTPS URL", chainID, DefaultURLs[chainID])
		}
	}
}

func TestExecuted(t *testing.T) {
	safe := ethrpc.MustParseAddress("0x5afe00000000000000000000000000000000000a")
	var server *httptest.Server
	var foreignNext bool
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The address has its checksum, and the path a trailing slash.
		if r.URL.Path != "/tx-service/eth/api/v1/safes/"+safe.String()+"/multisig-transactions/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("executed") != "true" || q.Get("nonce__lt") != "3" || q.Get("ordering") != "nonce" {
			http.Error(w, "unexpected query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		if q.Get("offset") == "" {
			next := server.URL + r.URL.Path + "?" + r.URL.RawQuery + "&offset=1"
			if foreignNext {
				next = "https://example.com/elsewhere"
			}
			w.Write([]byte(`{"next": "` + next + `", "results": [
				{"safe": "` + safe.String() + `", "to": "0x0000000000000000000000000000000000000001", "value": "0", "data": null,
				 "operation": 0, "safeTxGas": 0, "baseGas": 0, "gasPrice": "0", "gasToken": "0x0000000000000000000000000000000000000000",
				 "refundReceiver": "0x0000000000000000000000000000000000000000", "nonce": 0,
				 "safeTxHash": "` + hash.String() + `", "transactionHash": "` + hash.String() + `"}]}`))
			return
		}
		w.Write([]byte(`{"next": null, "results": [
			{"safe": "` + safe.String() + `", "to": "0x0000000000000000000000000000000000000002", "value": "5", "data": "0x12",
			 "operation": 1, "safeTxGas": 0, "baseGas": 0, "gasPrice": "0", "gasToken": "0x0000000000000000000000000000000000000000",
			 "refundReceiver": "0x0000000000000000000000000000000000000000", "nonce": "1",
			 "safeTxHash": "` + hash.String() + `", "transactionHash": null}]}`))
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL+"/tx-service/eth", ethrpc.Mainnet)

	got, err := client.Executed(t.Context(), safe, big.NewInt(3))
	if err != nil {
		t.Fatalf("Executed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Executed: got %d transactions, want 2 from two pages", len(got))
	}
	if got[0].TxHash == nil || *got[0].TxHash != hash || got[0].SafeTxHash != hash || got[0].Transaction.Nonce.Int64() != 0 {
		t.Errorf("Executed: got first %+v, want nonce 0 with its hashes", got[0])
	}
	second := got[1].Transaction
	if got[1].TxHash != nil || second.Nonce.Int64() != 1 || second.Value.Int64() != 5 || second.Operation != safenet.OperationDelegateCall || string(second.Data) != "\x12" {
		t.Errorf("Executed: got second %+v with transaction %+v, want nonce 1 without an onchain transaction", got[1], second)
	}

	// The service isn't followed to another host.
	foreignNext = true
	if _, err := client.Executed(t.Context(), safe, big.NewInt(3)); err == nil || !strings.Contains(err.Error(), "not on the service") {
		t.Errorf("Executed with a next page elsewhere: got error %v, want one about the page", err)
	}
}
