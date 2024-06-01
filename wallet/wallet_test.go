package wallet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
)

func waitForBlock(tb testing.TB, cm *chain.Manager, ws wallet.Store) {
	tb.Helper()
	for i := 0; i < 1000; i++ {
		time.Sleep(10 * time.Millisecond)
		tip, _ := ws.LastCommittedIndex()
		if tip == cm.Tip() {
			return
		}
	}
	tb.Fatal("timed out waiting for block")
}

func testV1Network(siafundAddr types.Address) (*consensus.Network, types.Block) {
	// use a modified version of Zen
	n, genesisBlock := chain.TestnetZen()
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = siafundAddr
	n.InitialTarget = types.BlockID{0xFF}
	n.HardforkDevAddr.Height = 1
	n.HardforkTax.Height = 1
	n.HardforkStorageProof.Height = 1
	n.HardforkOak.Height = 1
	n.HardforkASIC.Height = 1
	n.HardforkFoundation.Height = 1
	n.HardforkV2.AllowHeight = 1000
	n.HardforkV2.RequireHeight = 1000
	return n, genesisBlock
}

func testV2Network(siafundAddr types.Address) (*consensus.Network, types.Block) {
	// use a modified version of Zen
	n, genesisBlock := chain.TestnetZen()
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = siafundAddr
	n.InitialTarget = types.BlockID{0xFF}
	n.HardforkDevAddr.Height = 1
	n.HardforkTax.Height = 1
	n.HardforkStorageProof.Height = 1
	n.HardforkOak.Height = 1
	n.HardforkASIC.Height = 1
	n.HardforkFoundation.Height = 1
	n.HardforkV2.AllowHeight = 100
	n.HardforkV2.RequireHeight = 110
	return n, genesisBlock
}

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

func mineV2Block(state consensus.State, txns []types.V2Transaction, minerAddr types.Address) types.Block {
	b := types.Block{
		ParentID:     state.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: minerAddr, Value: state.BlockReward()}},

		V2: &types.V2BlockData{
			Transactions: txns,
			Height:       state.Index.Height + 1,
		},
	}
	b.V2.Commitment = state.Commitment(state.TransactionsCommitment(b.Transactions, b.V2Transactions()), b.MinerPayouts[0].Address)
	for b.ID().CmpWork(state.ChildTarget) < 0 {
		b.Nonce += state.NonceFactor()
	}
	return b
}

