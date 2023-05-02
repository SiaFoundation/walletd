package api_test

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"go.sia.tech/core/chain"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/syncer"
	"go.sia.tech/walletd/wallet"
)

type mockChainManager struct {
	sub chain.Subscriber
}

func (mockChainManager) TipState() (cs consensus.State)                           { return }
func (mockChainManager) RecommendedFee() (fee types.Currency)                     { return }
func (mockChainManager) PoolTransactions() []types.Transaction                    { return nil }
func (mockChainManager) AddPoolTransactions([]types.Transaction) error            { return nil }
func (mockChainManager) UnconfirmedParents(types.Transaction) []types.Transaction { return nil }
func (mockChainManager) BestIndex(height uint64) (i types.ChainIndex, ok bool)    { return i, true }

func (cm *mockChainManager) AddSubscriber(s chain.Subscriber, index types.ChainIndex) error {
	cm.sub = s
	return nil
}

func (cm *mockChainManager) sendTxn(txn types.Transaction) {
	created := make([]consensus.SiacoinOutputDiff, len(txn.SiacoinOutputs))
	for i := range created {
		created[i] = consensus.SiacoinOutputDiff{
			ID:     txn.SiacoinOutputID(i),
			Output: txn.SiacoinOutputs[i],
		}
	}
	spent := make([]consensus.SiacoinOutputDiff, len(txn.SiacoinInputs))
	for i := range spent {
		spent[i] = consensus.SiacoinOutputDiff{
			ID: txn.SiacoinInputs[i].ParentID,
			Output: types.SiacoinOutput{
				Value:   types.ZeroCurrency,
				Address: txn.SiacoinInputs[i].UnlockConditions.UnlockHash(),
			},
		}
	}
	cm.sub.ProcessChainApplyUpdate(&chain.ApplyUpdate{
		Block: types.Block{
			Timestamp:    types.CurrentTimestamp(),
			Transactions: []types.Transaction{txn},
		},
		Diff: consensus.BlockDiff{
			Transactions: []consensus.TransactionDiff{{
				CreatedSiacoinOutputs: created,
				SpentSiacoinOutputs:   spent,
			}},
		},
	}, true)
}

type mockSyncer struct{}

func (mockSyncer) Addr() string                                     { return "" }
func (mockSyncer) Peers() []*gateway.Peer                           { return nil }
func (mockSyncer) PeerInfo(string) (i syncer.PeerInfo, ok bool)     { return }
func (mockSyncer) Connect(addr string) (*gateway.Peer, error)       { return nil, nil }
func (mockSyncer) BroadcastTransactionSet(txns []types.Transaction) {}

func runServer(wm api.WalletManager) (*api.Client, func()) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	go func() {
		srv := api.NewServer(mockChainManager{}, mockSyncer{}, wm)
		http.Serve(l, jape.BasicAuth("password")(srv))
	}()
	c := api.NewClient("http://"+l.Addr().String(), "password")
	return c, func() { l.Close() }
}

func TestWallet(t *testing.T) {
	var cm mockChainManager
	wm := walletutil.NewEphemeralWalletManager(&cm)
	sav := wallet.NewSeedAddressVault(wallet.NewSeed(), 0, 20)

	c, shutdown := runServer(wm)
	defer shutdown()

	if err := c.AddWallet("primary", json.RawMessage(`{"type":"seed"}`)); err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet("primary")
	if err := wc.Subscribe(0); err != nil {
		t.Fatal(err)
	}

	balance, err := wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.IsZero() || balance.Siafunds != 0 {
		t.Fatal("balance should be 0")
	}

	// shouldn't have any events yet
	events, err := wc.Events(time.Time{}, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatal("event history should be empty")
	}

	// shouldn't have any addresses yet
	addresses, err := wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 0 {
		t.Fatal("address list should be empty")
	}

	// create and add an address
	addr, info := sav.NewAddress("primary")
	if err := wc.AddAddress(addr, info); err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if _, ok := addresses[addr]; !ok || len(addresses) != 1 {
		t.Fatal("bad address list", addresses)
	}

	// simulate a transaction
	cm.sendTxn(types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
		},
	})

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC")
	}

	// transaction should appear in history
	events, err = wc.Events(time.Time{}, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	} else if len(events) == 0 {
		t.Fatal("transaction should appear in history")
	}

	outputs, _, err := wc.Outputs()
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}
}
