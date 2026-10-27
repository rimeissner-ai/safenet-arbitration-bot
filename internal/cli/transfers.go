package cli

import (
	"cmp"
	"context"
	"fmt"
	"math/big"
	"slices"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/erc20"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/native"
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

// transfer is a transfer in the output of `arbot transfers`. Type says what
// moved, "erc20" for an ERC-20 token, whose contract is Token, or "native" for
// the chain's native currency, which has no Token, so that other kinds can be
// added without changing the others.
type transfer struct {
	Type     string          `json:"type"`
	Block    uint64          `json:"block"`
	TxHash   ethrpc.Hash     `json:"txHash"`
	LogIndex uint64          `json:"logIndex"`
	Token    *ethrpc.Address `json:"token,omitempty"`
	From     ethrpc.Address  `json:"from"`
	To       ethrpc.Address  `json:"to"`
	// Value is the amount in the smallest unit of the token or the currency.
	Value *big.Int `json:"value"`
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
		fmt.Fprintf(flags.Output(), "move tokens out of the Safe without it making a transaction. Native and ERC-721 transfers out of the\n")
		fmt.Fprintf(flags.Output(), "Safe aren't listed.\n\n")
		fmt.Fprintf(flags.Output(), "With -in, it lists the transfers into the Safe instead, in the same range: the Transfer events of any\n")
		fmt.Fprintf(flags.Output(), "contract with the Safe as recipient, and the native currency that the Safe's SafeReceived events show,\n")
		fmt.Fprintf(flags.Output(), "with the caller that sent it. Any contract can log a Transfer event to the Safe, such as a fake token\n")
		fmt.Fprintf(flags.Output(), "in an address poisoning attack, so the token that logged it matters. Native currency that reaches the\n")
		fmt.Fprintf(flags.Output(), "Safe other than through a call with no data, such as the value of an execTransaction call, a block\n")
		fmt.Fprintf(flags.Output(), "reward, or a self-destruct, has no event and isn't listed, and a delegate call that the Safe makes can\n")
		fmt.Fprintf(flags.Output(), "log a SafeReceived event. ERC-721 transfers aren't listed.\n\n")
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
	var tokens []erc20.Transfer
	report.Transfers = []transfer{}
	if *in {
		report.Direction = "in"
		if tokens, err = erc20.TransfersTo(ctx, scan.eth, scan.Safe, from, fromBlock, toBlock); err != nil {
			return err
		}
		received, err := native.ReceivedBy(ctx, scan.eth, scan.Safe, from, fromBlock, toBlock)
		if err != nil {
			return err
		}
		for _, t := range received {
			report.Transfers = append(report.Transfers, transfer{
				Type: "native", Block: t.Block, TxHash: t.TxHash, LogIndex: t.LogIndex, From: t.From, To: t.To, Value: t.Value,
			})
		}
	} else {
		report.Direction = "out"
		if tokens, err = erc20.TransfersFrom(ctx, scan.eth, scan.Safe, to, fromBlock, toBlock); err != nil {
			return err
		}
	}
	for _, t := range tokens {
		report.Transfers = append(report.Transfers, transfer{
			Type: "erc20", Block: t.Block, TxHash: t.TxHash, LogIndex: t.LogIndex, Token: &t.Token, From: t.From, To: t.To, Value: t.Value,
		})
	}
	slices.SortStableFunc(report.Transfers, func(a, b transfer) int {
		return cmp.Or(cmp.Compare(a.Block, b.Block), cmp.Compare(a.LogIndex, b.LogIndex))
	})

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	if *in {
		fmt.Fprintf(e.stdout, "Transfers to %s on chain %d, in blocks %d to %d", report.Safe, report.ChainID, report.FromBlock, report.ToBlock)
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
		fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTYPE\tTOKEN\tFROM\tVALUE")
		for _, t := range report.Transfers {
			token := "-"
			if t.Token != nil {
				token = t.Token.String()
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", t.Block, t.TxHash, t.Type, token, t.From, t.Value)
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
