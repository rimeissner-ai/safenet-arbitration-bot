package checks

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/functions"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

func TestCalls(t *testing.T) {
	safe := ethrpc.Address{0: 0x5a, 19: 0x01}
	token := ethrpc.Address{0: 0x70, 19: 0x01}
	receiver := ethrpc.Address{0: 0x7e, 19: 0x01}
	multiSend141 := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	callOnly141 := ethrpc.MustParseAddress("0x9641d764fc13c8B624c04430C7356C1C7C8102e2")
	singleton141 := singletonOf("1.4.1")
	tokenCode := ethrpc.Bytes{0x60, 0x80, 0x60, 0x40}
	multiSendCode := ethrpc.Bytes{0x60, 0x80}
	node := &fakeNode{accounts: map[ethrpc.Address]account{
		safe:             proxy(safeProxy100, singleton141),
		token:            {code: tokenCode},
		multiSend141:     {code: multiSendCode},
		callOnly141:      {code: multiSendCode},
		singleton141:     {code: multiSendCode},
		ethrpc.Address{}: {},
	}}

	approve := solabi.Call(solabi.Selector("approve(address,uint256)"), receiver, big.NewInt(5))
	batch := pack(
		call{to: token, value: new(big.Int), data: approve, operation: safenet.OperationCall},
		call{to: safe, value: big.NewInt(1), data: ethrpc.Bytes{}, operation: safenet.OperationCall},
	)
	multiSendData := func(transactions []byte) ethrpc.Bytes { return solabi.Call(multiSendSelector, transactions) }

	codeHash := func(code []byte) ethrpc.Hash { return ethrpc.Hash(keccak256.Hash(code)) }
	version := func(v string) *string { return &v }
	safeAccount := Account{Address: safe, CodeHash: codeHash(safeProxy100), CodeSize: len(safeProxy100), Self: true}
	tokenAccount := Account{Address: token, CodeHash: codeHash(tokenCode), CodeSize: len(tokenCode)}
	contract := func(address ethrpc.Address, name, version string) Account {
		return Account{
			Address: address, CodeHash: codeHash(multiSendCode), CodeSize: len(multiSendCode),
			Contract: &Contract{Name: name, Version: version},
		}
	}
	unidentified := func(a Account) Account {
		a.Contract = nil
		return a
	}
	noCode := func(address ethrpc.Address) Account {
		return Account{Address: address, CodeHash: codeHash(nil)}
	}
	mainnetSafe := SafeAccount{
		Address: safe, CodeHash: codeHash(safeProxy100), CodeSize: len(safeProxy100),
		ProxyVersion: version("1.0.0"), Singleton: singleton141, SingletonVersion: version("1.4.1"),
	}

	tests := []struct {
		name    string
		chainID int64
		tx      safenet.SafeTransaction
		safe    SafeAccount
		target  Account
		calls   []Call
	}{
		{
			name: "MultiSendCallOnly with a refund",
			tx: safenet.SafeTransaction{
				To: callOnly141, Data: multiSendData(batch), Operation: safenet.OperationDelegateCall,
				SafeTxGas: big.NewInt(10), BaseGas: big.NewInt(5), GasPrice: big.NewInt(2), RefundReceiver: receiver,
			},
			safe:   mainnetSafe,
			target: contract(callOnly141, "MultiSendCallOnly", "1.4.1"),
			calls: []Call{
				{Kind: "batched", To: tokenAccount, Value: new(big.Int), Operation: safenet.OperationCall, Data: approve},
				{Kind: "batched", To: safeAccount, Value: big.NewInt(1), Operation: safenet.OperationCall, Data: ethrpc.Bytes{}},
				{Kind: "refund", To: noCode(receiver), Value: big.NewInt(30), Operation: safenet.OperationCall, Data: ethrpc.Bytes{}},
			},
		},
		{
			name:   "call to the Safe",
			tx:     safenet.SafeTransaction{To: safe, Value: big.NewInt(1), Data: approve},
			safe:   mainnetSafe,
			target: safeAccount,
			calls:  []Call{{Kind: "root", To: safeAccount, Value: big.NewInt(1), Operation: safenet.OperationCall, Data: approve}},
		},
		{
			name:   "delegate call to a singleton",
			tx:     safenet.SafeTransaction{To: singleton141, Operation: safenet.OperationDelegateCall},
			safe:   mainnetSafe,
			target: contract(singleton141, "Safe", "1.4.1"),
			calls: []Call{{
				Kind: "root", To: contract(singleton141, "Safe", "1.4.1"), Value: new(big.Int),
				Operation: safenet.OperationDelegateCall, Data: ethrpc.Bytes{},
			}},
		},
		{
			name:   "reverting MultiSend",
			tx:     safenet.SafeTransaction{To: multiSend141, Data: multiSendData(batch)},
			safe:   mainnetSafe,
			target: contract(multiSend141, "MultiSend", "1.4.1"),
			calls: []Call{{
				Kind: "root", Placeholder: "reverting", To: contract(multiSend141, "MultiSend", "1.4.1"), Value: new(big.Int),
				Operation: safenet.OperationCall, Data: ethrpc.Bytes{},
			}},
		},
		{
			name: "empty MultiSend",
			tx: safenet.SafeTransaction{
				To: multiSend141, Data: multiSendData(make([]byte, 32)), Operation: safenet.OperationDelegateCall,
			},
			safe:   mainnetSafe,
			target: contract(multiSend141, "MultiSend", "1.4.1"),
			calls: []Call{{
				Kind: "root", Placeholder: "empty", To: contract(multiSend141, "MultiSend", "1.4.1"), Value: new(big.Int),
				Operation: safenet.OperationDelegateCall, Data: multiSendData([]byte{}),
			}},
		},
		{
			name:    "1.0.0 proxy on Arbitrum One",
			chainID: ethrpc.ArbitrumOne,
			tx:      safenet.SafeTransaction{To: token},
			safe: SafeAccount{
				Address: safe, CodeHash: codeHash(safeProxy100), CodeSize: len(safeProxy100),
				Singleton: singleton141, SingletonVersion: version("1.4.1"),
			},
			target: tokenAccount,
			calls:  []Call{{Kind: "root", To: tokenAccount, Value: new(big.Int), Operation: safenet.OperationCall, Data: ethrpc.Bytes{}}},
		},
		{
			name:    "network that the Charter doesn't cover",
			chainID: 10,
			tx:      safenet.SafeTransaction{To: callOnly141, Data: multiSendData(batch), Operation: safenet.OperationDelegateCall},
			safe: SafeAccount{
				Address: safe, CodeHash: codeHash(safeProxy100), CodeSize: len(safeProxy100), Singleton: singleton141,
			},
			target: unidentified(contract(callOnly141, "MultiSendCallOnly", "1.4.1")),
			calls: []Call{
				{Kind: "batched", To: tokenAccount, Value: new(big.Int), Operation: safenet.OperationCall, Data: approve},
				{Kind: "batched", To: safeAccount, Value: big.NewInt(1), Operation: safenet.OperationCall, Data: ethrpc.Bytes{}},
			},
		},
		{
			name:   "MultiSend without code",
			tx:     safenet.SafeTransaction{To: ethrpc.MustParseAddress("0x218543288004CD07832472D464648173c77D7eB7"), Data: multiSendData(batch)},
			safe:   mainnetSafe,
			target: noCode(ethrpc.MustParseAddress("0x218543288004CD07832472D464648173c77D7eB7")),
			calls: []Call{{
				Kind: "root", Placeholder: "reverting", To: noCode(ethrpc.MustParseAddress("0x218543288004CD07832472D464648173c77D7eB7")),
				Value: new(big.Int), Operation: safenet.OperationCall, Data: ethrpc.Bytes{},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := test.tx
			tx.Safe = safe
			if test.chainID != 0 {
				tx.ChainID = big.NewInt(test.chainID)
			}
			r := request(tx)
			got, err := Calls(t.Context(), node.dialer(t, testSafeBlock), r)
			if err != nil {
				t.Fatal(err)
			}
			// Each call has its data decoded as a well-known function, which the functions
			// package tests.
			for i := range test.calls {
				test.calls[i].Function = functions.Decode(test.calls[i].Data)
			}
			want := &TransactionCalls{
				ChainID: r.Proposal.Transaction.ChainID.Uint64(), SafeBlock: testSafeBlock,
				Safe: test.safe, Target: test.target, Calls: test.calls,
			}
			// The calls are compared as JSON, which is how arbot calls writes them, since
			// equal big.Int values can differ in their representation.
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Errorf("Calls():\ngot  %s\nwant %s", g, w)
			}
		})
	}
}

