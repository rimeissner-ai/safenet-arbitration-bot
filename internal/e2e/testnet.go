package e2e

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	transactionProposedEvent = solabi.Event("TransactionProposed(bytes32,bytes32,address,uint64,bytes,(uint256,address,address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256))")
	newRequestEvent          = solabi.Event("NewRequest(bytes32,address,uint96,uint96,uint24,uint96,uint64,uint64)")
	disputeTriggeredEvent    = solabi.Event("DisputeTriggered(bytes32,uint64)")
)

// Testnet runs Safenet requests on anvil. It embeds the Gnosis Chain node.
type Testnet struct {
	*Anvil
	// Mainnet is an Ethereum Mainnet node with only the Safe, for finding the
	// Mainnet blocks before proposals. It keeps the states of its recent blocks, so
	// that arbot can read the Safe at the block before a recent proposal.
	Mainnet   *Anvil
	Artifacts *Artifacts
	// Sponsor proposes the transactions and pays their fees.
	Sponsor ethrpc.Address
	// ArbitrationTimeout is the oracle's ARBITRATION_TIMEOUT.
	ArbitrationTimeout uint64

	// nonce is the nonce of the last Safe transaction, which keeps proposals
	// distinct.
	nonce uint64
	// gateway is the IPFS gateway that ServeIPFS starts, or nil.
	gateway *gateway
	// sourcify is the Sourcify server that ServeSourcify starts, or nil.
	sourcify *sourcify
}

// safe is the Safe that proposes the transactions, on both chains.
var safe = ethrpc.Address{0: 0x5a, 19: 0xfe}

// safeProxy is the code of the SafeProxy of Safe 1.3.0, which delegates calls
// to the singleton in its storage slot 0. The Safe has it on both nodes, with
// singleton, so that it is a Safe of a version that the Charter covers.
const safeProxy = "0x608060405273ffffffffffffffffffffffffffffffffffffffff600054167fa619486e0000000000000000000000000000000000000000000000000000000060003514156050578060005260206000f35b3660008037600080366000845af43d6000803e60008114156070573d6000fd5b3d6000f3fea2646970667358221220d1429297349653a4918076d650332de1a1068c5f3e07c5c82360c277770b955264736f6c63430007060033"

var singleton = ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552")

// NewTestnet starts a Gnosis Chain anvil node with the artifacts installed, and
// funds the sponsor, the sentinels, and the arbitrator. It also starts an
// Ethereum Mainnet node, whose genesis block is an hour earlier. Both have the
// Safe. It skips the test if anvil isn't installed, or in short mode.
func NewTestnet(tb testing.TB) *Testnet {
	tb.Helper()
	mainnet := StartAnvil(tb, ethrpc.Mainnet, "--prune-history=128", "--timestamp", fmt.Sprint(time.Now().Add(-time.Hour).Unix()))
	node := StartAnvil(tb, ethrpc.Gnosis)
	a := LoadArtifacts(tb)
	node.Install(a)
	var slot0 ethrpc.Hash
	copy(slot0[12:], singleton[:])
	for _, n := range []*Anvil{mainnet, node} {
		n.rpc(nil, "anvil_setCode", safe, safeProxy)
		n.rpc(nil, "anvil_setStorageAt", safe, ethrpc.Hash{}, slot0)
		for _, s := range []ethrpc.Address{a.SafeSingleton, a.SafeL2Singleton} {
			n.rpc(nil, "anvil_setCode", s, a.Accounts[s].Code)
		}
	}
	n := &Testnet{Anvil: node, Mainnet: mainnet, Artifacts: a, Sponsor: ethrpc.Address{0: 0x5f, 19: 1}}
	n.ArbitrationTimeout = n.callUint64("ARBITRATION_TIMEOUT()")

	// Fund the arbitrator with ether for gas, and the sponsor and sentinels with
	// fee tokens for fees and bonds, by depositing ether in WETH9.
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	funds := new(big.Int).Mul(big.NewInt(1_000_000), ether)
	n.SetBalance(a.Arbitrator, funds)
	for _, account := range append([]ethrpc.Address{n.Sponsor}, a.Sentinels...) {
		n.SetBalance(account, new(big.Int).Mul(funds, big.NewInt(2)))
		n.Receipt(n.Send(account, a.FeeToken, calldata("deposit()"), funds))
		n.Transact(account, a.FeeToken, calldata("approve(address,uint256)", a.Oracle, funds))
	}
	return n
}

