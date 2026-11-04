package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/executions"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// executionsReport is the JSON output of `arbot executions`.
type executionsReport struct {
	Safe    ethrpc.Address `json:"safe"`
	ChainID uint64         `json:"chainId"`
	// FromBlock and ToBlock are the blocks searched, both included. ToBlock is the
	// request's Safe block. FromBlock is 0 if the executions are complete.
	FromBlock uint64 `json:"fromBlock"`
	ToBlock   uint64 `json:"toBlock"`
	// Complete is whether the executions are all of the Safe's executions up to
	// ToBlock: those that the Safe Transaction Service lists, confirmed by the
	// receipts of their onchain transactions, have every nonce below the Safe's
	// nonce at ToBlock, and none is a delegate call. Otherwise, the executions are
	// those in the blocks searched, and those before them that the service lists
	// and their receipts confirm.
	Complete   bool        `json:"complete"`
	Executions []execution `json:"executions"`
}

// execution is an execution in the output of `arbot executions`.
type execution struct {
	executions.Execution
	// Target and Calls are the account that the transaction's own call is to, and
	// the calls that the transaction makes, as arbot calls reports them, with
	// -calls. They are left out without -calls, and for an unknown transaction.
	Target *checks.Account `json:"target,omitempty"`
	Calls  []checks.Call   `json:"calls,omitempty"`
}

