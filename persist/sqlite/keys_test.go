package sqlite

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/keys"
	"go.uber.org/zap/zaptest"
	"lukechampine.com/frand"
)

func TestSigningKeys(t *testing.T) {
	store, err := OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sk := types.GeneratePrivateKey()

	err = store.AddSigningKey(types.PrivateKey(frand.Bytes(8)))
	if !errors.Is(err, keys.ErrInvalidSize) {
		t.Fatal(err)
	}

	_, err = store.GetSigningKey(sk.PublicKey())
	if !errors.Is(err, keys.ErrNotFound) {
		t.Fatal(err)
	}

	if err = store.AddSigningKey(sk); err != nil {
		t.Fatal(err)
	}

	sk2, err := store.GetSigningKey(sk.PublicKey())
	if err != nil {
		t.Fatal(err)
	} else if !slices.Equal(sk, sk2) {
		t.Fatal("keys don't match")
	}
}