// InstallFeeToken installs the fee token, WETH9, on node, which is Mainnet or
// another node that doesn't have the artifacts, at the same address as on
// Gnosis Chain.
func (n *Testnet) InstallFeeToken(node *Anvil) {
	n.tb.Helper()
	token := n.Artifacts.FeeToken
	account := n.Artifacts.Accounts[token]
	node.rpc(nil, "anvil_setCode", token, account.Code)
	for slot, value := range account.Storage {
		node.rpc(nil, "anvil_setStorageAt", token, slot, value)
	}
}

// TransferFeeToken has from wrap amount of ether in the fee token, then
// transfer it to to, on node, each in its own block, and returns the receipt of
// the transfer. The account gets the ether for it, and for gas.
func (n *Testnet) TransferFeeToken(node *Anvil, from, to ethrpc.Address, amount *big.Int) *Receipt {
	n.tb.Helper()
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	node.SetBalance(from, new(big.Int).Add(amount, ether))
	token := n.Artifacts.FeeToken
	node.Receipt(node.Send(from, token, calldata("deposit()"), amount))
	return node.Transact(from, token, calldata("transfer(address,uint256)", to, amount))
}

// SetupSafe makes the account at safe on node a Safe with the owners, a
// threshold of one, and no modules or fallback handler, which delegates to
// singleton, one of the Safe singletons in the artifacts. The account gets the
// code of a SafeProxy, and is set up in a transaction of the first owner.
func (n *Testnet) SetupSafe(node *Anvil, safe, singleton ethrpc.Address, owners ...ethrpc.Address) {
	n.tb.Helper()
	var slot0 ethrpc.Hash
	copy(slot0[12:], singleton[:])
	node.rpc(nil, "anvil_setCode", safe, safeProxy)
	node.rpc(nil, "anvil_setStorageAt", safe, ethrpc.Hash{}, slot0)
	node.Transact(owners[0], safe, calldata(
		"setup(address[],uint256,address,bytes,address,address,uint256,address)",
		owners, uint64(1), ethrpc.Address{}, []byte{}, ethrpc.Address{}, ethrpc.Address{}, uint64(0), ethrpc.Address{},
	))
}

// InstallForwarder installs at forwarder a contract that calls target with the
// calldata that it gets, and reverts if the call does, so that gas estimates
// for it cover the call. A forwarder that is a Safe's owner can execute its
// transactions for others.
func (n *Testnet) InstallForwarder(node *Anvil, forwarder, target ethrpc.Address) {
	n.tb.Helper()
	// CALLDATACOPY(0, 0, size), then CALL(gas, target, 0, 0, size, 0, 0), then STOP
	// if it succeeded, and REVERT(0, 0) if not.
	code := append([]byte{0x36, 0x60, 0x00, 0x60, 0x00, 0x37, 0x60, 0x00, 0x60, 0x00, 0x36, 0x60, 0x00, 0x60, 0x00, 0x73}, target[:]...)
	code = append(code, 0x5a, 0xf1, 0x15, 0x60, 0x2b, 0x57, 0x00, 0x5b, 0x60, 0x00, 0x60, 0x00, 0xfd)
	node.rpc(nil, "anvil_setCode", forwarder, ethrpc.Bytes(code))
}

// SafeNonce returns the nonce of the Safe at the latest block of node.
func (n *Testnet) SafeNonce(node *Anvil, safe ethrpc.Address) *big.Int {
	n.tb.Helper()
	result, err := node.Call(n.tb.Context(), ethrpc.CallRequest{To: safe, Data: calldata("nonce()")}, node.blockNumber())
	if err != nil {
		n.tb.Fatalf("nonce() of %s: %v", safe, err)
	}
	d := solabi.NewDecoder(result)
	nonce := d.Uint(0)
	if err := d.Err(); err != nil {
		n.tb.Fatalf("nonce() of %s: %v", safe, err)
	}
	return nonce
}

// ExecCalldata returns the calldata of a call to execTransaction on the Safe
// that executes tx, which must be for a Safe with a threshold of one and the
// caller as an owner. The call is signed by the owner with an approved-hash
// signature, which is valid when the owner is the caller. Its nonce is that of
// the Safe when the call is mined, not tx's.
func (n *Testnet) ExecCalldata(tx *safenet.SafeTransaction, owner ethrpc.Address) ethrpc.Bytes {
	signature := make([]byte, 65)
	copy(signature[12:], owner[:])
	signature[64] = 1
	return calldata(
		"execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)",
		tx.To, tx.Value, []byte(tx.Data), uint64(tx.Operation), tx.SafeTxGas, tx.BaseGas, tx.GasPrice, tx.GasToken, tx.RefundReceiver, signature,
	)
}

