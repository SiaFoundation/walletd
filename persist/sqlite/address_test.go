package sqlite

import (
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap/zaptest"
	"lukechampine.com/frand"
)

func TestAddressesKnown(t *testing.T) {
	log := zaptest.NewLogger(t)

	// generate a large number of random addresses
	addresses := make([]types.Address, 1000)
	for i := range len(addresses) {
		addresses[i] = frand.Entropy256()
	}

	// create a new database
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if known, err := db.CheckAddresses(addresses); err != nil {
		t.Fatal(err)
	} else if known {
		t.Fatal("expected no addresses to be known")
	}

	// add a random address to the database
	address := addresses[frand.Intn(len(addresses))]

	w, err := db.AddWallet(wallet.Wallet{})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddress(w.ID, wallet.Address{
		Address: address,
	}); err != nil {
		t.Fatal(err)
	}

	if known, err := db.CheckAddresses(addresses); err != nil {
		t.Fatal(err)
	} else if !known {
		t.Fatal("expected addresses to be known")
	}
}
