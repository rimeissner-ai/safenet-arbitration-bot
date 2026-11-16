package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/txservice"
)

// safeServiceReport is the JSON output of `arbot safe-service`.
type safeServiceReport struct {
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	Proposed  time.Time      `json:"proposed"`
	Address   ethrpc.Address `json:"address"`
	// Source is where the evidence comes from, an offchain source (Charter § 3.4).
	Source serviceSource `json:"source"`
	// Indexed is whether the service indexes the account as a Safe. If not, the
	// other fields are null.
	Indexed bool `json:"indexed"`
	// Creation is the Safe's creation, or null if the service has it created at or
	// after the proposal.
	Creation *txservice.Creation `json:"creation"`
	// Transfers are those into and out of the account up to the Safe block, in the
	// service's order, newest first.
	Transfers []txservice.Transfer `json:"transfers"`
	// OwnerSafes are, with -owner-safes, the Safes that the service lists for each
	// owner that the Safe was set up with, and that were created before the
	// proposal, by owner. The service lists an owner's Safes as of now, so a Safe
	// that the owner no longer owns isn't listed. Left out without -owner-safes.
	OwnerSafes map[string][]ownerSafe `json:"ownerSafes,omitempty"`
}

// serviceSource is the Safe Transaction Service that a report comes from, and
// when the command observed it.
type serviceSource struct {
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	ObservedAt time.Time `json:"observedAt"`
}

// ownerSafe is a Safe of an owner in the output of `arbot safe-service`.
type ownerSafe struct {
	Address ethrpc.Address `json:"address"`
	Created time.Time      `json:"created"`
	Creator ethrpc.Address `json:"creator"`
}

