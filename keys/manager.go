package keys

import (
	"crypto/cipher"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/internal/threadgroup"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"lukechampine.com/frand"
)

var (
	// ErrInvalidSize is returned when a key has an invalid size.
	ErrInvalidSize = errors.New("invalid key size")
	// ErrNotFound is returned when a signing key is not found.
	ErrNotFound = errors.New("not found")
	// ErrSaltSet is returned when the key salt is already set.
	ErrSaltSet = errors.New("salt already set")
	// ErrIncorrectSecret is returned when the secret is incorrect.
	ErrIncorrectSecret = errors.New("incorrect secret")
)

type (
	// A Store saves and loads ed25519 signing keys.
	Store interface {
		// GetSigningKey returns the encrypted signing key with the given public key.
		// If the key is not found, it returns [ErrNotFound]. The key must be
		// decrypted before being used.
		GetSigningKey(types.PublicKey) ([]byte, error)
		// AddSigningKey adds a signing key to the store. If the key already
		// exists, nil is returned. The key must be encrypted before being
		// stored.
		AddSigningKey(pk types.PublicKey, buf []byte) error
		// DeleteSigningKey deletes the signing key with the given public key.
		// If the key does not exist, it returns [ErrNotFound].
		DeleteSigningKey(types.PublicKey) error

		// KeySalt returns the salt used to derive the key encryption
		// key. If no salt has been set, KeySalt should return (nil, nil).
		GetKeySalt() ([]byte, error)

		// SetKeySalt sets the salt used to derive the key encryption key.
		// If a salt has already been set, [keys.ErrSaltSet] is returned.
		SetKeySalt([]byte) error

		// GetBytesForVerify returns random encrypted bytes for verifying
		// the encryption key.
		GetBytesForVerify() ([]byte, error)
	}

	// A Manager is a key-value store for ed25519 signing keys.
	Manager struct {
		tg *threadgroup.ThreadGroup

		aead  cipher.AEAD
		store Store
	}
)

// Add adds a key to the manager. If the key is not the correct
// size, it returns [ErrInvalidSize].
func (m *Manager) Add(sk types.PrivateKey) error {
	if len(sk) != ed25519.PrivateKeySize {
		return ErrInvalidSize
	}

	done, err := m.tg.Add()
	if err != nil {
		return err
	}
	defer done()

	n := m.aead.NonceSize()
	buf := make([]byte, m.aead.NonceSize(), n+len(sk)+m.aead.Overhead())
	frand.Read(buf)
	encrypted := m.aead.Seal(buf, buf, sk, nil)
	return m.store.AddSigningKey(sk.PublicKey(), encrypted)
}

// Sign returns the signature for a hash. If the key is not
// found, it returns [ErrNotFound].
func (m *Manager) Sign(key types.PublicKey, hash types.Hash256) (types.Signature, error) {
	done, err := m.tg.Add()
	if err != nil {
		return types.Signature{}, err
	}
	defer done()

	buf, err := m.store.GetSigningKey(key)
	if err != nil {
		return types.Signature{}, err
	}
	defer clear(buf)

	sk := make(types.PrivateKey, 0, ed25519.PrivateKeySize)
	defer clear(sk)
	sk, err = m.aead.Open(sk, buf[:m.aead.NonceSize()], buf[m.aead.NonceSize():], nil)
	if err != nil {
		return types.Signature{}, fmt.Errorf("failed to decrypt key: %w", err)
	}

	if len(sk) != ed25519.PrivateKeySize {
		return types.Signature{}, ErrInvalidSize
	}
	return types.PrivateKey(sk).SignHash(hash), nil
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

// NewManager creates a new key manager. If the store contains
// encrypted keys, the secret must match the secret used to encrypt
// the existing keys. If the secret is incorrect, NewManager returns
// [ErrIncorrectSecret].
//
// Keys are encrypted using ChaCha20-Poly1305 with a key derived from
// the secret using Argon2ID.
func NewManager(store Store, secret string) (*Manager, error) {
	salt, err := store.GetKeySalt()
	if err != nil {
		return nil, fmt.Errorf("failed to get key salt: %w", err)
	} else if len(salt) == 0 {
		salt = frand.Bytes(32)
		if err := store.SetKeySalt(salt); err != nil {
			return nil, fmt.Errorf("failed to set key salt: %w", err)
		}
	}

	encryptionKey := argon2.IDKey([]byte(secret), salt, 3, 64*1024, 4, 32)
	aead, err := chacha20poly1305.NewX(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AEAD: %w", err)
	}

	buf, err := store.GetBytesForVerify()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("failed to get bytes for verify: %w", err)
	} else if err == nil {
		defer clear(buf)

		decrypted, err := aead.Open(nil, buf[:aead.NonceSize()], buf[aead.NonceSize():], nil)
		if err != nil {
			if strings.Contains(err.Error(), "message authentication failed") {
				return nil, ErrIncorrectSecret
			}
			return nil, fmt.Errorf("failed to verify encryption key: %w", err)
		}
		defer clear(decrypted)
	}

	return &Manager{
		aead:  aead,
		store: store,
		tg:    threadgroup.New(),
	}, nil
}
