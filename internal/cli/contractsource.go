package cli

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/sourcify"
)

// contractSourceReport is the JSON output of `arbot contract-source`.
type contractSourceReport struct {
	ChainID   uint64 `json:"chainId"`
	SafeBlock uint64 `json:"safeBlock"`
	*sourcify.Lookup
	// Files are the contract's source files: none for an account without code, and
	// nil for a contract that isn't verified, whose sources are unknown.
	Files []sourcify.File `json:"files"`
}

// contractSourceCommand prints the source code of a contract on the Safe's
// chain of a request.
func contractSourceCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("contract-source")
	asJSON := flags.Bool("json", false, "write the source files as JSON")
	requestFile := flags.String("request-file", "", "read the Safe's chain and block from the request in the JSON file at `path`, as written by arbot info -json")
	only := flags.String("file", "", "print only the source file with the path `file` in the compilation, such as contracts/Safe.sol")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s contract-source [flags] -request-file <path> <address>\n\n", e.progname)
		fmt.Fprintf(out, "Prints the source files of the contract at an address on the Safe's chain of a request, which\n")
		fmt.Fprintf(out, "Sourcify verified, an offchain source (Charter § 3.4), by compiling them into the contract's code. It\n")
		fmt.Fprintf(out, "reports the URL that it fetched them from and when, how well Sourcify's compilation matched the code,\n")
		fmt.Fprintf(out, "and the contract, by its fully qualified name, and the compiler. It fails unless the code that\n")
		fmt.Fprintf(out, "Sourcify verified is the account's code as of the request's Safe block. The status is \"verified\",\n")
		fmt.Fprintf(out, "\"unverified\" for a contract that Sourcify hasn't verified, whose sources are unknown, or \"no-code\"\n")
		fmt.Fprintf(out, "for an account without code at the block, which has none.\n\n")
		fmt.Fprintf(out, "A proxy's sources are only the proxy's own, and its implementation, such as a Safe's singleton,\n")
		fmt.Fprintf(out, "which arbot safe shows, has sources of its own. The text output prints each file after a\n")
		fmt.Fprintf(out, "\"==> <path> <==\" line, with the characters that a terminal doesn't print, such as escape sequences\n")
		fmt.Fprintf(out, "and bidirectional text controls, escaped as in a Go string; -json has them as they are.\n\n")
		fmt.Fprintf(out, "Flags must come before the address.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 1); err != nil {
		return err
	}
	address, err := ethrpc.ParseAddress(flags.Arg(0))
	if err != nil {
		return usageError(fmt.Sprintf("address %q: %v", flags.Arg(0), err))
	}
	safe, err := openSafe(ctx, e, *requestFile)
	if err != nil {
		return err
	}

	code, err := safe.eth.GetCode(ctx, address, ethrpc.BlockNumber(safe.SafeBlock))
	if err != nil {
		return err
	}
	repo := sourcify.NewClient(cmp.Or(e.cfg.Sourcify, sourcify.DefaultURL))
	lookup, contract, err := sourcify.Fetch(ctx, repo, safe.ChainID, address, code, sourcify.Sources)
	if err != nil {
		return err
	}
	report := contractSourceReport{ChainID: safe.ChainID, SafeBlock: safe.SafeBlock, Lookup: lookup}
	switch lookup.Status {
	case sourcify.NoCode:
		report.Files = []sourcify.File{}
	case sourcify.Verified:
		report.Files = []sourcify.File{}
		for _, f := range contract.Sources {
			if *only == "" || f.Path == *only {
				report.Files = append(report.Files, f)
			}
		}
		if *only != "" && len(report.Files) == 0 {
			return fmt.Errorf("contract %s has no source file %q", address, *only)
		}
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	writeLookup(e, safe.ChainID, safe.SafeBlock, lookup)
	for _, f := range report.Files {
		fmt.Fprintf(e.stdout, "\n==> %s <==\n%s", printable(f.Path), printable(f.Content))
		if !strings.HasSuffix(f.Content, "\n") {
			fmt.Fprintln(e.stdout)
		}
	}
	return nil
}

// writeLookup prints the lookup of a contract in Sourcify.
func writeLookup(e *env, chainID, safeBlock uint64, lookup *sourcify.Lookup) {
	fmt.Fprintf(e.stdout, "Contract %s on chain %d, as of block %d\n", lookup.Address, chainID, safeBlock)
	fmt.Fprintf(e.stdout, "  Code      %s (%d bytes)\n", lookup.CodeHash, lookup.CodeSize)
	fmt.Fprintf(e.stdout, "  Status    %s\n", lookup.Status)
	if s := lookup.Source; s != nil {
		fmt.Fprintf(e.stdout, "  Source    %s, observed %s\n", s.URL, s.ObservedAt.Format(time.RFC3339))
		if s.VerifiedAt != nil {
			fmt.Fprintf(e.stdout, "  Contract  %q, compiled with %q\n", s.Contract, s.Compiler)
			fmt.Fprintf(e.stdout, "  Match     %s, runtime %s, verified %s\n", s.Match, cmp.Or(s.RuntimeMatch, "no match"), s.VerifiedAt.Format(time.RFC3339))
		}
	}
}

// printable returns text with the characters that a terminal doesn't print,
// other than newlines and tabs, escaped as in a Go string, so that text from an
// untrusted source can't control the terminal or hide what it says.
func printable(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		quoted := strconv.QuoteRune(r)
		b.WriteString(quoted[1 : len(quoted)-1])
	}
	return b.String()
}