// safeServiceCommand reports what the Safe Transaction Service has on a Safe,
// as of the proposal of a request.
func safeServiceCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("safe-service")
	asJSON := flags.Bool("json", false, "write the report as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	addressFlag := flags.String("address", "", "report on the account at `address` on the Safe's chain instead of the request's Safe")
	withOwnerSafes := flags.Bool("owner-safes", false, "also list the other Safes of the owners that the Safe was set up with, created before the proposal")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s safe-service [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Reports what the Safe Transaction Service of the Safe's chain of a request has on the request's Safe,\n")
		fmt.Fprintf(out, "or on the account at -address, as of the proposal: the Safe's creation, if it was before the proposal,\n")
		fmt.Fprintf(out, "with its creator, factory, singleton, and the owners and threshold it was set up with, and the transfers\n")
		fmt.Fprintf(out, "into and out of it, of native currency, ERC-20, and ERC-721 tokens, in blocks up to the request's Safe\n")
		fmt.Fprintf(out, "block. The service finds native transfers in traces, so it lists those that internal calls make. It\n")
		fmt.Fprintf(out, "only indexes Safes, so an account that isn't one is reported as not indexed.\n\n")
		fmt.Fprintf(out, "With -owner-safes, it also lists, for each owner that the Safe was set up with, the Safes that the\n")
		fmt.Fprintf(out, "service lists the owner as owning now, and that were created before the proposal.\n\n")
		fmt.Fprintf(out, "The service is an offchain source (Charter § 3.4), so the report has its URL and when the command\n")
		fmt.Fprintf(out, "observed it. Token symbols are what the token's deployer chose, so they are data.\n\n")
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
	proposal := request.Proposal
	tx := proposal.Transaction
	if tx.ChainID == nil || !tx.ChainID.IsUint64() || proposal.SafeBlock == 0 || proposal.Time.IsZero() {
		return fmt.Errorf("request in %s has no Safe chain ID, block, or proposal time", *requestFile)
	}
	chainID := tx.ChainID.Uint64()
	address := tx.Safe
	if *addressFlag != "" {
		if address, err = ethrpc.ParseAddress(*addressFlag); err != nil {
			return usageError(fmt.Sprintf("invalid -address: %v", err))
		}
	}
	baseURL, ok := e.cfg.TxServices[chainID]
	if !ok {
		baseURL = txservice.DefaultURLs[chainID]
	}
	if baseURL == "" {
		return fmt.Errorf("no Safe Transaction Service for chain %d (see the txServices setting)", chainID)
	}
	service := txservice.NewClient(baseURL, chainID)

	report := safeServiceReport{
		ChainID: chainID, SafeBlock: proposal.SafeBlock, Proposed: proposal.Time, Address: address,
		Source: serviceSource{Name: "Safe Transaction Service", URL: service.BaseURL(), ObservedAt: time.Now().UTC().Truncate(time.Second)},
	}
	creation, err := service.Creation(ctx, address)
	switch {
	case errors.Is(err, txservice.ErrNotIndexed):
	case err != nil:
		return fmt.Errorf("getting the creation of %s: %w", address, err)
	default:
		report.Indexed = true
		if creation.Created.Before(proposal.Time) {
			report.Creation = creation
		}
	}
	if report.Indexed {
		if report.Transfers, err = service.Transfers(ctx, address, proposal.SafeBlock); err != nil {
			return fmt.Errorf("getting the transfers of %s: %w", address, err)
		}
	}
	if *withOwnerSafes && report.Creation != nil {
		report.OwnerSafes = make(map[string][]ownerSafe)
		for _, owner := range report.Creation.Owners {
			safes, err := service.OwnerSafes(ctx, owner)
			if err != nil {
				return fmt.Errorf("getting the Safes of %s: %w", owner, err)
			}
			list := []ownerSafe{}
			for _, safe := range safes {
				c, err := service.Creation(ctx, safe)
				if errors.Is(err, txservice.ErrNotIndexed) {
					continue
				} else if err != nil {
					return fmt.Errorf("getting the creation of %s: %w", safe, err)
				}
				if c.Created.Before(proposal.Time) {
					list = append(list, ownerSafe{Address: safe, Created: c.Created, Creator: c.Creator})
				}
			}
			report.OwnerSafes[owner.String()] = list
		}
	}

	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	return writeSafeServiceText(e, &report)
}

// writeSafeServiceText prints the report of arbot safe-service for humans.
func writeSafeServiceText(e *env, r *safeServiceReport) error {
	out := e.stdout
	fmt.Fprintf(out, "Account %s on chain %d, as of block %d (proposed %s)\n", r.Address, r.ChainID, r.SafeBlock, r.Proposed.Format(time.RFC3339))
	fmt.Fprintf(out, "Source  %s, %s, observed %s\n\n", r.Source.Name, r.Source.URL, r.Source.ObservedAt.Format(time.RFC3339))
	if !r.Indexed {
		_, err := fmt.Fprintln(out, "not indexed by the service")
		return err
	}
	if c := r.Creation; c == nil {
		fmt.Fprintf(out, "Creation  at or after the proposal\n")
	} else {
		fmt.Fprintf(out, "Creation\n")
		fmt.Fprintf(out, "  Created      %s\n", c.Created.Format(time.RFC3339))
		fmt.Fprintf(out, "  Creator      %s\n", c.Creator)
		fmt.Fprintf(out, "  Transaction  %s\n", c.TransactionHash)
		fmt.Fprintf(out, "  Factory      %s\n", optional(c.Factory))
		fmt.Fprintf(out, "  Singleton    %s\n", optional(c.Singleton))
		fmt.Fprintf(out, "  Owners       %d\n", len(c.Owners))
		for _, o := range c.Owners {
			fmt.Fprintf(out, "    %s\n", o)
		}
		threshold := "-"
		if c.Threshold != nil {
			threshold = strconv.Quote(*c.Threshold)
		}
		fmt.Fprintf(out, "  Threshold    %s\n", threshold)
	}

	fmt.Fprintf(out, "\nTransfers up to block %d\n\n", r.SafeBlock)
	if len(r.Transfers) == 0 {
		fmt.Fprintln(out, "none")
	} else {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "BLOCK\tTRANSACTION\tTYPE\tTOKEN\tSYMBOL\tFROM\tTO\tVALUE\tTOKEN ID")
		for _, t := range r.Transfers {
			symbol := "-"
			if t.TokenSymbol != nil {
				symbol = strconv.Quote(*t.TokenSymbol)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Block, t.TransactionHash, strconv.Quote(t.Type), optional(t.Token), symbol, t.From, t.To, optionalQuoted(t.Value), optionalQuoted(t.TokenID))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}

	if r.OwnerSafes == nil {
		return nil
	}
	fmt.Fprintf(out, "\nSafes of the owners, created before the proposal\n")
	for _, owner := range r.Creation.Owners {
		safes := r.OwnerSafes[owner.String()]
		fmt.Fprintf(out, "\n%s\n", owner)
		if len(safes) == 0 {
			fmt.Fprintln(out, "  none")
			continue
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, s := range safes {
			fmt.Fprintf(w, "  %s\t%s\tcreator %s\n", s.Address, s.Created.Format(time.RFC3339), s.Creator)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// optional returns an address, or "-" for none.
func optional(a *ethrpc.Address) string {
	if a == nil {
		return "-"
	}
	return a.String()
}

// optionalQuoted returns a string from the service, quoted, or "-" for none.
func optionalQuoted(s *string) string {
	if s == nil {
		return "-"
	}
	return strconv.Quote(*s)
}
