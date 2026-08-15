package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"go.uber.org/zap/zaptest"
)

// newTestStore creates a new Store for testing. It is closed automatically
// when the test completes.
func newTestStore(t testing.TB, opts ...Option) *Store {
	t.Helper()

	log := zaptest.NewLogger(t)
	opts = append([]Option{WithLog(log.Named("sqlite3"))}, opts...)
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	return db
}

func TestTransactionRetry(t *testing.T) {
	db := newTestStore(t)

	t.Run("retries busy errors", func(t *testing.T) {
		var attempts int
		err := db.transaction(func(tx *txn) error {
			attempts++
			if attempts < 3 {
				return errors.New("database is locked")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		} else if attempts != 3 {
			t.Fatalf("expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("does not retry other errors", func(t *testing.T) {
		expected := errors.New("constraint violation")
		var attempts int
		err := db.transaction(func(tx *txn) error {
			attempts++
			return expected
		})
		if !errors.Is(err, expected) {
			t.Fatalf("expected %v, got %v", expected, err)
		} else if attempts != 1 {
			t.Fatalf("expected 1 attempt, got %d", attempts)
		}
	})

	t.Run("rolls back a failed attempt", func(t *testing.T) {
		var attempts int
		err := db.transaction(func(tx *txn) error {
			attempts++
			if _, err := tx.Exec(`INSERT INTO syncer_peers (peer_address, first_seen) VALUES (?, ?)`, "1.2.3.4:9981", 0); err != nil {
				return err
			}
			if attempts < 2 {
				return errors.New("database is locked")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		var count int
		if err := db.transaction(func(tx *txn) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM syncer_peers WHERE peer_address=?`, "1.2.3.4:9981").Scan(&count)
		}); err != nil {
			t.Fatal(err)
		} else if count != 1 {
			t.Fatalf("expected 1 peer, got %d", count)
		}
	})
}
