package sqlite

import (
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