// Arbot returns an Arbot configured to read both nodes, the IPFS gateway of
// ServeIPFS if the test serves content, and the Sourcify server of
// ServeSourcify if the test serves contracts. It first mines Mainnet blocks up
// to the time of the latest Gnosis Chain block, so that arbot finds the Mainnet
// block before each proposal.
func (n *Testnet) Arbot() *Arbot {
	n.tb.Helper()
	n.Mainnet.MineUntil(n.Header(uint64(n.blockNumber())).Time())
	var gateway, sourcify string
	if n.gateway != nil {
		gateway = n.gateway.URL
	}
	if n.sourcify != nil {
		sourcify = n.sourcify.URL
	}
	return newArbot(n.tb, gateway, sourcify, n.Anvil, n.Mainnet)
}

// calldata returns the calldata for calling the function with signature, such
// as "transfer(address,uint256)", with args.
func calldata(signature string, args ...any) ethrpc.Bytes {
	return solabi.Call(solabi.Selector(signature), args...)
}

// callUint64 calls a function of the oracle without arguments, and decodes the
// uint64 it returns.
func (n *Testnet) callUint64(signature string) uint64 {
	n.tb.Helper()
	call := ethrpc.CallRequest{To: n.Artifacts.Oracle, Data: calldata(signature)}
	result, err := n.Call(n.tb.Context(), call, n.blockNumber())
	if err != nil {
		n.tb.Fatalf("%s: %v", signature, err)
	}
	d := solabi.NewDecoder(result)
	value := d.Uint64(0)
	if err := d.Err(); err != nil {
		n.tb.Fatalf("%s: %v", signature, err)
	}
	return value
}

// Request is a request that the test made, with what it expects the oracle and
// arbot to report about it.
type Request struct {
	n *Testnet

	ID ethrpc.Hash
	// Block and TxHash are the block and transaction of the proposal.
	Block      uint64
	TxHash     ethrpc.Hash
	OracleData []byte
	SafeTxHash ethrpc.Hash
	// Transaction is the proposed Safe transaction.
	Transaction                    *safenet.SafeTransaction
	CommitDeadline, RevealDeadline uint64
	// Ballots are the committed ballots, in commit order, and Revealed the
	// sentinels that revealed theirs.
	Ballots  []Ballot
	Revealed map[ethrpc.Address]bool
	// FrozenBlock is the block that finalizing the request froze it in.
	FrozenBlock uint64
	// Record is the receipt of the transaction that settled the arbitration, and
	// Context the arbitrator's context in it.
	Record  *Receipt
	Context string
}

// Ballot is a sentinel's vote on a request.
type Ballot struct {
	Request  *Request
	Sentinel ethrpc.Address
	Approve  bool
	Reason   string
}

// Approve returns a ballot by sentinel to approve the request.
func (r *Request) Approve(sentinel ethrpc.Address) Ballot {
	return Ballot{Request: r, Sentinel: sentinel, Approve: true}
}

// Deny returns a ballot by sentinel to deny the request, citing reason.
func (r *Request) Deny(sentinel ethrpc.Address, reason string) Ballot {
	return Ballot{Request: r, Sentinel: sentinel, Reason: reason}
}

// salt returns the ballot's commitment salt.
func (b Ballot) salt() ethrpc.Hash {
	return keccak256.Hash(b.Sentinel[:], b.Request.ID[:])
}

// Propose proposes a new Safe transaction with oracleData, in its own block.
func (n *Testnet) Propose(oracleData []byte) *Request {
	n.tb.Helper()
	tx := n.transaction()
	return n.proposed(n.Transact(n.Sponsor, n.Artifacts.Consensus, n.proposal(tx, oracleData)), tx, oracleData)
}

