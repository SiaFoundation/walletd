package sqlite

import (
	"path/filepath"
	"testing"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
)

func mineBlock(state consensus.State, txns []types.Transaction, minerAddr types.Address) types.Block {
	b := types.Block{
		ParentID:     state.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		Transactions: txns,
		MinerPayouts: []types.SiacoinOutput{{Address: minerAddr, Value: state.BlockReward()}},
	}
	for b.ID().CmpWork(state.ChildTarget) < 0 {
		b.Nonce += state.NonceFactor()
	}
	return b
}

func syncDB(tb testing.TB, store *Store, cm *chain.Manager) {
	index, err := store.LastCommittedIndex()
	if err != nil {
		tb.Fatalf("failed to get last committed index: %v", err)
	}
	for index != cm.Tip() {
		crus, caus, err := cm.UpdatesSince(index, 1000)
		if err != nil {
			tb.Fatalf("failed to subscribe to chain manager: %v", err)
		} else if err := store.UpdateChainState(crus, caus); err != nil {
			tb.Fatalf("failed to update chain state: %v", err)
		}

		switch {
		case len(caus) > 0:
			index = caus[len(caus)-1].State.Index
		case len(crus) > 0:
			index = crus[len(crus)-1].State.Index
		}
	}
}

func TestPruneSiacoins(t *testing.T) {
	log := zaptest.NewLogger(t)
	dir := t.TempDir()
	db, err := OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
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
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	// create a wallet
	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	// mine a block to the wallet
	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	assertBalance := func(siacoin, immature types.Currency) {
		t.Helper()

		b, err := db.WalletBalance(w.ID)
		if err != nil {
			t.Fatalf("failed to get wallet balance: %v", err)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			t.Fatalf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		} else if !b.Siacoins.Equals(siacoin) {
			t.Fatalf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		}
	}

	assertUTXOs := func(spent int, unspent int) {
		t.Helper()

		var n int
		err := db.db.QueryRow(`SELECT COUNT(*) FROM siacoin_elements WHERE spent_index_id IS NOT NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count spent siacoin elements: %v", err)
		} else if n != spent {
			t.Fatalf("expected %v spent siacoin elements, got %v", spent, n)
		}

		err = db.db.QueryRow(`SELECT COUNT(*) FROM siacoin_elements WHERE spent_index_id IS NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count unspent siacoin elements: %v", err)
		} else if n != unspent {
			t.Fatalf("expected %v unspent siacoin elements, got %v", unspent, n)
		}
	}

	assertBalance(types.ZeroCurrency, expectedPayout)
	assertUTXOs(0, 1)

	// mine until the payout matures
	for i := 0; i < int(maturityHeight); i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	syncDB(t, db, cm)
	assertBalance(expectedPayout, types.ZeroCurrency)
	assertUTXOs(0, 1)

	// spend the utxo
	utxos, err := db.WalletSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatalf("failed to get wallet siacoin outputs: %v", err)
	}

	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         types.SiacoinOutputID(utxos[0].ID),
			UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Value: utxos[0].SiacoinOutput.Value, Address: types.VoidAddress},
		},
	}

	sigHash := cm.TipState().WholeSigHash(txn, utxos[0].ID, 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures = append(txn.Signatures, types.TransactionSignature{
		ParentID:       utxos[0].ID,
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
		Timelock:       0,
		Signature:      sig[:],
	})

	// mine a block with the transaction
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), []types.Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// the utxo should now have 0 balance and 1 spent element
	assertBalance(types.ZeroCurrency, types.ZeroCurrency)
	assertUTXOs(1, 0)

	// mine until the element is pruned
	for i := 0; i < spentElementRetentionBlocks-1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
		syncDB(t, db, cm)
		assertUTXOs(1, 0) // check that the element is not pruned early
	}

	// trigger the pruning
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)
	assertUTXOs(0, 0)
}

func TestPruneSiafunds(t *testing.T) {
	log := zaptest.NewLogger(t)
	dir := t.TempDir()
	db, err := OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
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
	// send the siafund airdrop to the wallet
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = addr
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	// create a wallet
	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	syncDB(t, db, cm)

	assertBalance := func(siafunds uint64) {
		t.Helper()

		b, err := db.WalletBalance(w.ID)
		if err != nil {
			t.Fatalf("failed to get wallet balance: %v", err)
		} else if b.Siafunds != siafunds {
			t.Fatalf("expected siafund balance %v, got %v", siafunds, b.ImmatureSiacoins)
		}
	}

	assertUTXOs := func(spent int, unspent int) {
		t.Helper()

		var n int
		err := db.db.QueryRow(`SELECT COUNT(*) FROM siafund_elements WHERE spent_index_id IS NOT NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count spent siacoin elements: %v", err)
		} else if n != spent {
			t.Fatalf("expected %v spent siacoin elements, got %v", spent, n)
		}

		err = db.db.QueryRow(`SELECT COUNT(*) FROM siafund_elements WHERE spent_index_id IS NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count unspent siacoin elements: %v", err)
		} else if n != unspent {
			t.Fatalf("expected %v unspent siacoin elements, got %v", unspent, n)
		}
	}

	assertBalance(cm.TipState().SiafundCount())
	assertUTXOs(0, 1)

	// spend the utxo
	utxos, err := db.WalletSiafundOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatalf("failed to get wallet siacoin outputs: %v", err)
	}

	txn := types.Transaction{
		SiafundInputs: []types.SiafundInput{{
			ParentID:         types.SiafundOutputID(utxos[0].ID),
			UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
		}},
		SiafundOutputs: []types.SiafundOutput{
			{Value: utxos[0].SiafundOutput.Value, Address: types.VoidAddress},
		},
	}

	sigHash := cm.TipState().WholeSigHash(txn, utxos[0].ID, 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures = append(txn.Signatures, types.TransactionSignature{
		ParentID:       utxos[0].ID,
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
		Timelock:       0,
		Signature:      sig[:],
	})

	// mine a block with the transaction
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), []types.Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// the utxo should now have 0 balance and 1 spent element
	assertBalance(0)
	assertUTXOs(1, 0)

	// mine until the element is pruned
	for i := 0; i < spentElementRetentionBlocks-1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
		syncDB(t, db, cm) // check that the element is not pruned early
		assertUTXOs(1, 0)
	}

	// the spent element should now be pruned
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)
	assertUTXOs(0, 0)
}
