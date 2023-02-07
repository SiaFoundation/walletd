package api_test

import (
	"bytes"
	"fmt"
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/encoding"
	ctypes "go.sia.tech/core/types"
	"go.sia.tech/jape"
	"go.sia.tech/siad/modules"
	stypes "go.sia.tech/siad/types"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/wallet"
	"lukechampine.com/frand"
)

func coreConvertToSiad(from ctypes.EncoderTo, to interface{}) {
	var buf bytes.Buffer
	e := ctypes.NewEncoder(&buf)
	from.EncodeTo(e)
	e.Flush()
	if err := encoding.Unmarshal(buf.Bytes(), to); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

type mockChainManager struct{}

func (mockChainManager) TipState() (cs api.ConsensusState) { return }

type mockSyncer struct{}

func (mockSyncer) Addr() string              { return "" }
func (mockSyncer) Peers() []string           { return nil }
func (mockSyncer) Connect(addr string) error { return nil }
func (mockSyncer) BroadcastTransaction(txn ctypes.Transaction, dependsOn []ctypes.Transaction) {
}

type mockTxPool struct{}

func (mockTxPool) RecommendedFee() ctypes.Currency                   { return ctypes.Currency{} }
func (mockTxPool) Transactions() []ctypes.Transaction                { return nil }
func (mockTxPool) AddTransactionSet(txns []ctypes.Transaction) error { return nil }
func (mockTxPool) UnconfirmedParents(txn ctypes.Transaction) ([]ctypes.Transaction, error) {
	return nil, nil
}

type mockCS struct {
	subscriber    modules.ConsensusSetSubscriber
	dscos         map[stypes.BlockHeight][]modules.DelayedSiacoinOutputDiff
	filecontracts map[stypes.FileContractID]stypes.FileContract
	height        stypes.BlockHeight
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscriber = s
	return nil
}

func (m *mockCS) sendTxn(cTxn ctypes.Transaction) {
	var txn stypes.Transaction
	coreConvertToSiad(cTxn, &txn)

	outputs := make([]modules.SiacoinOutputDiff, len(txn.SiacoinOutputs))
	for i := range outputs {
		outputs[i] = modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: txn.SiacoinOutputs[i],
			ID:            txn.SiacoinOutputID(uint64(i)),
		}
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []stypes.Block{{
			Transactions: []stypes.Transaction{txn},
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
	if !balance.Siacoins.IsZero() || balance.Siafunds != 0 {
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
	cs.sendTxn(ctypes.Transaction{
		SiacoinOutputs: []ctypes.SiacoinOutput{
			{Address: addr, Value: ctypes.Siacoins(1).Div64(2)},
			{Address: addr, Value: ctypes.Siacoins(1).Div64(2)},
		},
	})

	// get new balance
	balance, err = c.WalletBalance()
	if err != nil {
		t.Fatal(err)
	}
	if !balance.Siacoins.Equals(ctypes.Siacoins(1)) {
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