func TestReorg(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	setupNode := func(t *testing.T, mode wallet.IndexMode) (consensus.State, *sqlite.Store, *chain.Manager, *wallet.Manager) {
		t.Helper()

		log := zaptest.NewLogger(t)
		dir := t.TempDir()
		db, err := sqlite.OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })

		bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { bdb.Close() })

		network, genesisBlock := testV1Network(types.VoidAddress) // don't care about siafunds

		store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
		if err != nil {
			t.Fatal(err)
		}
		cm := chain.NewManager(store, genesisState)

		wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(mode))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { wm.Close() })
		return genesisState, db, cm, wm
	}

	testReorg := func(t *testing.T, genesisState consensus.State, db *sqlite.Store, cm *chain.Manager, wm *wallet.Manager) {
		w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
		if err != nil {
			t.Fatal(err)
		} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
			t.Fatal(err)
		}

		expectedPayout := cm.TipState().BlockReward()
		maturityHeight := cm.TipState().MaturityHeight()
		// mine a block sending the payout to the wallet
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
			t.Fatal(err)
		}
		waitForBlock(t, cm, db)

		assertBalance := func(siacoin, immature types.Currency) error {
			b, err := wm.WalletBalance(w.ID)
			if err != nil {
				return fmt.Errorf("failed to check balance: %w", err)
			} else if !b.Siacoins.Equals(siacoin) {
				return fmt.Errorf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
			} else if !b.ImmatureSiacoins.Equals(immature) {
				return fmt.Errorf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
			}
			return nil
		}

		if err := assertBalance(types.ZeroCurrency, expectedPayout); err != nil {
			t.Fatal(err)
		}

		// check that a payout event was recorded
		events, err := wm.Events(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(events) != 1 {
			t.Fatalf("expected 1 event, got %v", len(events))
		} else if events[0].Type != wallet.EventTypeMinerPayout {
			t.Fatalf("expected payout event, got %v", events[0].Type)
		}

		// check that the utxo was created
		utxos, err := wm.UnspentSiacoinOutputs(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) != 1 {
			t.Fatalf("expected 1 output, got %v", len(utxos))
		} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
			t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
		} else if utxos[0].MaturityHeight != maturityHeight {
			t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
		}

		// mine to trigger a reorg
		var blocks []types.Block
		state := genesisState
		for i := 0; i < 10; i++ {
			block := mineBlock(state, nil, types.VoidAddress)
			blocks = append(blocks, block)
			state.Index.ID = block.ID()
			state.Index.Height++
		}
		if err := cm.AddBlocks(blocks); err != nil {
			t.Fatal(err)
		}
		waitForBlock(t, cm, db)

		// check that the balance was reverted
		if err := assertBalance(types.ZeroCurrency, types.ZeroCurrency); err != nil {
			t.Fatal(err)
		}

		// check that the payout event was reverted
		events, err = wm.Events(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(events) != 0 {
			t.Fatalf("expected 0 events, got %v", len(events))
		}

		// check that the utxo was removed
		utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) != 0 {
			t.Fatalf("expected 0 outputs, got %v", len(utxos))
		}

		// mine a new payout
		expectedPayout = cm.TipState().BlockReward()
		maturityHeight = cm.TipState().MaturityHeight()
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
			t.Fatal(err)
		}
		waitForBlock(t, cm, db)

		// check that the payout was received
		if err := assertBalance(types.ZeroCurrency, expectedPayout); err != nil {
			t.Fatal(err)
		}

		// check that a payout event was recorded
		events, err = wm.Events(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(events) != 1 {
			t.Fatalf("expected 1 event, got %v", len(events))
		} else if events[0].Type != wallet.EventTypeMinerPayout {
			t.Fatalf("expected payout event, got %v", events[0].Type)
		}

		// check that the utxo was created
		utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) != 1 {
			t.Fatalf("expected 1 output, got %v", len(utxos))
		} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
			t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
		} else if utxos[0].MaturityHeight != maturityHeight {
			t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
		}

		// mine until the payout matures
		var prevState consensus.State
		for i := cm.TipState().Index.Height; i < maturityHeight+1; i++ {
			if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
				t.Fatal(err)
			}
			if i == maturityHeight-5 {
				prevState = cm.TipState()
			}
		}
		waitForBlock(t, cm, db)

		// check that the balance was updated
		if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
			t.Fatal(err)
		}

		// reorg the last few blocks to re-mature the payout
		blocks = nil
		state = prevState
		for i := 0; i < 10; i++ {
			blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
			state.Index.ID = blocks[len(blocks)-1].ID()
			state.Index.Height++
		}
		if err := cm.AddBlocks(blocks); err != nil {
			t.Fatal(err)
		}
		waitForBlock(t, cm, db)

		// check that the balance is correct
		if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
			t.Fatal(err)
		}

		// check that only the single utxo still exists
		utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) != 1 {
			t.Fatalf("expected 1 output, got %v", len(utxos))
		} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
			t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
		} else if utxos[0].MaturityHeight != maturityHeight {
			t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
		}
	}

	t.Run("IndexModePersonal", func(t *testing.T) {
		state, db, cm, w := setupNode(t, wallet.IndexModePersonal)
		testReorg(t, state, db, cm, w)
	})

	t.Run("IndexModeFull", func(t *testing.T) {
		state, db, cm, w := setupNode(t, wallet.IndexModeFull)
		testReorg(t, state, db, cm, w)
	})
}

