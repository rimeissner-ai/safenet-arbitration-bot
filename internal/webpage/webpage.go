// Package webpage fetches a public web page and finds terms, such as an address
// or a function selector, in its visible text.
//
// A page is an offchain source (Charter § 3.4). The result has the page's URL,
// when it was fetched, and the SHA-256 hash of its body, so that the Council
// can identify the version that it relies on. A page is as of its fetch, not of
// a proposal.
package webpage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
)

// maxPageSize bounds the size of a page.
const maxPageSize = 16 << 20

// Match is a place where a term appears in the page's text, with the text
// around it.
type Match struct {
	Text string `json:"text"`
}

// Term is a term and where it appears.
type Term struct {
	Term string `json:"term"`
	// Count is the number of times that the term appears in the text, ignoring
	// case, of which Matches are the first.
	Count   int     `json:"count"`
	Matches []Match `json:"matches"`
}

// Page is a fetched page and the terms found in it.
type Page struct {
	URL string `json:"url"`
	// FinalURL is the URL after redirects.
	FinalURL   string    `json:"finalUrl"`
	ObservedAt time.Time `json:"observedAt"`
	Status     int       `json:"status"`
	// SHA256 is the hash of the page's body, as fetched.
	SHA256 string `json:"sha256"`
	Title  string `json:"title"`
	Terms  []Term `json:"terms"`
}

// Fetch fetches the page at the http or https URL and finds each term in its
// visible text, ignoring case, with around characters of text on each side, up
// to maxMatches matches per term.
func Fetch(ctx context.Context, client *http.Client, rawURL string, terms []string, around, maxMatches int) (*Page, error) {
	if client == nil {
		client = httpretry.Client
	}
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return nil, fmt.Errorf("URL %q is not http or https", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.5")
	page := &Page{URL: rawURL, ObservedAt: time.Now().UTC().Truncate(time.Second), Terms: []Term{}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPageSize {
		return nil, fmt.Errorf("page exceeds %d bytes", maxPageSize)
	}
	page.Status, page.FinalURL = resp.StatusCode, resp.Request.URL.String()
	sum := sha256.Sum256(body)
	page.SHA256 = hex.EncodeToString(sum[:])
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s from %s", resp.Status, page.FinalURL)
	}

	text := string(body)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") || strings.Contains(strings.ToLower(text[:min(len(text), 512)]), "<html") {
		page.Title, text = visibleText(text)
	}
	for _, term := range terms {
		page.Terms = append(page.Terms, find(text, term, around, maxMatches))
	}
	return page, nil
}

var (
	hidden  = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg)\b[^>]*>.*?</(script|style|noscript|template|svg)\s*>`)
	comment = regexp.MustCompile(`(?s)<!--.*?-->`)
	title   = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	tag     = regexp.MustCompile(`(?s)<[^>]*>`)
	space   = regexp.MustCompile(`\s+`)
)

// visibleText returns the title and the text of an HTML page, without its
// scripts, styles, comments, and tags, with runs of white space collapsed.
func visibleText(page string) (string, string) {
	var t string
	if m := title.FindStringSubmatch(page); m != nil {
		t = strings.TrimSpace(space.ReplaceAllString(html.UnescapeString(tag.ReplaceAllString(m[1], " ")), " "))
	}
	page = hidden.ReplaceAllString(page, " ")
	page = comment.ReplaceAllString(page, " ")
	page = tag.ReplaceAllString(page, " ")
	return t, strings.TrimSpace(space.ReplaceAllString(html.UnescapeString(page), " "))
}

// find returns where term appears in text, ignoring case.
func find(text, term string, around, maxMatches int) Term {
	result := Term{Term: term, Matches: []Match{}}
	lower, needle := strings.ToLower(text), strings.ToLower(term)
	if needle == "" || len(lower) != len(text) {
		// Lowercasing changed the length, so offsets don't carry over: fall back to a
		// case-sensitive search.
		lower, needle = text, term
	}
	for start := 0; ; {
		i := strings.Index(lower[start:], needle)
		if i < 0 || needle == "" {
			return result
		}
		i += start
		result.Count++
		if len(result.Matches) < maxMatches {
			from, to := max(0, i-around), min(len(text), i+len(needle)+around)
			// Keep to whole UTF-8 characters.
			for from > 0 && !utf8Start(text[from]) {
				from--
			}
			for to < len(text) && !utf8Start(text[to]) {
				to++
			}
			result.Matches = append(result.Matches, Match{Text: text[from:to]})
		}
		start = i + len(needle)
	}
}

// utf8Start reports whether b starts a UTF-8 character.
func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}
