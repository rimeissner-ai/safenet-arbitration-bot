package checks

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/functions"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// TransactionCalls are the calls that the Safe of a request makes, as the
// checks judge them, with the Safe and the accounts that they call, as of the
// request's Safe block.
type TransactionCalls struct {
	ChainID   uint64      `json:"chainId"`
	SafeBlock uint64      `json:"safeBlock"`
	Safe      SafeAccount `json:"safe"`
	// Target is the account that the Safe transaction's own call is to.
	Target Account `json:"target"`
	Calls  []Call  `json:"calls"`
}

// SafeAccount is the Safe of a request, with what § 2.1 identifies a Safe by:
// the version of its proxy, and the singleton that the proxy delegates to.
type SafeAccount struct {
	Address  ethrpc.Address `json:"address"`
	CodeHash ethrpc.Hash    `json:"codeHash"`
	CodeSize int            `json:"codeSize"`
	// ProxyVersion is the proxy version of § 2.1 whose official proxy factory
	// deploys a SafeProxy with the account's code on its network, if any.
	ProxyVersion *string `json:"proxyVersion"`
	// Singleton is the address in the low 20 bytes of the account's storage slot 0,
	// which a SafeProxy delegates calls to.
	Singleton ethrpc.Address `json:"singleton"`
	// SingletonVersion is the singleton version of § 2.1 that Singleton is an
	// official deployment of, with "+L2" for SafeL2, if any.
	SingletonVersion *string `json:"singletonVersion"`
}

// Account is an account that a Safe calls.
type Account struct {
	Address  ethrpc.Address `json:"address"`
	CodeHash ethrpc.Hash    `json:"codeHash"`
	CodeSize int            `json:"codeSize"`
	// Self is whether the account is the Safe itself.
	Self bool `json:"self"`
	// Contract is the contract of a Safe version of § 2.1 that the account is an
	// official deployment of, if any.
	Contract *Contract `json:"contract"`
}

// Contract is a contract of a Safe version of § 2.1: MultiSend,
// MultiSendCallOnly, or the Safe or SafeL2 singleton.
type Contract struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (c Contract) String() string {
	return c.Name + " " + c.Version
}

// Call is a call that a Safe makes, as the checks judge it.
type Call struct {
	// Kind is "root" for the Safe transaction's own call, or a placeholder for it,
	// "batched" for a call that a MultiSend makes, and "refund" for the synthetic
	// call that pays the largest gas refund that the transaction allows.
	Kind string `json:"kind"`
	// Placeholder is set for a root call that stands for the Safe transaction's own
	// call, rather than being it: "reverting" for a call to a MultiSend that
	// reverts whatever the state, and "empty" for one that makes no calls.
	Placeholder string            `json:"placeholder,omitempty"`
	To          Account           `json:"to"`
	Value       *big.Int          `json:"value"`
	Operation   safenet.Operation `json:"operation"`
	Data        ethrpc.Bytes      `json:"data"`
	// Function is the data decoded as a well-known function, by its selector and
	// ABI layout only, if it is one. What the call does depends on the account that
	// it is to.
	Function *functions.Function `json:"function"`
}

// callKinds are the names of the kinds of calls, by callKind.
var callKinds = []string{Root: "root", Batched: "batched", Refund: "refund"}

