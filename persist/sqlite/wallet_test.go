package sqlite

import (
	"fmt"
	"reflect"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap"
	"lukechampine.com/frand"
)

func TestAddAddresses(t *testing.T) {
	// generate a large number of random addresses
	addresses := make([]wallet.Address, 1000)
	for i := range addresses {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addresses[i].Address = sp.Address()
		addresses[i].SpendPolicy = &sp
		addresses[i].Description = fmt.Sprintf("address %d", i)
	}

	db := newTestStore(t)

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

	// change random addresses' descriptions
	for range 10 {
		i := frand.Intn(len(addresses))
		addresses[i].Description = fmt.Sprintf("updated address %d", i)
	}

	// add additional addresses
	for range 10 {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addresses = append(addresses, wallet.Address{
			Address:     sp.Address(),
			SpendPolicy: &sp,
			Description: fmt.Sprintf("address %d", len(addresses)),
		})
	}

	// re-add the initial addresses and the new ones to ensure updates work
	if err := db.AddWalletAddresses(w.ID, addresses...); err != nil {
		t.Fatal(err)
	}
	walletAddresses, err = db.WalletAddresses(w.ID)
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
	db := newTestStore(b, WithLog(zap.NewNop()))

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
