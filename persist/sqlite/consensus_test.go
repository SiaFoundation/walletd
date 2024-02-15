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
	n.HardforkV2.AllowHeight = 5
	n.HardforkV2.RequireHeight = 10
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
	} else if !balance.Immature.Equals(expectedPayout) {
		t.Fatalf("expected %v, got %v", expectedPayout, balance.Immature)
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
	} else if !balance.Immature.IsZero() {
		t.Fatalf("expected 0, got %v", balance.Immature)
	}

	// check that the payout event was reverted
	events, err = db.WalletEvents("test", 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatalf("expected 0 events, got %v", len(events))
	}
}
