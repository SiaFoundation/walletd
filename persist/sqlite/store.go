package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap"
)

type (
	// A Store is a persistent store that uses a SQL database as its backend.
	Store struct {
		indexMode                   wallet.IndexMode
		spentElementRetentionBlocks uint64 // number of blocks to retain spent elements

		db  *sql.DB
		log *zap.Logger
	}
)

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// transaction executes a function within a database transaction. If the
// function returns an error, the transaction is rolled back. Otherwise, the
// transaction is committed.
func (s *Store) transaction(fn func(*txn) error) error {
	log := s.log.Named("transaction")

	start := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Error("failed to rollback transaction", zap.Error(err))
		}
	}()
	if err := fn(&txn{
		Tx:  tx,
		log: log,
	}); err != nil {
		return err
	}
	// log the transaction if it took longer than txn duration
	if time.Since(start) > longTxnDuration {
		log.Debug("long transaction", zap.Duration("elapsed", time.Since(start)), zap.Stack("stack"), zap.Bool("failed", err != nil))
	}
	// commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

func sqliteFilepath(fp string) string {
	params := []string{
		fmt.Sprintf("_busy_timeout=%d", 300000), // 300 seconds
		"_foreign_keys=true",
		"_journal_mode=WAL",
		"_secure_delete=false",
		"_cache_size=-65536", // 64MiB
	}
	return "file:" + fp + "?" + strings.Join(params, "&")
}

// OpenDatabase creates a new SQLite store and initializes the database. If the
// database does not exist, it is created.
func OpenDatabase(fp string, opts ...Option) (*Store, error) {
	db, err := sql.Open("sqlite3", sqliteFilepath(fp))
	if err != nil {
		return nil, err
	}
	// set the number of open connections to 1 to prevent "database is locked"
	// errors
	db.SetMaxOpenConns(1)

	store := &Store{
		db: db,

		log:                         zap.NewNop(),
		spentElementRetentionBlocks: 144, // default to 144 blocks (1 day)
	}
	for _, opt := range opts {
		opt(store)
	}
	if err := store.init(); err != nil {
		return nil, err
	}
	sqliteVersion, _, _ := sqlite3.Version()
	store.log.Debug("database initialized", zap.String("sqliteVersion", sqliteVersion), zap.Int("schemaVersion", len(migrations)+1), zap.String("path", fp))
	return store, nil
}
