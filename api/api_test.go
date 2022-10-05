package api_test

import (
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"go.sia.tech/jape"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/wallet"
	"lukechampine.com/frand"
)

type mockChainManager struct{}

func (mockChainManager) TipState() (cs api.ConsensusState) { return }

type mockSyncer struct{}

func (mockSyncer) Addr() string              { return "" }
func (mockSyncer) Peers() []string           { return nil }
func (mockSyncer) Connect(addr string) error { return nil }
func (mockSyncer) BroadcastTransaction(txn types.Transaction, dependsOn []types.Transaction) {
}

type mockTxPool struct{}

func (mockTxPool) RecommendedFee() types.Currency                   { return types.ZeroCurrency }
func (mockTxPool) Transactions() []types.Transaction                { return nil }
func (mockTxPool) AddTransactionSet(txns []types.Transaction) error { return nil }
func (mockTxPool) UnconfirmedParents(txn types.Transaction) ([]types.Transaction, error) {
	return nil, nil
}

type mockCS struct {
	subscriber    modules.ConsensusSetSubscriber
	dscos         map[types.BlockHeight][]modules.DelayedSiacoinOutputDiff
	filecontracts map[types.FileContractID]types.FileContract
	height        types.BlockHeight
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscriber = s
	return nil
}

func (m *mockCS) sendTxn(txn types.Transaction) {
	outputs := make([]modules.SiacoinOutputDiff, len(txn.SiacoinOutputs))
	for i := range outputs {
		outputs[i] = modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: txn.SiacoinOutputs[i],
			ID:            txn.SiacoinOutputID(uint64(i)),
		}
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{{
			Transactions: []types.Transaction{txn},
		}},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			SiacoinOutputDiffs: outputs,
		},
		ID: frand.Entropy256(),
	}
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
}

type node struct {
	w *wallet.HotWallet
}

func runServer(n *node) (*api.Client, func()) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	go func() {
		srv := api.NewServer(mockChainManager{}, mockSyncer{}, mockTxPool{}, n.w)
		http.Serve(l, jape.AuthMiddleware(srv, "password"))
	}()
	c := api.NewClient("http://"+l.Addr().String(), "password")
	return c, func() { l.Close() }
}

func TestWallet(t *testing.T) {
	store := walletutil.NewEphemeralStore()
	ccid, err := store.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(store, ccid, nil)
	n := &node{wallet.NewHotWallet(store, wallet.NewSeed())}

	c, shutdown := runServer(n)
	defer shutdown()

	balance, err := c.WalletBalance()
	if err != nil {
		t.Fatal(err)
	}
	if !balance.Siacoins.IsZero() || !balance.Siafunds.IsZero() {
		t.Fatal("balance should be 0")
	}

	// shouldn't have any transactions yet
	txnHistory, err := c.WalletTransactions(time.Time{}, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(txnHistory) != 0 {
		t.Fatal("transaction history should be empty")
	}

	// shouldn't have any addresses yet
	addresses, err := c.WalletAddresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 0 {
		t.Fatal("address list should be empty")
	}

	// create and add an address
	addr, err := c.WalletAddress()
	if err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = c.WalletAddresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != addr {
		t.Fatal("bad address list", addresses)
	}

	// simulate a transaction
	cs.sendTxn(types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	})

	// get new balance
	balance, err = c.WalletBalance()
	if err != nil {
		t.Fatal(err)
	}
	if !balance.Siacoins.Equals(types.SiacoinPrecision) {
		t.Fatal("balance should be 1 SC")
	}

	// transaction should appear in history
	txnHistory, err = c.WalletTransactionsAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(txnHistory) != 1 {
		t.Fatal("transaction should appear in history")
	}

	outputs, err := c.WalletOutputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}
}
