package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/eip1967"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// proxyReport is the JSON output of `arbot proxy`.
type proxyReport struct {
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	Address   ethrpc.Address `json:"address"`
	eip1967.Slots
	// ImplementationCode is the code of the implementation, or null if the slot is
	// empty.
	ImplementationCode *codeInfo `json:"implementationCode"`
}

// codeInfo is the hash and size of an account's code.
type codeInfo struct {
	CodeHash ethrpc.Hash `json:"codeHash"`
	CodeSize int         `json:"codeSize"`
}

// proxyCommand reads the EIP-1967 slots of a contract on the Safe's chain of a
// request, as of its Safe block.
func proxyCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("proxy")
	asJSON := flags.Bool("json", false, "write the slots as JSON")
	requestFile := flags.String("request-file", "", "read the Safe's chain and block from the request in the JSON file at `path`, as written by arbot info -json")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s proxy [flags] -request-file <path> <address>\n\n", e.progname)
		fmt.Fprintf(out, "Reads the addresses that the contract at an address on the Safe's chain of a request stores in the\n")
		fmt.Fprintf(out, "storage slots of EIP-1967, as of the request's Safe block: the implementation that a proxy delegates\n")
		fmt.Fprintf(out, "calls to, its admin, and its beacon, with the code hash and size of the implementation. A slot that\n")
		fmt.Fprintf(out, "a contract doesn't use is empty, the zero address, and a value with nonzero high bytes isn't an\n")
		fmt.Fprintf(out, "address that Solidity stores. Look up the implementation's functions with arbot signatures.\n\n")
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
	block := ethrpc.BlockNumber(safe.SafeBlock)
	slots, err := eip1967.Read(ctx, safe.eth, address, block)
	if err != nil {
		return err
	}
	report := proxyReport{ChainID: safe.ChainID, SafeBlock: safe.SafeBlock, Address: address, Slots: *slots}
	if impl := slots.Implementation.Address; impl != (ethrpc.Address{}) {
		code, err := safe.eth.GetCode(ctx, impl, block)
		if err != nil {
			return fmt.Errorf("reading the code of %s: %w", impl, err)
		}
		report.ImplementationCode = &codeInfo{CodeHash: keccak256.Hash(code), CodeSize: len(code)}
	}
	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	fmt.Fprintf(e.stdout, "Contract %s on chain %d, as of block %d\n\n", address, report.ChainID, report.SafeBlock)
	for _, s := range []struct {
		name string
		slot eip1967.Slot
	}{{"Implementation", slots.Implementation}, {"Admin", slots.Admin}, {"Beacon", slots.Beacon}} {
		value := s.slot.Address.String()
		if !s.slot.Clean {
			value = "not an address: " + s.slot.Value.String()
		}
		fmt.Fprintf(e.stdout, "  %-14s  %s\n", s.name, value)
	}
	if c := report.ImplementationCode; c != nil {
		fmt.Fprintf(e.stdout, "\nImplementation code %s (%d bytes)\n", c.CodeHash, c.CodeSize)
	}
	return nil
}
