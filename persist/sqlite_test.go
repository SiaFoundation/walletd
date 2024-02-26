package persist_test

import (
	"path/filepath"
	"testing"

	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
)

func TestSQLite(t *testing.T) {
	testWalletStore(t, func(t *testing.T, log *zap.Logger) wallet.Store {
		db, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "walletd.sqlite3"), log.Named("sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		})
		return db
	})

	testPeerStore(t, func(t *testing.T, log *zap.Logger) syncer.PeerStore {
		db, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "syncer.sqlite3"), log.Named("sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		})
		return db
	})
}
