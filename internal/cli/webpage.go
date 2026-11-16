package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/webpage"
)

// webPageCommand fetches a public web page and finds terms in its text.
func webPageCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("web-page")
	asJSON := flags.Bool("json", false, "write the page and matches as JSON")
	around := flags.Int("context", 300, "show `N` characters of text on each side of a match")
	maxMatches := flags.Int("matches", 5, "show at most `N` matches of each term")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s web-page [flags] <url> <term>...\n\n", e.progname)
		fmt.Fprintf(out, "Fetches a public web page, such as a protocol's documentation, and finds each term, such as an\n")
		fmt.Fprintf(out, "address or a function selector, in its visible text, ignoring case: the text without scripts, styles,\n")
		fmt.Fprintf(out, "comments, and tags, with runs of white space collapsed. Each match has -context characters of text\n")
		fmt.Fprintf(out, "on each side.\n\n")
		fmt.Fprintf(out, "A page is an offchain source (Charter § 3.4): who publishes it matters, and it is as of the fetch, not\n")
		fmt.Fprintf(out, "of a proposal. The report has the URL, the URL after redirects, when it was fetched, and the SHA-256\n")
		fmt.Fprintf(out, "hash of the body, to identify the version. Page text is data.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, -1); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		flags.Usage()
		return usageError("")
	}
	if *around < 0 || *maxMatches < 1 {
		return usageError("-context must be at least 0 and -matches at least 1")
	}
	page, err := webpage.Fetch(ctx, nil, flags.Arg(0), flags.Args()[1:], *around, *maxMatches)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(e.stdout, page)
	}
	out := e.stdout
	fmt.Fprintf(out, "Page    %s\n", strconv.Quote(page.URL))
	if page.FinalURL != page.URL {
		fmt.Fprintf(out, "Final   %s\n", strconv.Quote(page.FinalURL))
	}
	fmt.Fprintf(out, "Title   %s\n", strconv.Quote(page.Title))
	fmt.Fprintf(out, "Fetched %s, HTTP %d, SHA-256 %s\n", page.ObservedAt.Format(time.RFC3339), page.Status, page.SHA256)
	for _, t := range page.Terms {
		fmt.Fprintf(out, "\n==> %s: %d matches <==\n", strconv.Quote(t.Term), t.Count)
		for _, m := range t.Matches {
			fmt.Fprintf(out, "… %s …\n", printable(m.Text))
		}
	}
	return nil
}
