package webpage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Environments &amp; contracts</title><script>var x = "0x3B4D in a script";</script></head>
<body><h2>Ethereum contracts</h2><table><tr><td>ZkLighter</td><td><code>0x3B4D794a66304F130a4Db8F2551B0070dfCf5ca7</code></td></tr></table>
<p>Use the <b>deposit</b> method (0x8a857083).</p><!-- 0x3b4d in a comment --></body></html>`)
	}))
	t.Cleanup(server.Close)

	page, err := Fetch(t.Context(), server.Client(), server.URL+"/docs", []string{"0x3b4d794a66304f130a4db8f2551b0070dfcf5ca7", "0x8a857083", "missing"}, 20, 5)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if page.Title != "Environments & contracts" || page.Status != 200 || len(page.SHA256) != 64 {
		t.Errorf("page = %+v", page)
	}
	address := page.Terms[0]
	if address.Count != 1 || !strings.Contains(address.Matches[0].Text, "ZkLighter 0x3B4D794a66304F130a4Db8F2551B0070dfCf5ca7") {
		t.Errorf("address = %+v, want one match in the table, none in the script or comment", address)
	}
	if page.Terms[1].Count != 1 || page.Terms[2].Count != 0 || len(page.Terms[2].Matches) != 0 {
		t.Errorf("terms = %+v", page.Terms)
	}

	if _, err := Fetch(t.Context(), server.Client(), server.URL+"/gone", nil, 20, 5); err == nil {
		t.Errorf("Fetch of a missing page succeeded")
	}
	if _, err := Fetch(t.Context(), server.Client(), "file:///etc/passwd", nil, 20, 5); err == nil {
		t.Errorf("Fetch of a file URL succeeded")
	}
}