func TestEphemeralBalance(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

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

	network, genesisBlock := testV1Network(types.VoidAddress) // don't care about siafunds

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)
	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight() + 1
	block := mineBlock(cm.TipState(), nil, addr)
	minerPayoutID := block.ID().MinerOutputID(0)
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{block}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the payout was received
	balance, err := wm.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	} else if events[0].ID != types.Hash256(minerPayoutID) {
		t.Fatalf("expected %v, got %v", minerPayoutID, events[0].ID)
	}

	// mine until the payout matures
	for i := cm.TipState().Index.Height; i < maturityHeight; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, db)

	// create a transaction that spends the matured payout
	utxos, err := wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	}

	unlockConditions := types.StandardUnlockConditions(pk.PublicKey())
	parentTxn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{
			{
				ParentID:         types.SiacoinOutputID(utxos[0].ID),
				UnlockConditions: unlockConditions,
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(100)},
			{Address: types.VoidAddress, Value: utxos[0].SiacoinOutput.Value.Sub(types.Siacoins(100))},
		},
		Signatures: []types.TransactionSignature{
			{
				ParentID:       utxos[0].ID,
				PublicKeyIndex: 0,
				CoveredFields:  types.CoveredFields{WholeTransaction: true},
			},
		},
	}
	parentSigHash := cm.TipState().WholeSigHash(parentTxn, utxos[0].ID, 0, 0, nil)
	parentSig := pk.SignHash(parentSigHash)
	parentTxn.Signatures[0].Signature = parentSig[:]

	outputID := parentTxn.SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{
			{
				ParentID:         outputID,
				UnlockConditions: unlockConditions,
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: types.Siacoins(100)},
		},
		Signatures: []types.TransactionSignature{
			{
				ParentID:       types.Hash256(outputID),
				PublicKeyIndex: 0,
				CoveredFields:  types.CoveredFields{WholeTransaction: true},
			},
		},
	}
	sigHash := cm.TipState().WholeSigHash(txn, types.Hash256(outputID), 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures[0].Signature = sig[:]

	txnset := []types.Transaction{parentTxn, txn}

	// broadcast the transactions
	revertState := cm.TipState()
	if err := cm.AddBlocks([]types.Block{mineBlock(revertState, txnset, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the payout was spent
	balance, err = wm.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.IsZero() {
		t.Fatalf("expected 0, got %v", balance.Siacoins)
	}

	// check that both transactions were added
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 3 { // 1 payout, 2 transactions
		t.Fatalf("expected 3 events, got %v", len(events))
	} else if events[2].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected miner payout event, got %v", events[2].Type)
	} else if events[1].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[1].Type)
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	} else if events[1].ID != types.Hash256(parentTxn.ID()) { // parent txn first
		t.Fatalf("expected %v, got %v", parentTxn.ID(), events[1].ID)
	} else if events[0].ID != types.Hash256(txn.ID()) { // child txn second
		t.Fatalf("expected %v, got %v", txn.ID(), events[0].ID)
	}

	// trigger a reorg
	var blocks []types.Block
	state := revertState
	for i := 0; i < 2; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height++
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the transaction was reverted
	balance, err = wm.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.Siacoins)
	}

	// check that only the payout event remains
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	}
}

