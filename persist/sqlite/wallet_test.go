package sqlite

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
)

func TestWalletAddresses(t *testing.T) {
	log := zaptest.NewLogger(t)
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), log.Named("sqlite3"))
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
