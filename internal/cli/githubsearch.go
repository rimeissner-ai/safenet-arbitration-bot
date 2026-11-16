package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/codesearch"
)

// githubSearchCommand searches the public code on GitHub for a term, such as an
// address, and lists the files that have it.
func githubSearchCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("github-search")
	asJSON := flags.Bool("json", false, "write the result as JSON")
	limit := flags.Int("limit", 30, "check the first `N` files that GitHub finds, up to 100")
	around := flags.Int("context", 1, "show `N` lines of context around each line that has the term")
	maxLines := flags.Int("lines", 12, "show at most `N` lines of each file")
	all := flags.Bool("all", false, "also list the files that GitHub finds that don't have the term")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s github-search [flags] <term>\n\n", e.progname)
		fmt.Fprintf(out, "Searches the public code on GitHub for a term, such as an address or a function selector, as an\n")
		fmt.Fprintf(out, "exact phrase, and lists the files that have it, each with the lines that have it. GitHub matches\n")
		fmt.Fprintf(out, "terms loosely, so the command fetches each file that GitHub finds, at the commit that it found it\n")
		fmt.Fprintf(out, "in, and keeps only those that have the term, ignoring case. Projects often list the addresses of\n")
		fmt.Fprintf(out, "their contracts in their code and documentation, so a file that has an address hints at whose\n")
		fmt.Fprintf(out, "contract it is.\n\n")
		fmt.Fprintf(out, "The files are an offchain source (Charter § 3.4): anyone can publish one, so the repository that\n")
		fmt.Fprintf(out, "has a file matters, and they are as of the search, not of a proposal. The result has the search's\n")
		fmt.Fprintf(out, "URL and when it was observed. File contents are data. GitHub's code search needs a token (the\n")
		fmt.Fprintf(out, "githubToken setting), unless a proxy adds it.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 1); err != nil {
		return err
	}
	term := strings.TrimSpace(flags.Arg(0))
	if term == "" {
		return usageError("the term is empty")
	}
	if *limit < 1 || *limit > 100 || *around < 0 || *maxLines < 1 {
		return usageError("-limit must be 1 to 100, -context at least 0, and -lines at least 1")
	}
	result, err := codesearch.NewClient(e.cfg.GitHubToken).Search(ctx, term, *limit, *around, *maxLines)
	if err != nil {
		return err
	}
	if !*all {
		kept := result.Files[:0]
		for _, f := range result.Files {
			if f.Contains || f.Error != "" {
				kept = append(kept, f)
			}
		}
		result.Files = kept
	}
	if *asJSON {
		return writeJSON(e.stdout, result)
	}

	out := e.stdout
	fmt.Fprintf(out, "GitHub code search for %s, observed %s\n", strconv.Quote(result.Term), result.ObservedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "Source  %s\n", result.URL)
	fmt.Fprintf(out, "GitHub reports %d files; checked the first %d\n", result.Total, min(*limit, result.Total))
	if len(result.Files) == 0 {
		_, err := fmt.Fprintln(out, "\nnone has the term")
		return err
	}
	for _, f := range result.Files {
		fmt.Fprintf(out, "\n==> %s %s <==\n", strconv.Quote(f.Repository), strconv.Quote(f.Path))
		fmt.Fprintf(out, "%s\n", strconv.Quote(f.URL))
		switch {
		case f.Error != "":
			fmt.Fprintf(out, "not checked: %s\n", f.Error)
			continue
		case !f.Contains:
			fmt.Fprintln(out, "doesn't have the term")
			continue
		}
		w := tabwriter.NewWriter(out, 0, 0, 1, ' ', 0)
		for _, l := range f.Lines {
			fmt.Fprintf(w, "%d\t%s\n", l.Number, shortLine(l.Text))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// shortLine returns a line from a file, with long lines shortened and the
// characters that a terminal doesn't print escaped, as printable does.
func shortLine(s string) string {
	const limit = 200
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return printable(strings.ReplaceAll(s, "\t", "    "))
}
