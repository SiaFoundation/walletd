package keys_test

import (
	"errors"
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/keys"
	"go.sia.tech/walletd/persist/sqlite"
	"go.uber.org/zap"
	"lukechampine.com/frand"
)

func TestKeyManager(t *testing.T) {
	store, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m, err := keys.NewManager(store, "foo")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sk := types.GeneratePrivateKey()

	if err := m.Add(sk); err != nil {
		t.Fatal(err)
	}

	// try to add it again
	if err := m.Add(sk); err != nil {
		t.Fatal(err)
	}

	hash := types.Hash256(frand.Entropy256())

	sig, err := m.Sign(sk.PublicKey(), hash)
	if err != nil {
		t.Fatal(err)
	} else if !sk.PublicKey().VerifyHash(hash, sig) {
		t.Fatal("signature failed to verify")
	}

	// try to sign with an unknown key
	_, err = m.Sign(types.GeneratePrivateKey().PublicKey(), hash)
	if !errors.Is(err, keys.ErrNotFound) {
		t.Fatalf("expected %v, got %v", keys.ErrNotFound, err)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = keys.NewManager(store, "foobar")
	if !errors.Is(err, keys.ErrIncorrectSecret) {
		t.Fatalf("expected %v, got %v", keys.ErrIncorrectSecret, err)
	}

	m, err = keys.NewManager(store, "foo")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sig, err = m.Sign(sk.PublicKey(), hash)
	if err != nil {
		t.Fatal(err)
	} else if !sk.PublicKey().VerifyHash(hash, sig) {
		t.Fatal("signature failed to verify")
	}

	// delete the key
	if err := m.Delete(sk.PublicKey()); err != nil {
		t.Fatal(err)
	} else if _, err := m.Sign(sk.PublicKey(), hash); !errors.Is(err, keys.ErrNotFound) {
		t.Fatalf("expected %v, got %v", keys.ErrNotFound, err)
	}
}
