package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// callsCommand lists the calls that the Safe of a request makes, as the
// deterministic checks judge them.
func callsCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("calls")
	asJSON := flags.Bool("json", false, "write the calls as JSON")
	requestFile := flags.String("request-file", "", "read the request from the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s calls [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Lists the calls that the Safe of a request makes, as arbot classify judges them, read from the Safe's\n")
		fmt.Fprintf(out, "chain as of the request's Safe block, without the transaction's own effects or anything later.\n\n")
		fmt.Fprintf(out, "It shows the Safe's code hash and the Safe proxy version of Charter § 2.1 whose official proxy factory\n")
		fmt.Fprintf(out, "deploys a proxy with that code on its network, if any, and its singleton, the address in storage slot\n")
		fmt.Fprintf(out, "0, with the § 2.1 singleton version that it is an official deployment of, if any.\n\n")
		fmt.Fprintf(out, "Delegate calls to the MultiSend and MultiSendCallOnly contracts of § 2.1 versions are decoded into\n")
		fmt.Fprintf(out, "the calls that they make, as the contracts do. A root call that reverts whatever the state is replaced\n")
		fmt.Fprintf(out, "by a \"reverting\" placeholder, a call to the MultiSend with no data, and one that makes no calls by an\n")
		fmt.Fprintf(out, "\"empty\" placeholder, a delegate call to its multiSend with no transactions. A last \"refund\" call\n")
		fmt.Fprintf(out, "pays the largest gas refund that the transaction allows, if any, where the zero address as its\n")
		fmt.Fprintf(out, "recipient means the account that executes the transaction. Each account that the Safe calls has its\n")
		fmt.Fprintf(out, "code hash and size, and the contract of a § 2.1 version that it is, identified by its address on the\n")
		fmt.Fprintf(out, "networks that the Charter covers if it has code. The text output shortens long data; -json has it all.\n\n")
		fmt.Fprintf(out, "Each call's data is decoded as a well-known function, if it is one: ERC-20 transfer, transferFrom,\n")
		fmt.Fprintf(out, "and approve, the Safe's execTransaction, with the data of the call that it makes decoded the same way,\n")
		fmt.Fprintf(out, "and the Safe proxy factories' createProxyWithNonce. Data is decoded by its selector and ABI layout only,\n")
		fmt.Fprintf(out, "so what a call does depends on the account that it is to.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	if *requestFile == "" {
		return usageError("-request-file is required")
	}
	request, err := readRequest(*requestFile)
	if err != nil {
		return err
	}
	report, err := checks.Calls(ctx, ethrpc.NewDialer(e.cfg.RPCs), request)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	safe := report.Safe
	fmt.Fprintf(e.stdout, "Safe %s on chain %d, as of block %d\n", safe.Address, report.ChainID, report.SafeBlock)
	fmt.Fprintf(e.stdout, "  Code       %s (%d bytes), %s\n", safe.CodeHash, safe.CodeSize, versionText("Safe proxy", safe.ProxyVersion))
	fmt.Fprintf(e.stdout, "  Singleton  %s, %s\n\n", safe.Singleton, versionText("Safe singleton", safe.SingletonVersion))
	fmt.Fprintf(e.stdout, "Target %s, %s\n\n", report.Target.Address, accountText(report.Target))

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tKIND\tTO\tACCOUNT\tOPERATION\tVALUE\tFUNCTION\tDATA")
	for i, c := range report.Calls {
		data := "-"
		if len(c.Data) > 0 {
			data = formatValue(c.Data)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", i+1, callText(c), data)
	}
	return w.Flush()
}

// callText describes a call in the KIND, TO, ACCOUNT, OPERATION, VALUE, and
// FUNCTION columns of a table of calls.
func callText(c checks.Call) string {
	kind := c.Kind
	if c.Placeholder != "" {
		kind += " (" + c.Placeholder + " placeholder)"
	}
	function := "-"
	if c.Function != nil {
		function = c.Function.String()
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", kind, c.To.Address, accountText(c.To), c.Operation, c.Value, function)
}

// versionText describes a § 2.1 version of a kind of contract, or its absence.
func versionText(kind string, version *string) string {
	if version == nil {
		return "not a " + kind + " of a § 2.1 version"
	}
	return kind + " " + *version
}

// accountText describes an account that a Safe calls.
func accountText(a checks.Account) string {
	switch {
	case a.Self:
		return "the Safe itself"
	case a.Contract != nil:
		return a.Contract.String()
	case a.CodeSize == 0:
		return "no code"
	}
	return fmt.Sprintf("code %s (%d bytes)", a.CodeHash, a.CodeSize)
}
