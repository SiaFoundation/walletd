package sqlite

import (
	"database/sql"
	"errors"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/keys"
)

// AddSigningKey adds a signing key to the store. If the key already exists, it
// is not added again.
func (s *Store) AddSigningKey(pk types.PublicKey, buf []byte) error {
	return s.transaction(func(tx *txn) error {
		_, err := tx.Exec("INSERT INTO signing_keys (public_key, private_key) VALUES (?, ?) ON CONFLICT (public_key) DO NOTHING", encode(pk), buf)
		return err
	})
}

// GetSigningKey returns the private key corresponding to the given public key.
// If the key is not found, it returns [keys.ErrNotFound].
func (s *Store) GetSigningKey(pk types.PublicKey) (buf []byte, err error) {
	err = s.transaction(func(tx *txn) error {
		err := s.db.QueryRow("SELECT private_key FROM signing_keys WHERE public_key = ?", encode(pk)).Scan(&buf)
		if errors.Is(err, sql.ErrNoRows) {
			return keys.ErrNotFound
		} else if err != nil {
			return err
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

// KeySalt returns the salt used to derive the key encryption
// key. If no salt has been set, KeySalt returns [keys.ErrNotFound].
func (s *Store) GetKeySalt() (salt []byte, err error) {
	err = s.transaction(func(tx *txn) error {
		err := s.db.QueryRow("SELECT key_salt FROM global_settings").Scan(&salt)
		if errors.Is(err, sql.ErrNoRows) {
			return keys.ErrNotFound
		}
		return err
	})
	return
}

// SetKeySalt sets the salt used to derive the key encryption key.
// If a salt has already been set, [keys.ErrKeySaltSet] is returned.
func (s *Store) SetKeySalt(salt []byte) error {
	return s.transaction(func(tx *txn) error {
		res, err := tx.Exec("UPDATE global_settings SET key_salt = ? WHERE key_salt IS NULL", salt)
		if err != nil {
			return err
		} else if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("key salt already set")
		}
		return nil
	})
}

// GetBytesForVerify returns random encrypted bytes for verifying
// the encryption key. If there are no keys in the store, it returns
// [keys.ErrNotFound].
func (s *Store) GetBytesForVerify() (buf []byte, err error) {
	err = s.transaction(func(tx *txn) error {
		err := s.db.QueryRow("SELECT private_key FROM signing_keys LIMIT 1").Scan(&buf)
		if errors.Is(err, sql.ErrNoRows) {
			return keys.ErrNotFound
		}
		return err
	})
	return
}
