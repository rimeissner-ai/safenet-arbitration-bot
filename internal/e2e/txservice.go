package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// txService is a Safe Transaction Service for Ethereum Mainnet that serves the
// transfers of a test.
type txService struct {
	*httptest.Server
	mu sync.Mutex
	// transfers are the transfers of each Safe, as the service lists them.
	transfers map[ethrpc.Address][]map[string]any
}

// ServeTransfers lists transfers, as the Safe Transaction Service's transfers
// endpoint lists them, as the transfers of safe on Ethereum Mainnet, from the
// service that Arbot configures arbot to use, starting it if needed. The
// service lists them in one page, as they are, so a test can list transfers
// that the chain doesn't confirm.
func (n *Testnet) ServeTransfers(safe ethrpc.Address, transfers []map[string]any) {
	n.tb.Helper()
	if n.txService == nil {
		s := &txService{transfers: make(map[ethrpc.Address][]map[string]any)}
		s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, _ := strings.CutPrefix(r.URL.Path, "/api/v1/safes/")
			address, rest, _ := strings.Cut(path, "/")
			safe, err := ethrpc.ParseAddress(address)
			s.mu.Lock()
			transfers, ok := s.transfers[safe]
			s.mu.Unlock()
			// The service rejects an address without its checksum.
			if err != nil || rest != "transfers/" || address != safe.String() || !ok {
				http.NotFound(w, r)
				return
			}
			count := len(transfers)
			if r.URL.Query().Get("offset") != "0" {
				transfers = nil
			}
			json.NewEncoder(w).Encode(map[string]any{"count": count, "results": transfers})
		}))
		n.tb.Cleanup(s.Close)
		n.txService = s
	}
	n.txService.mu.Lock()
	defer n.txService.mu.Unlock()
	n.txService.transfers[safe] = transfers
}