func TestWalletAddresses(t *testing.T) {
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

	network, genesisBlock := testV1Network(types.VoidAddress) // don't care about siafunds

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)
	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// Add a wallet
	w := wallet.Wallet{
		Name:        "test",
		Description: "hello, world!",
		Metadata:    json.RawMessage(`{"foo": "bar"}`),
	}
	w, err = wm.AddWallet(w)
	if err != nil {
		t.Fatal(err)
	}

	wallets, err := wm.Wallets()
	if err != nil {
		t.Fatal(err)
	} else if len(wallets) != 1 {
		t.Fatal("expected 1 wallet, got", len(wallets))
	} else if wallets[0].ID != w.ID {
		t.Fatal("unexpected wallet ID", wallets[0].ID)
	} else if wallets[0].Name != "test" {
		t.Fatal("unexpected wallet name", wallets[0].Name)
	} else if wallets[0].Description != "hello, world!" {
		t.Fatal("unexpected description", wallets[0].Description)
	} else if !bytes.Equal(wallets[0].Metadata, []byte(`{"foo": "bar"}`)) {
		t.Fatal("unexpected metadata", wallets[0].Metadata)
	} else if wallets[0].DateCreated.IsZero() || !wallets[0].DateCreated.Equal(w.DateCreated) {
		t.Fatalf("expected creation date %s, got %s", w.DateCreated, wallets[0].DateCreated)
	} else if wallets[0].LastUpdated.IsZero() || !wallets[0].LastUpdated.Equal(w.LastUpdated) {
		t.Fatalf("expected last updated date %s, got %s", w.LastUpdated, wallets[0].LastUpdated)
	}

	// Add an address
	pk := types.GeneratePrivateKey()
	spendPolicy := types.PolicyPublicKey(pk.PublicKey())
	address := spendPolicy.Address()

	addr := wallet.Address{
		Address:     address,
		SpendPolicy: &spendPolicy,
		Description: "hello, world",
	}
	err = db.AddWalletAddress(w.ID, addr)
	if err != nil {
		t.Fatal(err)
	}

	// Check that the address was added
	addresses, err := db.WalletAddresses(w.ID)
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 1 {
		t.Fatal("expected 1 address, got", len(addresses))
	} else if addresses[0].Address != address {
		t.Fatal("unexpected address", addresses[0].Address)
	} else if addresses[0].Description != "hello, world" {
		t.Fatal("unexpected description", addresses[0].Description)
	} else if *addresses[0].SpendPolicy != spendPolicy {
		t.Fatal("unexpected spend policy", addresses[0].SpendPolicy)
	}

	// update the addresses metadata and description
	addr.Description = "goodbye, world"
	addr.Metadata = json.RawMessage(`{"foo": "bar"}`)

	if err := db.AddWalletAddress(w.ID, addr); err != nil {
		t.Fatal(err)
	}

	// Check that the address was added
	addresses, err = db.WalletAddresses(w.ID)
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 1 {
		t.Fatal("expected 1 address, got", len(addresses))
	} else if addresses[0].Address != address {
		t.Fatal("unexpected address", addresses[0].Address)
	} else if addresses[0].Description != "goodbye, world" {
		t.Fatal("unexpected description", addresses[0].Description)
	} else if *addresses[0].SpendPolicy != spendPolicy {
		t.Fatal("unexpected spend policy", addresses[0].SpendPolicy)
	} else if string(addresses[0].Metadata) != `{"foo": "bar"}` {
		t.Fatal("unexpected metadata", addresses[0].Metadata)
	}

	// Remove the address
	err = db.RemoveWalletAddress(w.ID, address)
	if err != nil {
		t.Fatal(err)
	}

	// Check that the address was removed
	addresses, err = db.WalletAddresses(w.ID)
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 0 {
		t.Fatal("expected 0 addresses, got", len(addresses))
	}
}

func TestScan(t *testing.T) {
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

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

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
	// rescan to get the genesis Siafund state
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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
	b, ok := coreutils.MineBlock(cm, addr, 5*time.Second)
	if !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	expectedBalance2 := cm.TipState().BlockReward()

	// mine a block to fund the second address
	b, ok = coreutils.MineBlock(cm, addr2, 5*time.Second)
	if !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet has one immature payout
	if err := checkBalance(types.ZeroCurrency, expectedBalance1); err != nil {
		t.Fatal(err)
	}

	// mine until the first payout matures
	for i := cm.Tip().Height; i < genesisState.MaturityHeight(); i++ {
		if b, ok := coreutils.MineBlock(cm, types.VoidAddress, 5*time.Second); !ok {
			t.Fatal("failed to mine block")
		} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
			t.Fatal(err)
		}
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// scan for changes
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	if err := checkBalance(expectedBalance1, expectedBalance2); err != nil {
		t.Fatal(err)
	}

	// mine a block to mature the second payout
	b, ok = coreutils.MineBlock(cm, types.VoidAddress, 5*time.Second)
	if !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1.Add(expectedBalance2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// sanity check
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

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

	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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
	}

	if b, ok := coreutils.MineBlock(cm, types.VoidAddress, 5*time.Second); !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	} else if err := checkBalance(w1.ID, sendAmount); err != nil {
		t.Fatal(err)
	}

	// rescan for sanity check
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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

