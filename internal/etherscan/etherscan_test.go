package etherscan

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

var contract = ethrpc.MustParseAddress("0x3B4D794a66304F130a4Db8F2551B0070dfCf5ca7")

// serve starts an API that answers with body, and returns a client for it.
func serve(t *testing.T, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != "secret" || q.Get("action") != "getsourcecode" || q.Get("chainid") != "1" || q.Get("address") != contract.String() {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return &Client{http: server.Client(), API: server.URL, key: "secret"}
}

func TestContract(t *testing.T) {
	abi := `[{"type":"function","name":"deposit","stateMutability":"payable","inputs":[{"name":"_to","type":"address"},{"name":"_assetIndex","type":"uint16"},{"name":"_routeType","type":"uint8"},{"name":"_amount","type":"uint256"}]},{"type":"receive","stateMutability":"payable"}]`
	body := fmt.Sprintf(`{"status":"1","message":"OK","result":[{"ABI":%q,"ContractName":"ZkLighter","CompilerVersion":"v0.8.25","Proxy":"1","Implementation":"0x9b4FDAE9f4490e3bBdC1eAF3C06b48172AC0f94f"}]}`, abi)
	got, err := serve(t, body).Contract(t.Context(), 1, contract)
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if strings.Contains(got.URL, "secret") {
		t.Errorf("URL %q has the key", got.URL)
	}
	if !got.Verified || got.Name != "ZkLighter" || !got.Proxy || got.Implementation == nil || got.Receive == nil {
		t.Errorf("Contract = %+v", got)
	}
	if len(got.Functions) != 1 {
		t.Fatalf("functions = %+v, want deposit", got.Functions)
	}
	f := got.Functions[0]
	if f.Selector.String() != "0x8a857083" || f.Signature != "deposit(address,uint16,uint8,uint256)" || f.StateMutability != "payable" {
		t.Errorf("function = %+v, want payable deposit with selector 0x8a857083", f)
	}
	if len(f.Inputs) != 4 || f.Inputs[0] != (Param{Name: "_to", Type: "address"}) {
		t.Errorf("inputs = %+v", f.Inputs)
	}
}

func TestUnverified(t *testing.T) {
	got, err := serve(t, `{"status":"1","message":"OK","result":[{"ABI":"Contract source code not verified","ContractName":""}]}`).Contract(t.Context(), 1, contract)
	if err != nil || got.Verified {
		t.Errorf("Contract = %+v, %v; want unverified", got, err)
	}
}

func TestNoKey(t *testing.T) {
	if _, err := NewClient("").Contract(t.Context(), 1, contract); err != ErrNoKey {
		t.Errorf("Contract without a key: %v, want ErrNoKey", err)
	}
}