// Calls returns the calls that the Safe of request makes, as Classify judges
// them: delegate calls to the MultiSend and MultiSendCallOnly contracts of §
// 2.1 versions are decoded into the calls that they make, a call that reverts
// whatever the state or makes no calls is replaced by a placeholder, and a
// synthetic call pays the largest gas refund that the transaction allows, if
// any. It reads the Safe and the accounts that it calls on the Safe's chain at
// the proposal's SafeBlock, connecting with dial.
//
// Contracts are identified by their address, since the contracts of § 2.1
// versions are deterministic deployments, and only on the networks that the
// Charter covers, which they are deployed on. An address without code isn't
// identified.
func Calls(ctx context.Context, dial ethrpc.Dialer, request *safenet.Request) (*TransactionCalls, error) {
	tx := &request.Proposal.Transaction
	components, err := components(tx)
	if err != nil {
		return nil, err
	}
	if !tx.ChainID.IsUint64() {
		return nil, errors.New("safe transaction has an invalid chainId")
	}
	if request.Proposal.SafeBlock == 0 {
		return nil, errors.New("proposal has no safeBlock")
	}
	chainID := tx.ChainID.Uint64()
	covered := slices.Contains(networks, chainID)
	client, err := dial(ctx, chainID)
	if err != nil {
		return nil, err
	}
	block := ethrpc.BlockNumber(request.Proposal.SafeBlock)

	codes := map[ethrpc.Address]ethrpc.Bytes{}
	code := func(address ethrpc.Address) (ethrpc.Bytes, error) {
		if c, ok := codes[address]; ok {
			return c, nil
		}
		c, err := client.GetCode(ctx, address, block)
		if err != nil {
			return nil, err
		}
		codes[address] = c
		return c, nil
	}

	safeCode, err := code(tx.Safe)
	if err != nil {
		return nil, err
	}
	slot0, err := client.GetStorageAt(ctx, tx.Safe, ethrpc.Hash{}, block)
	if err != nil {
		return nil, err
	}
	singleton := ethrpc.Address(slot0[12:])
	safe := SafeAccount{
		Address:      tx.Safe,
		CodeHash:     ethrpc.Hash(keccak256.Hash(safeCode)),
		CodeSize:     len(safeCode),
		ProxyVersion: optional(proxyVersion(safeCode, chainID)),
		Singleton:    singleton,
	}
	if covered {
		safe.SingletonVersion = optional(singletons[singleton])
	}

	account := func(address ethrpc.Address) (Account, error) {
		c, err := code(address)
		if err != nil {
			return Account{}, err
		}
		a := Account{
			Address:  address,
			CodeHash: ethrpc.Hash(keccak256.Hash(c)),
			CodeSize: len(c),
			Self:     address == tx.Safe,
		}
		if covered && len(c) > 0 {
			a.Contract = contractAt(address)
		}
		return a, nil
	}

	report := &TransactionCalls{ChainID: chainID, SafeBlock: request.Proposal.SafeBlock, Safe: safe, Calls: []Call{}}
	if report.Target, err = account(tx.To); err != nil {
		return nil, err
	}
	own := call{to: tx.To, value: tx.Value, data: tx.Data, operation: tx.Operation, kind: Root}
	for _, c := range components.calls {
		to, err := account(c.to)
		if err != nil {
			return nil, err
		}
		reported := Call{Kind: callKinds[c.kind], To: to, Value: c.value, Operation: c.operation, Data: c.data}
		if reported.Data == nil {
			reported.Data = ethrpc.Bytes{}
		}
		reported.Function = functions.Decode(reported.Data)
		if c.kind == Root && !c.equal(own) {
			switch {
			case c.equal(revertingMultiSend(c.to)):
				reported.Placeholder = "reverting"
			case c.equal(emptyMultiSend(c.to)):
				reported.Placeholder = "empty"
			}
		}
		report.Calls = append(report.Calls, reported)
	}
	return report, nil
}

// contractAt returns the contract of a § 2.1 version at address, if any.
func contractAt(address ethrpc.Address) *Contract {
	if ms, ok := multiSends[address]; ok {
		name := "MultiSend"
		if ms.callOnly {
			name = "MultiSendCallOnly"
		}
		return &Contract{Name: name, Version: ms.version}
	}
	if version, ok := singletons[address]; ok {
		if v, ok := strings.CutSuffix(version, "+L2"); ok {
			return &Contract{Name: "SafeL2", Version: v}
		}
		return &Contract{Name: "Safe", Version: version}
	}
	return nil
}

// optional returns a pointer to s, or nil if s is empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