func TestOrphans(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

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

	network, genesisBlock := testV1Network(types.VoidAddress) // don't care about siafunds
	network.HardforkV2.AllowHeight = 200
	network.HardforkV2.RequireHeight = 201

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}

	// mine until the maturity height
	for i := cm.TipState().Index.Height; i < maturityHeight+1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, db)

	assertBalance := func(siacoin, immature types.Currency) error {
		b, err := wm.WalletBalance(w.ID)
		if err != nil {
			return fmt.Errorf("failed to check balance: %w", err)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			return fmt.Errorf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		} else if !b.Siacoins.Equals(siacoin) {
			return fmt.Errorf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		}
		return nil
	}

	if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that a payout event was recorded
	events, err := wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	}

	// check that the utxo was created
	utxos, err := wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	} else if utxos[0].MaturityHeight != maturityHeight {
		t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
	}

	resetState := cm.TipState()

	// send a transaction that will be orphaned
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{
			{
				ParentID:         types.SiacoinOutputID(utxos[0].ID),
				UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: expectedPayout.Div64(2)}, // send the other half to the void
			{Address: addr, Value: expectedPayout.Div64(2)},              // send half the payout back to the wallet
		},
		Signatures: []types.TransactionSignature{
			{
				ParentID:       utxos[0].ID,
				PublicKeyIndex: 0,
				CoveredFields:  types.CoveredFields{WholeTransaction: true},
			},
		},
	}
	sigHash := cm.TipState().WholeSigHash(txn, utxos[0].ID, 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures[0].Signature = sig[:]

	// broadcast the transaction
	if _, err := cm.AddPoolTransactions([]types.Transaction{txn}); err != nil {
		t.Fatal(err)
	} else if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), []types.Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	if err := assertBalance(expectedPayout.Div64(2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that the transaction event was recorded
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 2 {
		t.Fatalf("expected 2 events, got %v", len(events))
	}

	// simulate an interrupted rescan by closing the wallet manager, resetting the
	// last scan index, and initializing a new wallet manager.
	if err := wm.Close(); err != nil {
		t.Fatal(err)
	} else if err := db.ResetLastIndex(); err != nil {
		t.Fatal(err)
	}

	// mine to trigger a reorg. The underlying store must properly revert the
	// orphaned blocks that will not be cleanly reverted since the rescan was
	// interrupted.
	var blocks []types.Block
	state := resetState
	for i := 0; i < 5; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height++
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}

	wm, err = wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	waitForBlock(t, cm, db)

	// check that the transaction was reverted
	if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that the transaction event was reverted
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	}

	// check that the utxo was reverted
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if !utxos[0].SiacoinOutput.Value.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	}
}

