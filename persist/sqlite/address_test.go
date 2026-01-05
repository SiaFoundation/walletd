package sqlite

import (
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
	"lukechampine.com/frand"
)

func TestCheckAddresses(t *testing.T) {
	// generate a large number of random addresses
	addresses := make([]types.Address, 1000)
	for i := range addresses {
		addresses[i] = frand.Entropy256()
	}

	db := newTestStore(t)

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
	} else if err := db.AddWalletAddresses(w.ID, wallet.Address{
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
