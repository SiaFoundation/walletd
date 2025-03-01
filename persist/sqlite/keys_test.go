package sqlite

import (
	"bytes"
	"errors"
	"path/filepath"
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

	_, err = store.GetSigningKey(sk.PublicKey())
	if !errors.Is(err, keys.ErrNotFound) {
		t.Fatal(err)
	}

	expected := frand.Bytes(64) // mock encrypted key
	if err = store.AddSigningKey(sk.PublicKey(), expected); err != nil {
		t.Fatal(err)
	}

	buf, err := store.GetSigningKey(sk.PublicKey())
	if err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(expected, buf) {
		t.Fatal("keys don't match")
	}
}