// ProposeInOneBlock proposes a new Safe transaction for each of the oracle
// data, all in the same block.
func (n *Testnet) ProposeInOneBlock(oracleData ...[]byte) []*Request {
	n.tb.Helper()
	txs := make([]*safenet.SafeTransaction, len(oracleData))
	calls := make([]call, len(oracleData))
	for i, data := range oracleData {
		txs[i] = n.transaction()
		calls[i] = call{n.Sponsor, n.Artifacts.Consensus, n.proposal(txs[i], data)}
	}
	receipts := n.transactInOneBlock(calls...)

	requests := make([]*Request, len(oracleData))
	for i, data := range oracleData {
		requests[i] = n.proposed(receipts[i], txs[i], data)
		if requests[i].Block != requests[0].Block {
			n.tb.Fatalf("proposals are in blocks %d and %d, want the same block", requests[0].Block, requests[i].Block)
		}
	}
	return requests
}

// transaction returns a new Safe transaction, which adds an owner to the Safe.
// Its fields vary with its nonce, so that every one of them is checked: every
// other one is a delegate call, and every third one is on Gnosis Chain instead
// of Ethereum Mainnet.
func (n *Testnet) transaction() *safenet.SafeTransaction {
	n.nonce++
	i := byte(n.nonce)
	chainID := int64(ethrpc.Mainnet)
	if i%3 == 0 {
		chainID = ethrpc.Gnosis
	}
	return &safenet.SafeTransaction{
		ChainID:        big.NewInt(chainID),
		Safe:           safe,
		To:             safe,
		Value:          big.NewInt(int64(i)),
		Data:           calldata("addOwnerWithThreshold(address,uint256)", ethrpc.Address{0: 0xbe, 19: i}, uint64(2)),
		Operation:      safenet.Operation(i % 2),
		SafeTxGas:      big.NewInt(1000 * int64(i)),
		BaseGas:        big.NewInt(100 * int64(i)),
		GasPrice:       big.NewInt(10 * int64(i)),
		GasToken:       ethrpc.Address{0: 0x70, 19: i},
		RefundReceiver: ethrpc.Address{0: 0x7e, 19: i},
		Nonce:          big.NewInt(int64(i)),
	}
}

// proposal returns the calldata for proposing tx to the oracle.
func (n *Testnet) proposal(tx *safenet.SafeTransaction, oracleData []byte) ethrpc.Bytes {
	return calldata(
		"proposeTransaction(address,bytes,(uint256,address,address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256))",
		n.Artifacts.Oracle,
		oracleData,
		solabi.Tuple{
			tx.ChainID, tx.Safe, tx.To, tx.Value, tx.Data, uint64(tx.Operation), tx.SafeTxGas,
			tx.BaseGas, tx.GasPrice, tx.GasToken, tx.RefundReceiver, tx.Nonce,
		},
	)
}

// proposed returns the request that proposing tx with oracleData made, given
// the proposal's receipt. Its ID and deadlines are those that the oracle
// logged.
func (n *Testnet) proposed(receipt *Receipt, tx *safenet.SafeTransaction, oracleData []byte) *Request {
	n.tb.Helper()
	proposal, ok := receipt.Log(n.Artifacts.Consensus, transactionProposedEvent)
	if !ok {
		n.tb.Fatalf("proposal %s has no TransactionProposed log", receipt.TransactionHash)
	}
	posted, ok := receipt.Log(n.Artifacts.Oracle, newRequestEvent)
	if !ok {
		n.tb.Fatalf("proposal %s has no NewRequest log", receipt.TransactionHash)
	}
	d := solabi.NewDecoder(posted.Data)
	r := &Request{
		n:              n,
		ID:             posted.Topics[1],
		Block:          uint64(receipt.BlockNumber),
		TxHash:         receipt.TransactionHash,
		OracleData:     oracleData,
		SafeTxHash:     proposal.Topics[1],
		Transaction:    tx,
		CommitDeadline: d.Uint64(4),
		RevealDeadline: d.Uint64(5),
		Revealed:       make(map[ethrpc.Address]bool),
	}
	if err := d.Err(); err != nil {
		n.tb.Fatalf("decoding NewRequest: %v", err)
	}
	return r
}

// Commit commits the ballots, all in one block, since the commit window is only
// a few blocks long.
func (n *Testnet) Commit(ballots ...Ballot) {
	n.tb.Helper()
	calls := make([]call, len(ballots))
	for i, b := range ballots {
		approve := []byte{0}
		if b.Approve {
			approve[0] = 1
		}
		salt := b.salt()
		hash := keccak256.Hash(approve, salt[:], b.Sentinel[:], b.Request.ID[:], []byte(b.Reason))
		calls[i] = call{b.Sentinel, n.Artifacts.Oracle, calldata("commit(bytes32,bytes32)", b.Request.ID, ethrpc.Hash(hash))}
		b.Request.Ballots = append(b.Request.Ballots, b)
	}
	n.transactInOneBlock(calls...)
}