func TestFullIndex(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	pk2 := types.GeneratePrivateKey()
	addr2 := types.StandardUnlockHash(pk2.PublicKey())

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

	network, genesisBlock := testV2Network(addr2)
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	waitForBlock(t, cm, db)

	assertBalance := func(t *testing.T, address types.Address, siacoin, immature types.Currency, siafund uint64) {
		t.Helper()

		b, err := wm.AddressBalance(address)
		if err != nil {
			t.Fatal(err)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			t.Fatalf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		} else if !b.Siacoins.Equals(siacoin) {
			t.Fatalf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		} else if b.Siafunds != siafund {
			t.Fatalf("expected siafund balance %v, got %v", siafund, b.Siafunds)
		}
	}

	// check the events are empty for the first address
	if events, err := wm.AddressEvents(addr, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatalf("expected 0 events, got %v", len(events))
	}

	// assert that the airdropped siafunds are on the second address
	assertBalance(t, addr2, types.ZeroCurrency, types.ZeroCurrency, cm.TipState().SiafundCount())
	// check the events for the air dropped siafunds
	if events, err := wm.AddressEvents(addr2, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	}

	// mine a block and send the payout to the first address
	expectedBalance1 := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check the payout was received
	if events, err := wm.AddressEvents(addr, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected miner payout event, got %v", events[0].Type)
	}

	assertBalance(t, addr, types.ZeroCurrency, expectedBalance1, 0)

	// mine until the payout matures
	for i := cm.TipState().Index.Height; i < maturityHeight; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, db)

	// check that the events did not change
	if events, err := wm.AddressEvents(addr, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected miner payout event, got %v", events[0].Type)
	}

	assertBalance(t, addr, expectedBalance1, types.ZeroCurrency, 0)
	assertBalance(t, addr2, types.ZeroCurrency, types.ZeroCurrency, cm.TipState().SiafundCount())

	// send half siacoins to the second address
	utxos, err := wm.AddressSiacoinOutputs(addr, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	policy := types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey()))
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: utxos[0],
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: types.SpendPolicy{
						Type: policy,
					},
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr2, Value: utxos[0].SiacoinOutput.Value.Div64(2)},
			{Address: addr, Value: utxos[0].SiacoinOutput.Value.Div64(2)},
		},
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	assertBalance(t, addr, expectedBalance1.Div64(2), types.ZeroCurrency, 0)
	assertBalance(t, addr2, expectedBalance1.Div64(2), types.ZeroCurrency, cm.TipState().SiafundCount())

	// check the events for the transaction
	if events, err := wm.AddressEvents(addr, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 2 {
		t.Fatalf("expected 2 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	}

	// check the events for the second address
	if events, err := wm.AddressEvents(addr2, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 2 {
		t.Fatalf("expected 2 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	}

	sf, err := wm.AddressSiafundOutputs(addr2, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	// send the siafunds to the first address
	policy = types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk2.PublicKey()))
	txn = types.V2Transaction{
		SiafundInputs: []types.V2SiafundInput{
			{
				Parent: sf[0],
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: types.SpendPolicy{
						Type: policy,
					},
				},
				ClaimAddress: addr2, // claim address shouldn't create an event since the value is 0
			},
		},
		SiafundOutputs: []types.SiafundOutput{
			{Address: addr, Value: sf[0].SiafundOutput.Value},
		},
	}
	txn.SiafundInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk2.SignHash(cm.TipState().InputSigHash(txn))}

	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	assertBalance(t, addr, expectedBalance1.Div64(2), types.ZeroCurrency, cm.TipState().SiafundCount())
	assertBalance(t, addr2, expectedBalance1.Div64(2), types.ZeroCurrency, 0)

	// check the events for the transaction
	if events, err := wm.AddressEvents(addr2, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 3 {
		t.Fatalf("expected 3 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	}

	// check the events for the first address
	if events, err := wm.AddressEvents(addr, 0, 100); err != nil {
		t.Fatal(err)
	} else if len(events) != 3 {
		t.Fatalf("expected 3 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	}
}

func TestV2(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

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

	network, genesisBlock := testV2Network(types.VoidAddress) // don't care about siafunds

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)
	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the payout was received
	balance, err := db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	}

	// mine until the payout matures
	maturityHeight := cm.TipState().MaturityHeight() + 1
	for i := cm.TipState().Index.Height; i < maturityHeight; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, db)

	// create a v2 transaction that spends the matured payout
	utxos, err := wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	sce := utxos[0]
	policy := types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey()))
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{{
			Parent: sce,
			SatisfiedPolicy: types.SatisfiedPolicy{
				Policy: types.SpendPolicy{Type: policy},
			},
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: sce.SiacoinOutput.Value.Sub(types.Siacoins(100))},
			{Address: addr, Value: types.Siacoins(100)},
		},
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the change was received
	balance, err = wm.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(100)) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a transaction event was recorded
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 2 {
		t.Fatalf("expected 2 events, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Type)
	} else if events[0].Relevant[0] != addr {
		t.Fatalf("expected address %v, got %v", addr, events[0].Relevant[0])
	}
}

func TestScanV2(t *testing.T) {
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

	network, genesisBlock := testV2Network(addr)
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

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
	// rescan to get the genesis Siafund state
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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
	if b, ok := coreutils.MineBlock(cm, addr, 5*time.Second); !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	// mine a block to fund the second address
	expectedBalance2 := cm.TipState().BlockReward()
	if b, ok := coreutils.MineBlock(cm, addr2, 5*time.Second); !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet has one immature payout
	if err := checkBalance(types.ZeroCurrency, expectedBalance1); err != nil {
		t.Fatal(err)
	}

	// mine until the first payout matures
	for i := cm.Tip().Height; i < genesisState.MaturityHeight(); i++ {
		if b, ok := coreutils.MineBlock(cm, types.VoidAddress, 5*time.Second); !ok {
			t.Fatal("failed to mine block")
		} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
			t.Fatal(err)
		}
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// scan for changes
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
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
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	if err := checkBalance(expectedBalance1, expectedBalance2); err != nil {
		t.Fatal(err)
	}

	// mine a block to mature the second payout
	if b, ok := coreutils.MineBlock(cm, types.VoidAddress, 5*time.Second); !ok {
		t.Fatal("failed to mine block")
	} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1.Add(expectedBalance2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// sanity check
	if err := wm.Scan(context.Background(), types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	// check that the wallet balance has matured
	if err := checkBalance(expectedBalance1.Add(expectedBalance2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	utxos, err := wm.AddressSiacoinOutputs(addr, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	// spend the payout
	sce := utxos[0]
	policy := types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey()))
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{{
			Parent: sce,
			SatisfiedPolicy: types.SatisfiedPolicy{
				Policy: types.SpendPolicy{Type: policy},
			},
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: sce.SiacoinOutput.Value},
		},
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the first address has a balance of zero
	if err := checkBalance(expectedBalance2, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}
}

func TestReorgV2(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

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

	network, genesisBlock := testV2Network(types.VoidAddress) // don't care about siafunds

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	assertBalance := func(siacoin, immature types.Currency) error {
		b, err := wm.WalletBalance(w.ID)
		if err != nil {
			return fmt.Errorf("failed to check balance: %w", err)
		} else if !b.Siacoins.Equals(siacoin) {
			return fmt.Errorf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			return fmt.Errorf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		}
		return nil
	}

	if err := assertBalance(types.ZeroCurrency, expectedPayout); err != nil {
		t.Fatal(err)
	}

	// check that a payout event was recorded
	events, err := wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	}

	// check that the utxo was created
	utxos, err := wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	} else if utxos[0].MaturityHeight != maturityHeight {
		t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
	}

	// mine to trigger a reorg
	var blocks []types.Block
	state := genesisState
	for i := 0; i < 10; i++ {
		block := mineBlock(state, nil, types.VoidAddress)
		blocks = append(blocks, block)
		state.Index.ID = block.ID()
		state.Index.Height++
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the balance was reverted
	if err := assertBalance(types.ZeroCurrency, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that the payout event was reverted
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatalf("expected 0 events, got %v", len(events))
	}

	// check that the utxo was removed
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 0 {
		t.Fatalf("expected 0 outputs, got %v", len(utxos))
	}

	// mine a new payout
	expectedPayout = cm.TipState().BlockReward()
	maturityHeight = cm.TipState().MaturityHeight()
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the payout was received
	if err := assertBalance(types.ZeroCurrency, expectedPayout); err != nil {
		t.Fatal(err)
	}

	// check that a payout event was recorded
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	}

	// check that the utxo was created
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	} else if utxos[0].MaturityHeight != maturityHeight {
		t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
	}

	// mine until the payout matures
	var prevState consensus.State
	for i := cm.TipState().Index.Height; i < maturityHeight+1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
		if i == maturityHeight-5 {
			prevState = cm.TipState()
		}
	}
	waitForBlock(t, cm, db)

	// check that the balance was updated
	if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// reorg the last few blocks to re-mature the payout
	blocks = nil
	state = prevState
	for i := 0; i < 10; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height++
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the balance is correct
	if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that only the single utxo still exists
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	} else if utxos[0].MaturityHeight != maturityHeight {
		t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
	}

	// spend the payout
	sce := utxos[0]
	policy := types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey()))
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{{
			Parent: sce,
			SatisfiedPolicy: types.SatisfiedPolicy{
				Policy: types.SpendPolicy{Type: policy},
			},
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: sce.SiacoinOutput.Value},
		},
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the balance is correct
	if err := assertBalance(types.ZeroCurrency, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that all UTXOs have been spent
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 0 {
		t.Fatalf("expected 0 output, got %v", len(utxos))
	}
}

