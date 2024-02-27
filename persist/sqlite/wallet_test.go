package sqlite_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
)

func TestWalletAddresses(t *testing.T) {
	log := zaptest.NewLogger(t)
	db, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Add a wallet
	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}

	wallets, err := db.Wallets()
	if err != nil {
		t.Fatal(err)
	} else if len(wallets) != 1 {
		t.Fatal("expected 1 wallet, got", len(wallets))
	} else if wallets[0].ID != w.ID {
		t.Fatal("unexpected wallet ID", wallets[0].ID)
	} else if wallets[0].Name != "test" {
		t.Fatal("unexpected wallet name", wallets[0].Name)
	} else if wallets[0].Metadata != nil {
		t.Fatal("unexpected metadata", wallets[0].Metadata)
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

	network, genesisBlock := testV1Network()

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

	// check that the payout was received
	balance, err := db.WalletBalance(w.ID)
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

	cm.RemoveSubscriber(db)
	if err := cm.AddSubscriber(db, types.ChainIndex{}); err != nil {
		t.Fatal(err)
	}

	// check that the balance, events, and utxos did not change
	// check that the payout was received
	balance, err = db.WalletBalance(w.ID)
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
}