// Vote commits the ballots, and reveals them all in one block once the commit
// window has closed. The oracle finalizes a request when the last committed
// vote on it is revealed, so the ballots must be every vote on their requests.
func (n *Testnet) Vote(ballots ...Ballot) {
	n.tb.Helper()
	n.Commit(ballots...)
	var commitDeadline uint64
	for _, b := range ballots {
		commitDeadline = max(commitDeadline, b.Request.CommitDeadline)
	}
	n.MineTo(commitDeadline + 1)

	calls := make([]call, len(ballots))
	for i, b := range ballots {
		calls[i] = call{b.Sentinel, n.Artifacts.Oracle,
			calldata("reveal(bytes32,bool,bytes32,string)", b.Request.ID, b.Approve, b.salt(), b.Reason)}
	}
	for i, receipt := range n.transactInOneBlock(calls...) {
		r := ballots[i].Request
		r.Revealed[ballots[i].Sentinel] = true
		r.finalized(receipt)
	}
}

// call is a transaction that from sends to to with data.
type call struct {
	from, to ethrpc.Address
	data     ethrpc.Bytes
}

// oneBlockGas is the gas limit of each transaction that transactInOneBlock
// sends, which is ample for any of them, and leaves room for several in a
// block.
const oneBlockGas = 2_000_000

// transactInOneBlock sends the calls, mines them in one block, in order, and
// returns their receipts. It fails the test if any of them reverted.
//
// The transactions have a fixed gas limit, since anvil estimates the gas of
// each against the state before the block, without the transactions before it.
// For example, the last reveal of a request also finalizes it, which its
// estimate leaves out.
func (n *Testnet) transactInOneBlock(calls ...call) []*Receipt {
	n.tb.Helper()
	hashes := make([]ethrpc.Hash, len(calls))
	n.SetAutomine(false)
	for i, c := range calls {
		tx := map[string]any{"from": c.from, "to": c.to, "data": c.data, "gas": ethrpc.Quantity(oneBlockGas)}
		n.rpc(&hashes[i], "eth_sendTransaction", tx)
	}
	n.MinePending()
	n.SetAutomine(true)
	receipts := make([]*Receipt, len(calls))
	for i, hash := range hashes {
		receipts[i] = n.Receipt(hash)
	}
	return receipts
}

// Finalize finalizes the request, recording the block if it freezes it.
func (r *Request) Finalize() {
	r.n.tb.Helper()
	r.finalized(r.n.Transact(r.n.Sponsor, r.n.Artifacts.Oracle, calldata("finalize(bytes32)", r.ID)))
}

// finalized records the block of the receipt if it froze the request.
func (r *Request) finalized(receipt *Receipt) {
	if _, ok := receipt.Log(r.n.Artifacts.Oracle, disputeTriggeredEvent); ok {
		r.FrozenBlock = uint64(receipt.BlockNumber)
	}
}

// Resolve rules on the frozen request as the arbitrator: secure if approveWins,
// and insecure otherwise.
func (r *Request) Resolve(approveWins bool, context string) {
	r.n.tb.Helper()
	r.settle(r.n.Artifacts.Arbitrator, calldata("resolveDispute(bytes32,bool,string)", r.ID, approveWins, context), context)
}

// MarkOutOfScope declines to rule on the frozen request as the arbitrator.
func (r *Request) MarkOutOfScope(context string) {
	r.n.tb.Helper()
	r.settle(r.n.Artifacts.Arbitrator, calldata("markOutOfScope(bytes32,string)", r.ID, context), context)
}

// TimeoutArbitration times out the arbitration of the frozen request, which
// must be past its deadline.
func (r *Request) TimeoutArbitration() {
	r.n.tb.Helper()
	r.settle(r.n.Sponsor, calldata("timeoutArbitration(bytes32)", r.ID), "")
}

// settle sends the transaction that settles the arbitration, and records its
// receipt and context.
func (r *Request) settle(from ethrpc.Address, data ethrpc.Bytes, context string) {
	r.n.tb.Helper()
	r.Record, r.Context = r.n.Transact(from, r.n.Artifacts.Oracle, data), context
}
