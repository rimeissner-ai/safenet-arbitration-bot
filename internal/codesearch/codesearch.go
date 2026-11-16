// Package codesearch searches the public code on GitHub for a term, such as an
// address or a function selector, and checks each file that GitHub finds by
// fetching it, since GitHub's search matches terms loosely.
//
// Projects often list the addresses of their contracts in their code and
// documentation, so a file that has an address hints at whose contract it is.
// The files are an offchain source (Charter § 3.4): anyone can publish one, so
// the repository that has a file matters, and they are as of the search, not of
// a proposal.
package codesearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/httpretry"
)

// DefaultAPI is the base URL of GitHub's REST API, and DefaultRaw that of its
// server of raw file contents.
const (
	DefaultAPI = "https://api.github.com"
	DefaultRaw = "https://raw.githubusercontent.com"
)

// maxResponseSize bounds a search response, and maxFileSize the size of a file
// that the client fetches to check it.
const (
	maxResponseSize = 8 << 20
	maxFileSize     = 4 << 20
)

// maxResults is the most results that GitHub returns in a page.
const maxResults = 100

// Client searches GitHub.
type Client struct {
	http *http.Client
	// API and Raw are the base URLs of the REST API and of the raw contents.
	API, Raw string
	// Token is a GitHub token to authenticate with, or empty. GitHub's code search
	// needs one, unless a proxy adds it.
	Token string
}

// NewClient returns a client for GitHub, with the token if it isn't empty.
func NewClient(token string) *Client {
	return &Client{http: httpretry.Client, API: DefaultAPI, Raw: DefaultRaw, Token: token}
}

// Line is a line of a file that has the term.
type Line struct {
	// Number is the line's number, from 1.
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// File is a file that GitHub found for a term.
type File struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	// Commit is the commit that GitHub found the file in.
	Commit string `json:"commit"`
	URL    string `json:"url"`
	// Contains is whether the file has the term, ignoring case. GitHub's search
	// matches terms loosely, so it finds files that don't.
	Contains bool `json:"contains"`
	// Lines are the lines that have the term, with the lines of context around
	// them, up to a limit. They are empty if the file doesn't have the term.
	Lines []Line `json:"lines"`
	// Error is why the file couldn't be fetched to check it, or empty.
	Error string `json:"error,omitempty"`
}

// Result is the result of a search.
type Result struct {
	Term string `json:"term"`
	// URL is the search that the client sent, and ObservedAt when.
	URL        string    `json:"url"`
	ObservedAt time.Time `json:"observedAt"`
	// Total is the number of files that GitHub reports for the search, of which
	// Files are the first.
	Total int    `json:"total"`
	Files []File `json:"files"`
}

// Search searches GitHub's code for the term, as an exact phrase, and returns
// the first limit files that GitHub finds, up to 100, each checked for the
// term, with around lines of context around each line that has it, up to
// maxLines lines.
func (c *Client) Search(ctx context.Context, term string, limit, around, maxLines int) (*Result, error) {
	if limit < 1 || limit > maxResults {
		return nil, fmt.Errorf("limit %d is not between 1 and %d", limit, maxResults)
	}
	query := url.Values{"q": {strconv.Quote(term)}, "per_page": {strconv.Itoa(limit)}}
	u := strings.TrimSuffix(c.API, "/") + "/search/code?" + query.Encode()
	result := &Result{Term: term, URL: u, ObservedAt: time.Now().UTC().Truncate(time.Second), Files: []File{}}

	body, err := c.get(ctx, u, "application/vnd.github+json", maxResponseSize, true)
	if err != nil {
		return nil, fmt.Errorf("searching GitHub: %w", err)
	}
	var r struct {
		TotalCount int `json:"total_count"`
		Items      []struct {
			Path       string `json:"path"`
			URL        string `json:"url"`
			HTMLURL    string `json:"html_url"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decoding the search: %w", err)
	}
	result.Total = r.TotalCount
	for _, item := range r.Items {
		file := File{Repository: item.Repository.FullName, Path: item.Path, URL: item.HTMLURL, Lines: []Line{}}
		// The API URL of the file names the commit that it was found in.
		if api, err := url.Parse(item.URL); err == nil {
			file.Commit = api.Query().Get("ref")
		}
		if file.Commit == "" {
			file.Error = "no commit in the search result"
		} else {
			raw := strings.TrimSuffix(c.Raw, "/") + "/" + file.Repository + "/" + file.Commit + "/" + escapePath(file.Path)
			content, err := c.get(ctx, raw, "", maxFileSize, false)
			if err != nil {
				file.Error = err.Error()
			} else {
				file.Lines = matches(content, term, around, maxLines)
				file.Contains = len(file.Lines) > 0
			}
		}
		result.Files = append(result.Files, file)
	}
	return result, nil
}

// matches returns the lines of content that have term, ignoring case, with
// around lines of context around each, up to maxLines lines.
func matches(content []byte, term string, around, maxLines int) []Line {
	lines := strings.Split(string(content), "\n")
	lower := strings.ToLower(term)
	keep := make([]bool, len(lines))
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), lower) {
			for j := max(0, i-around); j <= min(len(lines)-1, i+around); j++ {
				keep[j] = true
			}
		}
	}
	found := []Line{}
	for i, line := range lines {
		if keep[i] && len(found) < maxLines {
			found = append(found, Line{Number: i + 1, Text: strings.TrimRight(line, "\r")})
		}
	}
	return found
}

// escapePath escapes each segment of a file's path for a URL.
func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// get fetches the URL, with the token if auth is set, and returns its body if
// the response is 200 OK.
func (c *Client) get(ctx context.Context, u, accept string, limit int64, auth bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if auth && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("HTTP %s: GitHub's code search needs a token (see the githubToken setting)", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %s: %q", resp.Status, snippet(body))
	case int64(len(body)) > limit:
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}

// snippet returns the start of a response body, for an error message.
func snippet(body []byte) string {
	const limit = 200
	if len(body) > limit {
		body = body[:limit]
	}
	return string(bytes.TrimSpace(body))
}
