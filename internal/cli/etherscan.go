package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/etherscan"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// etherscanReport is the JSON output of `arbot etherscan`.
type etherscanReport struct {
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	Address   ethrpc.Address `json:"address"`
	// CodeHash and CodeSize are those of the account's code at the Safe block.
	CodeHash ethrpc.Hash `json:"codeHash"`
	CodeSize int         `json:"codeSize"`
	*etherscan.Record
}

// etherscanCommand reports Etherscan's verified record of a contract on the
// Safe's chain of a request.
func etherscanCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("etherscan")
	asJSON := flags.Bool("json", false, "write the record as JSON")
	requestFile := flags.String("request-file", "", "read the Safe's chain and block from the request in the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s etherscan [flags] -request-file <path> <address>\n\n", e.progname)
		fmt.Fprintf(out, "Reports Etherscan's record of the contract at an address on the Safe's chain of a request: whether\n")
		fmt.Fprintf(out, "Etherscan verified its source, by compiling it into the contract's code, its name and compiler, the\n")
		fmt.Fprintf(out, "functions of its verified ABI, with their selectors, state mutability, and named inputs, and its\n")
		fmt.Fprintf(out, "fallback and receive functions. For a proxy, Etherscan's ABI is the proxy's own, so look up the\n")
		fmt.Fprintf(out, "implementation that arbot proxy reads at the Safe block. Etherscan verifies the code that the\n")
		fmt.Fprintf(out, "address has, which the report gives as of the Safe block, and its implementation field is as of now.\n\n")
		fmt.Fprintf(out, "Etherscan is an offchain source (Charter § 3.4), so the report has the request's URL, without the\n")
		fmt.Fprintf(out, "key, and when it was observed. Its API needs a key: the etherscanKey setting.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 1); err != nil {
		return err
	}
	address, err := ethrpc.ParseAddress(flags.Arg(0))
	if err != nil {
		return usageError(fmt.Sprintf("invalid address: %v", err))
	}
	safe, err := openSafe(ctx, e, *requestFile)
	if err != nil {
		return err
	}
	code, err := safe.eth.GetCode(ctx, address, ethrpc.BlockNumber(safe.SafeBlock))
	if err != nil {
		return fmt.Errorf("reading the code of %s: %w", address, err)
	}
	record, err := etherscan.NewClient(e.cfg.EtherscanKey).Contract(ctx, safe.ChainID, address)
	if err != nil {
		return err
	}
	report := etherscanReport{
		ChainID: safe.ChainID, SafeBlock: safe.SafeBlock, Address: address,
		CodeHash: keccak256.Hash(code), CodeSize: len(code), Record: record,
	}
	if *asJSON {
		return writeJSON(e.stdout, report)
	}

	out := e.stdout
	fmt.Fprintf(out, "Contract %s on chain %d, as of block %d\n", address, report.ChainID, report.SafeBlock)
	fmt.Fprintf(out, "  Code      %s (%d bytes)\n", report.CodeHash, report.CodeSize)
	fmt.Fprintf(out, "  Source    %s, observed %s\n", record.URL, record.ObservedAt.Format(time.RFC3339))
	if !record.Verified {
		_, err := fmt.Fprintf(out, "  Status    unverified\n")
		return err
	}
	fmt.Fprintf(out, "  Status    verified\n")
	fmt.Fprintf(out, "  Contract  %s, compiled with %s\n", strconv.Quote(record.Name), strconv.Quote(record.Compiler))
	if record.Proxy {
		fmt.Fprintf(out, "  Proxy     implementation %s, as Etherscan has it now\n", optional(record.Implementation))
	}
	if record.Fallback != nil {
		fmt.Fprintf(out, "  Fallback  %s\n", record.Fallback.StateMutability)
	}
	if record.Receive != nil {
		fmt.Fprintf(out, "  Receive   %s\n", record.Receive.StateMutability)
	}
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SELECTOR\tFUNCTION\tMUTABILITY")
	for _, f := range record.Functions {
		inputs := make([]string, len(f.Inputs))
		for i, p := range f.Inputs {
			inputs[i] = strings.TrimSpace(p.Type + " " + printable(p.Name))
		}
		name, _, _ := strings.Cut(f.Signature, "(")
		fmt.Fprintf(w, "%s\t%s(%s)\t%s\n", f.Selector, name, strings.Join(inputs, ", "), f.StateMutability)
	}
	return w.Flush()
}
