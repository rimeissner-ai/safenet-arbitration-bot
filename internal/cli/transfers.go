package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/erc20"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// transfersReport is the JSON output of `arbot transfers`.
type transfersReport struct {
	Safe    ethrpc.Address `json:"safe"`
	ChainID uint64         `json:"chainId"`
	// FromBlock and ToBlock are the blocks scanned, both included. ToBlock is the
	// request's Safe block.
	FromBlock uint64 `json:"fromBlock"`
	ToBlock   uint64 `json:"toBlock"`
	// Direction is "out" for the transfers out of the Safe, and "in" for those into
	// it.
	Direction string `json:"direction"`
	// From is the sender that the transfers into the Safe were limited to, if any.
	From *ethrpc.Address `json:"from,omitempty"`
	// To is the recipient that the transfers out of the Safe were limited to, if
	// any.
	To        *ethrpc.Address `json:"to,omitempty"`
	Transfers []transfer      `json:"transfers"`
}

// transfer is an ERC-20 transfer, in the output of `arbot transfers`. Type says
// what kind of token moved, so that other kinds can be added without changing
// the others.
type transfer struct {
	Type string `json:"type"`
	erc20.Transfer
}

// transfers lists the token transfers out of a Safe's balance, or into it,
// before the proposal of a request.
func transfers(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("transfers")
	asJSON := flags.Bool("json", false, "write the transfers as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	in := flags.Bool("in", false, "list the transfers into the Safe instead of out of it")
	fromFlag := flags.String("from", "", "with -in, only list transfers from this `address`")
	toFlag := flags.String("to", "", "without -in, only list transfers to this `address`")
	blocks := flags.Uint64("blocks", defaultScanBlocks, "scan this number of blocks up to the request's Safe block")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s transfers [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(flags.Output(), "Lists the ERC-20 transfers out of the Safe of a request, from the Safe's chain, as of the request's\n")
		fmt.Fprintf(flags.Output(), "Safe block: the Transfer events of any token with the Safe as sender, in the last -blocks blocks up to\n")
		fmt.Fprintf(flags.Output(), "it, without the transaction's own effects or anything later. It scans that range only, so a transfer\n")
		fmt.Fprintf(flags.Output(), "before it isn't listed. The events show that a token moved, not why: an account with an allowance can\n")
		fmt.Fprintf(flags.Output(), "move tokens out of the Safe without it making a transaction. Native and ERC-721 transfers aren't listed.\n\n")
		fmt.Fprintf(flags.Output(), "With -in, it lists the ERC-20 transfers into the Safe instead, in the same range: the Transfer events\n")
		fmt.Fprintf(flags.Output(), "of any contract with the Safe as recipient. Any contract can log a Transfer event to the Safe, such as\n")
		fmt.Fprintf(flags.Output(), "a fake token in an address poisoning attack, so the token that logged it matters.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	if *in && *toFlag != "" {
		return usageError("-to only applies to transfers out of the Safe, without -in")
	}
	if !*in && *fromFlag != "" {
		return usageError("-from only applies to transfers into the Safe, with -in")
	}
	from, err := addressFlag("from", *fromFlag)
	if err != nil {
		return err
	}
	to, err := addressFlag("to", *toFlag)
	if err != nil {
		return err
	}

	scan, err := openScan(ctx, e, *requestFile, *blocks)
	if err != nil {
		return err
	}

	report := transfersReport{Safe: scan.Safe, ChainID: scan.ChainID, FromBlock: scan.FromBlock, ToBlock: scan.ToBlock, From: from, To: to}
	fromBlock, toBlock := ethrpc.BlockNumber(scan.FromBlock), ethrpc.BlockNumber(scan.ToBlock)
	var found []erc20.Transfer
	if *in {
		report.Direction = "in"
		found, err = erc20.TransfersTo(ctx, scan.eth, scan.Safe, from, fromBlock, toBlock)
	} else {
		report.Direction = "out"
		found, err = erc20.TransfersFrom(ctx, scan.eth, scan.Safe, to, fromBlock, toBlock)
	}
	if err != nil {
		return err
	}
	report.Transfers = []transfer{}
	for _, t := range found {
		report.Transfers = append(report.Transfers, transfer{Type: "erc20", Transfer: t})
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	if *in {
		fmt.Fprintf(e.stdout, "ERC-20 transfers to %s on chain %d, in blocks %d to %d", report.Safe, report.ChainID, report.FromBlock, report.ToBlock)
		if from != nil {
			fmt.Fprintf(e.stdout, ", from %s", from)
		}
	} else {
		fmt.Fprintf(e.stdout, "ERC-20 transfers from %s on chain %d, in blocks %d to %d", report.Safe, report.ChainID, report.FromBlock, report.ToBlock)
		if to != nil {
			fmt.Fprintf(e.stdout, ", to %s", to)
		}
	}
	fmt.Fprintf(e.stdout, "\n\n")
	if len(report.Transfers) == 0 {
		_, err = fmt.Fprintln(e.stdout, "none")
		return err
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	if *in {
		fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTOKEN\tFROM\tVALUE")
		for _, t := range report.Transfers {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.Block, t.TxHash, t.Token, t.From, t.Value)
		}
	} else {
		fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTOKEN\tTO\tVALUE")
		for _, t := range report.Transfers {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.Block, t.TxHash, t.Token, t.To, t.Value)
		}
	}
	return w.Flush()
}

// addressFlag parses the value of the address flag name, which is nil if the
// flag isn't set.
func addressFlag(name, value string) (*ethrpc.Address, error) {
	if value == "" {
		return nil, nil
	}
	address, err := ethrpc.ParseAddress(value)
	if err != nil {
		return nil, usageError(fmt.Sprintf("-%s %q: %v", name, value, err))
	}
	return &address, nil
}
