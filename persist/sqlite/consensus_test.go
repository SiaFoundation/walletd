package sqlite_test

import (
	"path/filepath"
	"testing"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
)

func testNetwork() (*consensus.Network, types.Block) {
	// use a modified version of Zen
	n, genesisBlock := chain.TestnetZen()
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

func TestReorg(t *testing.T) {
	log := zaptest.NewLogger(t)
	dir := t.TempDir()
	db, err := sqlite.OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	network, genesisBlock := testNetwork()

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cm := chain.NewManager(store, genesisState)

	if err := cm.AddSubscriber(db, types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	if err := db.AddWallet("test", nil); err != nil {
		t.Fatal(err)
	} else if err := db.AddAddress("test", addr, nil); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}

	// check that the payout was received
	balance, err := db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := db.WalletEvents("test", 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	}

	// mine to trigger a reorg
	var blocks []types.Block
	state := genesisState
	for i := 0; i < 5; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height = state.Index.Height + 1
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}

	// check that the payout was reverted
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatalf("expected 0, got %v", balance.ImmatureSiacoins)
	}

	// check that the payout event was reverted
	events, err = db.WalletEvents("test", 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatalf("expected 0 events, got %v", len(events))
	}
}

func TestEphemeralBalance(t *testing.T) {
	log := zaptest.NewLogger(t)
	dir := t.TempDir()
	db, err := sqlite.OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	network, genesisBlock := testNetwork()

	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cm := chain.NewManager(store, genesisState)

	if err := cm.AddSubscriber(db, types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	if err := db.AddWallet("test", nil); err != nil {
		t.Fatal(err)
	} else if err := db.AddAddress("test", addr, nil); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight() + 1
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}

	// check that the payout was received
	balance, err := db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := db.WalletEvents("test", 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	}

	// mine until the payout matures
	for i := cm.TipState().Index.Height; i < maturityHeight; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}

	// create a transaction that spends the matured payout
	utxos, err := db.UnspentSiacoinOutputs("test")
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

	// check that the payout was spent
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.IsZero() {
		t.Fatalf("expected 0, got %v", balance.Siacoins)
	}

	// trigger a reorg
	var blocks []types.Block
	state := revertState
	for i := 0; i < 2; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height = state.Index.Height + 1
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}

	// check that the transaction was reverted
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.Siacoins)
	}

	// check that only the payout event remains
	events, err = db.WalletEvents("test", 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 events, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	}
}
