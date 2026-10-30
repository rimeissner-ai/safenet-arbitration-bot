package cli

import (
	"cmp"
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/signatures"
	"github.com/safe-research/safenet-arbitration-bot/internal/sourcify"
)

// signaturesReport is the JSON output of `arbot signatures`.
type signaturesReport struct {
	ChainID   uint64 `json:"chainId"`
	SafeBlock uint64 `json:"safeBlock"`
	*signatures.Report
}

// signaturesCommand lists the functions of a contract on the Safe's chain of a
// request.
func signaturesCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("signatures")
	asJSON := flags.Bool("json", false, "write the functions as JSON")
	requestFile := flags.String("request-file", "", "read the Safe's chain and block from the request in the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s signatures [flags] -request-file <path> <address>\n\n", e.progname)
		fmt.Fprintf(out, "Lists the functions of the contract at an address on the Safe's chain of a request, with their\n")
		fmt.Fprintf(out, "signatures, selectors, and state mutability, and whether it has a fallback or receive function, from\n")
		fmt.Fprintf(out, "the contract's ABI that Sourcify verified, an offchain source (Charter § 3.4). It reports the URL that\n")
		fmt.Fprintf(out, "it fetched the ABI from and when, how well Sourcify's compilation matched the code, and the contract\n")
		fmt.Fprintf(out, "and compiler. It fails unless the code that Sourcify verified is the account's code as of the\n")
		fmt.Fprintf(out, "request's Safe block. The status is \"verified\", \"unverified\" for a contract that Sourcify hasn't\n")
		fmt.Fprintf(out, "verified, whose functions are unknown, or \"no-code\" for an account without code at the block, which\n")
		fmt.Fprintf(out, "has no functions.\n\n")
		fmt.Fprintf(out, "In a contract compiled from Solidity, a call whose data starts with no function's selector runs the\n")
		fmt.Fprintf(out, "fallback function, and a call with no data runs the receive function, or else the fallback function.\n")
		fmt.Fprintf(out, "Without the function that it runs, the call reverts. A proxy's ABI has only the proxy's own\n")
		fmt.Fprintf(out, "functions, and its fallback function delegates the other calls to its implementation, such as a\n")
		fmt.Fprintf(out, "Safe's singleton, which arbot safe shows.\n\n")
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
	report, err := signatures.Lookup(ctx, repo, safe.ChainID, address, code)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(e.stdout, signaturesReport{ChainID: safe.ChainID, SafeBlock: safe.SafeBlock, Report: report})
	}
	writeLookup(e, safe.ChainID, safe.SafeBlock, report.Lookup)
	if report.Status != sourcify.Verified {
		return nil
	}
	fmt.Fprintf(e.stdout, "  Fallback  %s\n", specialText(report.Fallback))
	fmt.Fprintf(e.stdout, "  Receive   %s\n\n", specialText(report.Receive))

	if len(report.Functions) == 0 {
		fmt.Fprintf(e.stdout, "No functions\n")
		return nil
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SELECTOR\tSIGNATURE\tSTATE MUTABILITY")
	for _, f := range report.Functions {
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Selector, f.Signature, f.StateMutability)
	}
	return w.Flush()
}

// specialText describes a fallback or receive function, or its absence.
func specialText(s *signatures.Special) string {
	if s == nil {
		return "none"
	}
	return s.StateMutability
}