// executionsCommand lists the Safe transactions that the Safe of a request
// executed before its proposal.
func executionsCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("executions")
	asJSON := flags.Bool("json", false, "write the executions as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	blocks := flags.Uint64("blocks", defaultScanBlocks, "if the executions that the Safe Transaction Service lists aren't complete, search this number of blocks up to the request's Safe block")
	withCalls := flags.Bool("calls", false, "also list the calls that each transaction makes, as arbot calls lists them")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s executions [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Lists the Safe transactions that the Safe of a request executed, from the Safe's chain, as of the\n")
		fmt.Fprintf(out, "request's Safe block, without the transaction's own effects or anything later: the ExecutionSuccess\n")
		fmt.Fprintf(out, "and ExecutionFailure events of the Safe. Executions by modules aren't listed.\n\n")
		fmt.Fprintf(out, "It first reads the events from the receipts of the onchain transactions that the Safe Transaction\n")
		fmt.Fprintf(out, "Service lists as executing the Safe's transactions, so the service can leave executions out, but\n")
		fmt.Fprintf(out, "not make them up. If those have every nonce below the Safe's nonce at the Safe block, and none is a\n")
		fmt.Fprintf(out, "delegate call, which could change the nonce, they are complete: all of the Safe's executions.\n")
		fmt.Fprintf(out, "Otherwise, it also scans the Safe's events in the last -blocks blocks up to the Safe block, so an\n")
		fmt.Fprintf(out, "execution before them that the service doesn't list isn't listed.\n\n")
		fmt.Fprintf(out, "The events only have the hash of each Safe transaction, so the command decodes the transaction from\n")
		fmt.Fprintf(out, "the first source that has one with that hash, and reports the source:\n\n")
		fmt.Fprintf(out, "  event     the SafeMultiSigTransaction event that SafeL2 logs\n")
		fmt.Fprintf(out, "  calldata  the calldata of the onchain transaction, if it calls execTransaction on the Safe\n")
		fmt.Fprintf(out, "  trace     a trace of the onchain transaction, which needs a node with debug_traceTransaction\n")
		fmt.Fprintf(out, "  service   the Safe Transaction Service (see the txServices setting, where an empty URL leaves it\n")
		fmt.Fprintf(out, "            out), an offchain source\n\n")
		fmt.Fprintf(out, "A transaction is only reported if its EIP-712 hash is the one that the Safe logged, whichever the\n")
		fmt.Fprintf(out, "source. The calldata and traces have no nonce, so it is worked out from the Safe's nonce at the last\n")
		fmt.Fprintf(out, "block. If no source has the transaction, it is an unknown transaction, or null with -json.\n")
		fmt.Fprintf(out, "The text output shows the selector and length of the data, and -json has all of it.\n\n")
		fmt.Fprintf(out, "With -calls, it also lists the calls that each known transaction makes, as arbot calls lists those\n")
		fmt.Fprintf(out, "of a request's transaction, with the accounts that they are to as of the request's Safe block: delegate\n")
		fmt.Fprintf(out, "calls to MultiSend contracts decoded into their calls, and each call's data decoded as a well-known\n")
		fmt.Fprintf(out, "function, if it is one, such as an ERC-20 transfer, or a Safe's execTransaction and the call it makes.\n")
		fmt.Fprintf(out, "The refund call is the largest refund that a transaction allows, and the payment is what it paid. The\n")
		fmt.Fprintf(out, "calls of a failed execution had no effect.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	scan, err := openScan(ctx, e, *requestFile, *blocks)
	if err != nil {
		return err
	}

	finder := &executions.Finder{
		Eth:  scan.eth,
		Warn: func(err error) { fmt.Fprintf(e.stderr, "%s: warning: %v\n", e.progname, err) },
	}
	baseURL, ok := e.cfg.TxServices[scan.ChainID]
	if !ok {
		baseURL = txservice.DefaultURLs[scan.ChainID]
	}
	if baseURL != "" {
		finder.Service = txservice.NewClient(baseURL, scan.ChainID)
	}
	found, err := finder.Find(ctx, scan.Safe, ethrpc.BlockNumber(scan.FromBlock), ethrpc.BlockNumber(scan.ToBlock))
	if err != nil {
		return err
	}
	report := executionsReport{
		Safe: scan.Safe, ChainID: scan.ChainID, FromBlock: scan.FromBlock, ToBlock: scan.ToBlock,
		Complete: found.Complete, Executions: make([]execution, len(found.Executions)),
	}
	if found.Complete {
		report.FromBlock = 0
	}
	var reader *checks.CallReader
	if *withCalls {
		reader = checks.NewCallReader(scan.eth, scan.Safe, ethrpc.BlockNumber(scan.SafeBlock))
	}
	for i, x := range found.Executions {
		report.Executions[i].Execution = x
		if reader == nil || x.Transaction == nil {
			continue
		}
		calls, err := reader.Calls(ctx, x.Transaction)
		if err != nil {
			return fmt.Errorf("decoding the calls of Safe transaction %s: %w", x.SafeTxHash, err)
		}
		report.Executions[i].Target, report.Executions[i].Calls = &calls.Target, calls.Calls
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	if report.Complete {
		fmt.Fprintf(e.stdout, "Safe transactions executed by %s on chain %d, up to block %d: all of them\n\n", report.Safe, report.ChainID, report.ToBlock)
	} else {
		fmt.Fprintf(e.stdout, "Safe transactions executed by %s on chain %d, in blocks %d to %d, and those before them that the Safe Transaction Service lists\n\n", report.Safe, report.ChainID, report.FromBlock, report.ToBlock)
	}
	if len(found.Executions) == 0 {
		_, err = fmt.Fprintln(e.stdout, "none")
		return err
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BLOCK\tTRANSACTION\tRESULT\tSAFE TX HASH\tSOURCE\tNONCE\tTO\tVALUE\tOPERATION\tDATA")
	for _, x := range found.Executions {
		result := "failure"
		if x.Success {
			result = "success"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t", x.Block, x.TxHash, result, x.SafeTxHash)
		if tx := x.Transaction; tx != nil {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Source, tx.Nonce, tx.To, tx.Value, tx.Operation, summarizeData(tx.Data))
		} else {
			fmt.Fprintf(w, "-\tunknown transaction\n")
		}
	}
	if err := w.Flush(); err != nil || reader == nil {
		return err
	}

	fmt.Fprintf(e.stdout, "\nCalls\n\n")
	w = tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NONCE\t#\tKIND\tTO\tACCOUNT\tOPERATION\tVALUE\tFUNCTION")
	for _, x := range report.Executions {
		for i, c := range x.Calls {
			fmt.Fprintf(w, "%s\t%d\t%s\n", x.Transaction.Nonce, i+1, callText(c))
		}
	}
	return w.Flush()
}

// summarizeData returns the selector and length of call data, such as
// "0xa9059cbb (68 bytes)", or "-" if there is none.
func summarizeData(data []byte) string {
	if len(data) == 0 {
		return "-"
	}
	return fmt.Sprintf("%s (%d bytes)", ethrpc.Bytes(data[:min(len(data), 4)]), len(data))
}