// TestCallReader checks that a reader reads the calls of several transactions
// of the Safe, reading each account once, and refuses those of another Safe or
// chain.
func TestCallReader(t *testing.T) {
	safe := ethrpc.Address{0: 0x5a, 19: 0x01}
	token := ethrpc.Address{0: 0x70, 19: 0x01}
	node := &fakeNode{accounts: map[ethrpc.Address]account{
		safe:  proxy(safeProxy100, singletonOf("1.4.1")),
		token: {code: ethrpc.Bytes{0x60, 0x80}},
	}}
	r := request(safenet.SafeTransaction{Safe: safe, To: token})
	client, err := node.dialer(t, testSafeBlock)(t.Context(), ethrpc.Mainnet)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewCallReader(client, safe, testSafeBlock)
	for i, data := range []ethrpc.Bytes{
		solabi.Call(solabi.Selector("transfer(address,uint256)"), safe, big.NewInt(1)),
		solabi.Call(solabi.Selector("approve(address,uint256)"), safe, big.NewInt(2)),
	} {
		tx := r.Proposal.Transaction
		tx.Data = data
		got, err := reader.Calls(t.Context(), &tx)
		if err != nil {
			t.Fatalf("transaction %d: %v", i, err)
		}
		if got.Safe.ProxyVersion == nil || len(got.Calls) != 1 || got.Calls[0].To.Address != token || got.Calls[0].Function == nil {
			t.Errorf("transaction %d: got %+v, want the Safe and one decoded call to the token", i, got)
		}
	}
	// The Safe's code and slot 0, and the token's code.
	if n := node.reads.Load(); n != 3 {
		t.Errorf("reads: got %d, want 3", n)
	}
	otherSafe, otherChain := r.Proposal.Transaction, r.Proposal.Transaction
	otherSafe.Safe = token
	otherChain.ChainID = big.NewInt(ethrpc.Gnosis)
	for name, tx := range map[string]*safenet.SafeTransaction{"Safe": &otherSafe, "chain": &otherChain} {
		if _, err := reader.Calls(t.Context(), tx); err == nil || !strings.Contains(err.Error(), "another Safe or chain") {
			t.Errorf("transaction of another %s: got error %v, want one about another Safe or chain", name, err)
		}
	}
}

func TestCallsErrors(t *testing.T) {
	missingGasPrice := request(safenet.SafeTransaction{})
	missingGasPrice.Proposal.Transaction.GasPrice = nil
	noSafeBlock := request(safenet.SafeTransaction{})
	noSafeBlock.Proposal.SafeBlock = 0
	hugeChainID := request(safenet.SafeTransaction{ChainID: new(big.Int).Lsh(big.NewInt(1), 64)})
	for _, test := range []struct {
		name    string
		request *safenet.Request
		want    string
	}{
		{"missing amount", missingGasPrice, "gasPrice"},
		{"no Safe block", noSafeBlock, "safeBlock"},
		{"invalid chain ID", hugeChainID, "chainId"},
	} {
		if _, err := Calls(t.Context(), nil, test.request); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: Calls() = _, %v; want an error about %s", test.name, err, test.want)
		}
	}

	node := &fakeNode{fail: "eth_getStorageAt"}
	if _, err := Calls(t.Context(), node.dialer(t, testSafeBlock), request(safenet.SafeTransaction{})); err == nil {
		t.Error("Calls() with eth_getStorageAt failing: expected an error")
	}
}
