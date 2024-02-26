//go:build test_pg

package persist_test

import (
	"testing"

	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/persist/postgres"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
)

func openPostgresDB(t *testing.T, log *zap.Logger) *postgres.Store {
	conn := postgres.ConnectionInfo{
		Host:     "localhost",
		Port:     5432,
		User:     "walletd_test",
		Password: "walletd_test",
		Database: "walletd_test",
		SSLMode:  "disable",
	}
	db, err := postgres.OpenDatabase(conn, log.Named("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.ResetDatabase(); err != nil {
			t.Fatal(err)
		} else if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return db
}

func TestPostgres(t *testing.T) {
	testWalletStore(t, func(t *testing.T, log *zap.Logger) wallet.Store {
		return openPostgresDB(t, log)
	})
	testPeerStore(t, func(t *testing.T, log *zap.Logger) syncer.PeerStore {
		return openPostgresDB(t, log)
	})
}
