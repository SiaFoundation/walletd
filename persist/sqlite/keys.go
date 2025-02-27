package sqlite

import (
	"crypto/ed25519"
	"database/sql"
	"errors"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/keys"
)

// AddSigningKey adds a signing key to the store. If the key already exists, it
// is not added again.
func (s *Store) AddSigningKey(sk types.PrivateKey) error {
	if len(sk) != ed25519.PrivateKeySize {
		return keys.ErrInvalidSize
	}
	return s.transaction(func(tx *txn) error {
		_, err := tx.Exec("INSERT INTO signing_keys (public_key, private_key) VALUES (?, ?) ON CONFLICT (public_key) DO NOTHING", encode(sk.PublicKey()), sk[:])
		return err
	})
}

// GetSigningKey returns the private key corresponding to the given public key.
// If the key is not found, it returns [keys.ErrNotFound].
func (s *Store) GetSigningKey(pk types.PublicKey) (sk types.PrivateKey, err error) {
	err = s.transaction(func(tx *txn) error {
		err := s.db.QueryRow("SELECT private_key FROM signing_keys WHERE public_key = ?", encode(pk)).Scan(&sk)
		if errors.Is(err, sql.ErrNoRows) {
			return keys.ErrNotFound
		} else if err != nil {
			return err
		} else if len(sk) != ed25519.PrivateKeySize {
			return keys.ErrInvalidSize
		}
		return nil
	})
	return
}

// DeleteSigningKey deletes the signing key with the given public key. If the key
// does not exist, it returns nil.
func (s *Store) DeleteSigningKey(pk types.PublicKey) error {
	return s.transaction(func(tx *txn) error {
		_, err := tx.Exec("DELETE FROM signing_keys WHERE public_key = ?", encode(pk))
		return err
	})
}
