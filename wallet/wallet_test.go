package wallet_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
)

func waitForBlock(tb testing.TB, cm *chain.Manager, ws wallet.Store) {
	for i := 0; i < 1000; i++ {
		time.Sleep(10 * time.Millisecond)
		tip, _ := ws.LastCommittedIndex()
		if tip == cm.Tip() {
			return
		}
	}
	tb.Fatal("timed out waiting for block")
}

func TestResubscribe(t *testing.T) {
	log := zaptest.NewLogger(t)
	dir := t.TempDir()
	db, err := sqlite.OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	// mine a single payout to the wallet
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	network, genesisBlock := testutil.Network()
	// send the siafunds to the owned address
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = addr

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, log.Named("wallet"))
	if err != nil {
		t.Fatal(err)
	}

	pk2 := types.GeneratePrivateKey()
	addr2 := types.StandardUnlockHash(pk2.PublicKey())

	// create a wallet with no addresses
	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}

	// add the address to the wallet
	if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	checkBalance := func(siacoin, immature types.Currency) error {
		waitForBlock(t, cm, db)

		// note: the siafund balance is currently hardcoded to the number of
		// siafunds in genesis. If we ever modify this test to also spend
		// siafunds, this will need to be updated.
		b, err := wm.WalletBalance(w.ID)
		if err != nil {
			return fmt.Errorf("failed to check balance: %w", err)
		} else if !b.Siacoins.Equals(siacoin) {
			return fmt.Errorf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			return fmt.Errorf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		} else if b.Siafunds != network.GenesisState().SiafundCount() {
			return fmt.Errorf("expected siafund balance %v, got %v", network.GenesisState().SiafundCount(), b.Siafunds)
		}
		return nil
	}

	// check that the wallet has no balance
	if err := checkBalance(types.ZeroCurrency, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	expectedBalance1 := cm.TipState().BlockReward()
	// mine a block to fund the first address
	if err := cm.AddBlocks([]types.Block{testutil.MineBlock(cm, addr)}); err != nil {
		t.Fatal(err)
	}

	// mine a block to fund the second address
	expectedBalance2 := cm.TipState().BlockReward()
	if err := cm.AddBlocks([]types.Block{testutil.MineBlock(cm, addr2)}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet has one immature payout
	if err := checkBalance(types.ZeroCurrency, expectedBalance1); err != nil {
		t.Fatal(err)
	}

	// mine until the first payout matures
	for i := cm.Tip().Height; i < genesisState.MaturityHeight(); i++ {
		if err := cm.AddBlocks([]types.Block{testutil.MineBlock(cm, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// scan for changes
	if err := wm.Scan(types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet balance did not change
	if err := checkBalance(expectedBalance1, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// add the second address to the wallet
	if err := wm.AddAddress(w.ID, wallet.Address{Address: addr2}); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(expectedBalance1, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// scan for changes
	if err := wm.Scan(types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	if err := checkBalance(expectedBalance1, expectedBalance2); err != nil {
		t.Fatal(err)
	}

	// mine a block to mature the second payout
	if err := cm.AddBlocks([]types.Block{testutil.MineBlock(cm, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1.Add(expectedBalance2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// sanity check
	if err := wm.Scan(types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1.Add(expectedBalance2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}
}

func TestSiafunds(t *testing.T) {
	log := zaptest.NewLogger(t)
	dir := t.TempDir()
	db, err := sqlite.OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	// mine a single payout to the wallet
	pk := types.GeneratePrivateKey()
	addr1 := types.StandardUnlockHash(pk.PublicKey())

	network, genesisBlock := testutil.Network()
	// send the siafunds to the owned address
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = addr1

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, log.Named("wallet"))
	if err != nil {
		t.Fatal(err)
	}

	pk2 := types.GeneratePrivateKey()
	addr2 := types.StandardUnlockHash(pk2.PublicKey())

	// create a wallet with no addresses
	w1, err := wm.AddWallet(wallet.Wallet{Name: "test1"})
	if err != nil {
		t.Fatal(err)
	}

	// add the address to the wallet
	if err := wm.AddAddress(w1.ID, wallet.Address{Address: addr1}); err != nil {
		t.Fatal(err)
	}

	checkBalance := func(walletID wallet.ID, siafunds uint64) error {
		waitForBlock(t, cm, db)

		b, err := wm.WalletBalance(walletID)
		if err != nil {
			return fmt.Errorf("failed to check balance: %w", err)
		} else if b.Siafunds != siafunds {
			return fmt.Errorf("expected siafund balance %v, got %v", siafunds, b.Siafunds)
		}
		return nil
	}

	if err := wm.Scan(types.ChainIndex{}); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(w1.ID, network.GenesisState().SiafundCount()); err != nil {
		t.Fatal(err)
	}

	// split the siafunds between the two addresses
	sendAmount := network.GenesisState().SiafundCount() / 2
	parentID := genesisBlock.Transactions[0].SiafundOutputID(0)
	txn := types.Transaction{
		SiafundInputs: []types.SiafundInput{
			{
				ParentID:         parentID,
				UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
			},
		},
		SiafundOutputs: []types.SiafundOutput{
			{Address: addr2, Value: sendAmount},
			{Address: addr1, Value: sendAmount},
		},
		Signatures: []types.TransactionSignature{
			{
				ParentID:      types.Hash256(parentID),
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			},
		},
	}
	state := cm.TipState()
	sigHash := state.WholeSigHash(txn, txn.Signatures[0].ParentID, 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures[0].Signature = sig[:]

	if _, err := cm.AddPoolTransactions([]types.Transaction{txn}); err != nil {
		t.Fatal(err)
	} else if err := cm.AddBlocks([]types.Block{testutil.MineBlock(cm, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(w1.ID, sendAmount); err != nil {
		t.Fatal(err)
	}

	// rescan for sanity check
	if err := wm.Scan(types.ChainIndex{}); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(w1.ID, sendAmount); err != nil {
		t.Fatal(err)
	}

	// add a second wallet
	w2, err := wm.AddWallet(wallet.Wallet{Name: "test2"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w2.ID, wallet.Address{Address: addr2}); err != nil {
		t.Fatal(err)
	}

	// wallet should have no balance since it hasn't been scanned
	if err := checkBalance(w2.ID, 0); err != nil {
		t.Fatal(err)
	}

	// rescan for the second wallet
	if err := wm.Scan(types.ChainIndex{}); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(w2.ID, sendAmount); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(w1.ID, sendAmount); err != nil {
		t.Fatal(err)
	}

	// add the first address to the second wallet
	if err := wm.AddAddress(w2.ID, wallet.Address{Address: addr1}); err != nil {
		t.Fatal(err)
	}
	// rescan shouldn't be necessary since the address was already scanned
	if err := checkBalance(w2.ID, network.GenesisState().SiafundCount()); err != nil {
		t.Fatal(err)
	}
}
