package sqlite

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap/zaptest"
)

func TestAddAddresses(t *testing.T) {
	log := zaptest.NewLogger(t)

	// generate a large number of random addresses
	addresses := make([]wallet.Address, 1000)
	for i := range addresses {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addresses[i].Address = sp.Address()
		addresses[i].SpendPolicy = &sp
		addresses[i].Description = fmt.Sprintf("address %d", i)
	}

	// create a new database
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite"), WithLog(log.Named("sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	w, err := db.AddWallet(wallet.Wallet{})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.AddWalletAddresses(w.ID, addresses...); err != nil {
		t.Fatal(err)
	}

	walletAddresses, err := db.WalletAddresses(w.ID)
	if err != nil {
		t.Fatal(err)
	} else if len(walletAddresses) != len(addresses) {
		t.Fatalf("expected %d addresses, got %d", len(addresses), len(walletAddresses))
	}
	for i, addr := range walletAddresses {
		if !reflect.DeepEqual(addr, addresses[i]) {
			t.Fatalf("expected address %d to be %v, got %v", i, addresses[i], addr)
		}
	}
}

func BenchmarkAddWalletAddresses(b *testing.B) {
	db, err := OpenDatabase(filepath.Join(b.TempDir(), "walletd.sqlite3"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	addresses := make([]wallet.Address, b.N)
	for i := range addresses {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addresses[i].Address = sp.Address()
		addresses[i].SpendPolicy = &sp
		addresses[i].Description = fmt.Sprintf("address %d", i)
	}

	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	if err := db.AddWalletAddresses(w.ID, addresses...); err != nil {
		b.Fatal(err)
	}
}