func TestOrphansV2(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

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

	network, genesisBlock := testV2Network(types.VoidAddress) // don't care about siafunds
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}

	// mine until the maturity height
	for i := cm.TipState().Index.Height; i < maturityHeight+1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, db)

	assertBalance := func(siacoin, immature types.Currency) error {
		b, err := wm.WalletBalance(w.ID)
		if err != nil {
			return fmt.Errorf("failed to check balance: %w", err)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			return fmt.Errorf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		} else if !b.Siacoins.Equals(siacoin) {
			return fmt.Errorf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		}
		return nil
	}

	if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that a payout event was recorded
	events, err := wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Type != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Type)
	}

	// check that the utxo was created
	utxos, err := wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if utxos[0].SiacoinOutput.Value.Cmp(expectedPayout) != 0 {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	} else if utxos[0].MaturityHeight != maturityHeight {
		t.Fatalf("expected %v, got %v", maturityHeight, utxos[0].MaturityHeight)
	}

	resetState := cm.TipState()

	// send a transaction that will be orphaned
	sce := utxos[0]
	policy := types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey()))
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{{
			Parent: sce,
			SatisfiedPolicy: types.SatisfiedPolicy{
				Policy: types.SpendPolicy{Type: policy},
			},
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: expectedPayout.Div64(2)}, // send the other half to the void
			{Address: addr, Value: expectedPayout.Div64(2)},              // send half the payout back to the wallet
		},
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

	// broadcast the transaction
	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	if err := assertBalance(expectedPayout.Div64(2), types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that the transaction event was recorded
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 2 {
		t.Fatalf("expected 2 events, got %v", len(events))
	}

	// simulate an interrupted rescan by closing the wallet manager, resetting the
	// last scan index, and initializing a new wallet manager.
	if err := wm.Close(); err != nil {
		t.Fatal(err)
	} else if err := db.ResetLastIndex(); err != nil {
		t.Fatal(err)
	}

	// mine to trigger a reorg. The underlying store must properly revert the
	// orphaned blocks that will not be cleanly reverted since the rescan was
	// interrupted.
	var blocks []types.Block
	state := resetState
	for i := 0; i < 5; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height++
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}

	wm, err = wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	waitForBlock(t, cm, db)

	// check that the transaction was reverted
	if err := assertBalance(expectedPayout, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that the transaction event was reverted
	events, err = wm.Events(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	}

	// check that the utxo was reverted
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 1 {
		t.Fatalf("expected 1 output, got %v", len(utxos))
	} else if !utxos[0].SiacoinOutput.Value.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, utxos[0].SiacoinOutput.Value)
	}

	// spend the payout
	txn = types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{{
			Parent: sce,
			SatisfiedPolicy: types.SatisfiedPolicy{
				Policy: types.SpendPolicy{Type: policy},
			},
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: sce.SiacoinOutput.Value},
		},
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

	if err := cm.AddBlocks([]types.Block{mineV2Block(cm.TipState(), []types.V2Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, db)

	// check that the balance is correct
	if err := assertBalance(types.ZeroCurrency, types.ZeroCurrency); err != nil {
		t.Fatal(err)
	}

	// check that all UTXOs have been spent
	utxos, err = wm.UnspentSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(utxos) != 0 {
		t.Fatalf("expected 0 output, got %v", len(utxos))
	}
}

func TestDeleteWallet(t *testing.T) {
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

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

	network, genesisBlock := testV1Network(types.VoidAddress) // don't care about siafunds

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(store, genesisState)

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	w, err := wm.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := wm.AddAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	if err := wm.DeleteWallet(w.ID); err != nil {
		t.Fatal(err)
	}
}
