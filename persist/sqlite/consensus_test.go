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

func syncDB(t *testing.T, db *sqlite.Store, cm *chain.Manager) {
	index, err := db.LastCommittedIndex()
	if err != nil {
		t.Fatal(err)
	}
	for index != cm.Tip() {
		crus, caus, err := cm.UpdatesSince(index, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, cru := range crus {
			if err := db.ProcessChainRevertUpdate(cru); err != nil {
				t.Fatal("failed to process revert update:", err)
			}
			index = cru.State.Index
		}
		for _, cau := range caus {
			if err := db.ProcessChainApplyUpdate(cau); err != nil {
				t.Fatal("failed to process apply update:", err)
			}
			index = cau.State.Index
		}
	}
}

func TestReorg(t *testing.T) {
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

	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// check that the payout was received
	balance, err := db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	}

	// check that the utxo was created
	utxos, err := db.WalletSiacoinOutputs(w.ID, 0, 100)
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
	for i := 0; i < 5; i++ {
		blocks = append(blocks, mineBlock(state, nil, types.VoidAddress))
		state.Index.ID = blocks[len(blocks)-1].ID()
		state.Index.Height++
	}
	if err := cm.AddBlocks(blocks); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// check that the payout was reverted
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatalf("expected 0, got %v", balance.ImmatureSiacoins)
	}

	// check that the payout event was reverted
	events, err = db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatalf("expected 0 events, got %v", len(events))
	}

	// check that the utxo was removed
	utxos, err = db.WalletSiacoinOutputs(w.ID, 0, 100)
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
	syncDB(t, db, cm)

	// check that the payout was received
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err = db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	}

	// check that the utxo was created
	utxos, err = db.WalletSiacoinOutputs(w.ID, 0, 100)
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
	syncDB(t, db, cm)

	// check that the balance was updated
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatalf("expected %v, got %v", types.ZeroCurrency, balance.ImmatureSiacoins)
	} else if !balance.Siacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.Siacoins)
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
	syncDB(t, db, cm)

	// check that the balance is correct
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatalf("expected %v, got %v", types.ZeroCurrency, balance.ImmatureSiacoins)
	} else if !balance.Siacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.Siacoins)
	}

	// check that only the single utxo still exists
	utxos, err = db.WalletSiacoinOutputs(w.ID, 0, 100)
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

	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddress(w.ID, wallet.Address{Address: addr}); err != nil {
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
	syncDB(t, db, cm)

	// check that the payout was received
	balance, err := db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	} else if events[0].ID != types.Hash256(minerPayoutID) {
		t.Fatalf("expected %v, got %v", minerPayoutID, events[0].ID)
	}

	// mine until the payout matures
	for i := cm.TipState().Index.Height; i < maturityHeight; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	syncDB(t, db, cm)

	// create a transaction that spends the matured payout
	utxos, err := db.WalletSiacoinOutputs(w.ID, 0, 100)
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
	syncDB(t, db, cm)

	// check that the payout was spent
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.IsZero() {
		t.Fatalf("expected 0, got %v", balance.Siacoins)
	}

	// check that both transactions were added
	events, err = db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 3 { // 1 payout, 2 transactions
		t.Fatalf("expected 3 events, got %v", len(events))
	} else if events[2].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected miner payout event, got %v", events[2].Data.EventType())
	} else if events[1].Data.EventType() != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[1].Data.EventType())
	} else if events[0].Data.EventType() != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Data.EventType())
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
	syncDB(t, db, cm)

	// check that the transaction was reverted
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.Siacoins)
	}

	// check that only the payout event remains
	events, err = db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 events, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
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

	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddress(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	expectedPayout := cm.TipState().BlockReward()
	// mine a block sending the payout to the wallet
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// check that the payout was received
	balance, err := db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a payout event was recorded
	events, err := db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 1 {
		t.Fatalf("expected 1 event, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeMinerPayout {
		t.Fatalf("expected payout event, got %v", events[0].Data.EventType())
	}

	// mine until the payout matures
	maturityHeight := cm.TipState().MaturityHeight() + 1
	for i := cm.TipState().Index.Height; i < maturityHeight; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	syncDB(t, db, cm)

	// create a v2 transaction that spends the matured payout
	utxos, err := db.WalletSiacoinOutputs(w.ID, 0, 100)
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
	syncDB(t, db, cm)

	// check that the change was received
	balance, err = db.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(100)) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.ImmatureSiacoins)
	}

	// check that a transaction event was recorded
	events, err = db.WalletEvents(w.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 2 {
		t.Fatalf("expected 2 events, got %v", len(events))
	} else if events[0].Data.EventType() != wallet.EventTypeTransaction {
		t.Fatalf("expected transaction event, got %v", events[0].Data.EventType())
	} else if events[0].Relevant[0] != addr {
		t.Fatalf("expected address %v, got %v", addr, events[0].Relevant[0])
	}
}
