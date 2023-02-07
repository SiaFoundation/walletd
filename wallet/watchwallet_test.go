package wallet_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/wallet"
	"lukechampine.com/frand"
)

func TestWatchWallet(t *testing.T) {
	store := walletutil.NewEphemeralStore()
	w := wallet.NewWatchWallet(store)
	cs := new(mockCS)
	ccid, err := store.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs.ConsensusSetSubscribe(store, ccid, nil)

	uc := wallet.StandardUnlockConditions(wallet.NewSeed().PublicKey(0))
	addr := uc.UnlockHash()
	if err := w.AddAddress(uc); err != nil {
		panic(err)
	}
	// initial balance should be zero
	sc, _, err := w.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if !sc.IsZero() {
		t.Fatal("balance should be zero")
	}

	// shouldn't have any transactions yet
	txnHistory, err := w.Transactions(time.Time{}, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(txnHistory) != 0 {
		t.Fatal("transaction history should be empty")
	}

	// should have an address now
	addresses, err := w.Addresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != addr {
		t.Fatal("bad address list", addresses)
	}

	// simulate a transaction
	cs.sendTxn(types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
		},
	})

	// get new balance
	sc, _, err = w.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC")
	}

	// transaction should appear in history
	txnHistory, err = w.TransactionsByAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(txnHistory) != 1 {
		t.Fatal("transaction should appear in history")
	}
	htx, err := w.Transaction(txnHistory[0].ID)
	if err != nil {
		t.Fatal(err)
	} else if len(htx.Raw.SiacoinOutputs) != 2 {
		t.Fatal("transaction should have two outputs")
	} else if htx.Index.Height != 1 {
		t.Fatal("transaction height should be 1")
	}

	outputs, err := w.UnspentSiacoinOutputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}
}

func TestWatchWalletThreadSafety(t *testing.T) {
	store := walletutil.NewEphemeralStore()
	w := wallet.NewWatchWallet(store)
	cs := new(mockCS)
	ccid, err := store.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs.ConsensusSetSubscribe(store, ccid, nil)

	uc := wallet.StandardUnlockConditions(wallet.NewSeed().PublicKey(0))
	addr := uc.UnlockHash()
	txn := types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
		},
	}

	// create a bunch of goroutines that call routes and add transactions
	// concurrently
	funcs := []func(){
		func() { w.AddAddress(uc) },
		func() { cs.sendTxn(txn) },
		func() { w.Balance() },
		func() { w.Address() },
		func() { w.Addresses() },
		func() { w.Transactions(time.Time{}, math.MaxInt64) },
	}
	var wg sync.WaitGroup
	wg.Add(len(funcs))
	for _, fn := range funcs {
		go func(fn func()) {
			for i := 0; i < 10; i++ {
				time.Sleep(time.Duration(frand.Intn(10)) * time.Millisecond)
				fn()
			}
			wg.Done()
		}(fn)
	}
	wg.Wait()
}
