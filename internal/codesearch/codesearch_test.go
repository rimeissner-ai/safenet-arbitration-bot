package codesearch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const term = "0x3B4D794a66304F130a4Db8F2551B0070dfCf5ca7"

func TestSearch(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/code":
			if got := r.URL.Query().Get("q"); got != `"`+term+`"` {
				t.Errorf("query %q, want the term quoted", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer secret" {
				t.Errorf("Authorization %q, want the token", got)
			}
			fmt.Fprintf(w, `{"total_count": 3, "items": [
				{"path": "a.md", "url": "%[1]s/repos/o/r/contents/a.md?ref=c1", "html_url": "https://github.com/o/r/blob/c1/a.md", "repository": {"full_name": "o/r"}},
				{"path": "dir/b c.ts", "url": "%[1]s/repos/o/s/contents/dir/b%%20c.ts?ref=c2", "html_url": "https://github.com/o/s/blob/c2/dir/b%%20c.ts", "repository": {"full_name": "o/s"}},
				{"path": "gone.md", "url": "%[1]s/repos/o/r/contents/gone.md?ref=c1", "html_url": "https://github.com/o/r/blob/c1/gone.md", "repository": {"full_name": "o/r"}}
			]}`, server.URL)
		case "/raw/o/r/c1/a.md":
			if r.Header.Get("Authorization") != "" {
				t.Errorf("raw request has a token")
			}
			fmt.Fprint(w, "one\ntwo\nLighter: "+strings.ToLower(term)+"\nfour\nfive\n")
		case "/raw/o/s/c2/dir/b c.ts":
			fmt.Fprint(w, "no address here\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := &Client{http: server.Client(), API: server.URL, Raw: server.URL + "/raw", Token: "secret"}

	got, err := client.Search(t.Context(), term, 10, 1, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Total != 3 || len(got.Files) != 3 {
		t.Fatalf("Search = %+v, want 3 files", got)
	}
	a, b, gone := got.Files[0], got.Files[1], got.Files[2]
	if !a.Contains || a.Commit != "c1" || len(a.Lines) != 3 || a.Lines[0].Number != 2 || a.Lines[1].Text != "Lighter: "+strings.ToLower(term) {
		t.Errorf("first file %+v, want lines 2 to 4", a)
	}
	if b.Contains || b.Error != "" || len(b.Lines) != 0 {
		t.Errorf("second file %+v, want it checked without the term", b)
	}
	if gone.Contains || gone.Error == "" {
		t.Errorf("third file %+v, want an error", gone)
	}

	if _, err := client.Search(t.Context(), term, 101, 1, 10); err == nil {
		t.Errorf("Search with limit 101 succeeded")
	}
}

func TestMatchesLimit(t *testing.T) {
	content := []byte(strings.Repeat("x "+term+"\n", 10))
	if got := matches(content, term, 0, 4); len(got) != 4 {
		t.Errorf("matches = %d lines, want 4", len(got))
	}
}
