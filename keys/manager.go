package keys

import (
	"errors"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/internal/threadgroup"
)

var (
	// ErrInvalidSize is returned when a key has an invalid size.
	ErrInvalidSize = errors.New("invalid key size")
	// ErrNotFound is returned when a signing key is not found.
	ErrNotFound = errors.New("not found")
)

type (
	// A Store saves and loads ed25519 signing keys.
	Store interface {
		GetSigningKey(types.PublicKey) (types.PrivateKey, error)
		AddSigningKey(types.PrivateKey) error
		DeleteSigningKey(types.PublicKey) error
	}

	// A Manager is a key-value store for ed25519 signing keys.
	Manager struct {
		tg    *threadgroup.ThreadGroup
		store Store
	}
)

// Add adds a key to the manager.
func (m *Manager) Add(key types.PrivateKey) error {
	done, err := m.tg.Add()
	if err != nil {
		return err
	}
	defer done()
	return m.store.AddSigningKey(key)
}

// Sign returns the signature for a hash. If the key is not found, it returns
// [ErrNotFound].
func (m *Manager) Sign(key types.PublicKey, hash types.Hash256) (types.Signature, error) {
	done, err := m.tg.Add()
	if err != nil {
		return types.Signature{}, err
	}
	defer done()

	sk, err := m.store.GetSigningKey(key)
	if err != nil {
		return types.Signature{}, err
	}
	return sk.SignHash(hash), nil
}

// Delete removes a key from the manager.
func (m *Manager) Delete(key types.PublicKey) error {
	done, err := m.tg.Add()
	if err != nil {
		return err
	}
	defer done()
	return m.store.DeleteSigningKey(key)
}

// Close closes the manager.
func (m *Manager) Close() error {
	m.tg.Stop()
	return nil
}

// NewManager creates a new key manager.
func NewManager(store Store) *Manager {
	return &Manager{
		store: store,
		tg:    threadgroup.New(),
	}
}
