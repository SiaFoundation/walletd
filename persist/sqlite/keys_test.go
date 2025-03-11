package sqlite

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/keys"
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

func TestSalt(t *testing.T) {
	store, err := OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assertSalt := func(t *testing.T, expected []byte) {
		t.Helper()
		s, err := store.GetKeySalt()
		if err != nil {
			t.Fatal(err)
		} else if expected == nil && s != nil {
			t.Fatal("expected nil salt") // bytes.Equal([]byte{}, nil) == true
		} else if !bytes.Equal(s, expected) {
			t.Fatal("salts don't match")
		}
	}

	// check salt is initially nil
	assertSalt(t, nil)

	expected := frand.Bytes(32)
	if err = store.SetKeySalt(expected); err != nil {
		t.Fatal(err)
	}
	assertSalt(t, expected)

	if err = store.SetKeySalt(frand.Bytes(32)); !errors.Is(err, keys.ErrSaltSet) {
		t.Fatalf("expected %v, got %v", keys.ErrSaltSet, err)
	}

	// check salt was not changed
	assertSalt(t, expected)
}
