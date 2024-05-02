package sqlite

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
	"lukechampine.com/frand"
)

type (
	// A Store is a persistent store that uses a SQL database as its backend.
	Store struct {
		indexMode wallet.IndexMode

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
// transaction is committed. If the transaction fails due to a busy error, it is
// retried up to 10 times before returning.
func (s *Store) transaction(fn func(*txn) error) error {
	var err error
	txnID := hex.EncodeToString(frand.Bytes(4))
	log := s.log.Named("transaction").With(zap.String("id", txnID))
	start := time.Now()
	attempt := 1
	for ; attempt < maxRetryAttempts; attempt++ {
		attemptStart := time.Now()
		log := log.With(zap.Int("attempt", attempt))
		err = doTransaction(s.db, log, fn)
		if err == nil {
			// no error, break out of the loop
			return nil
		}

		// return immediately if the error is not a busy error
		if !strings.Contains(err.Error(), "database is locked") {
			break
		}
		// exponential backoff
		sleep := time.Duration(math.Pow(factor, float64(attempt))) * time.Millisecond
		if sleep > maxBackoff {
			sleep = maxBackoff
		}
		log.Debug("database locked", zap.Duration("elapsed", time.Since(attemptStart)), zap.Duration("totalElapsed", time.Since(start)), zap.Stack("stack"), zap.Duration("retry", sleep))
		jitterSleep(sleep)
	}
	return fmt.Errorf("transaction failed (attempt %d): %w", attempt, err)
}

func sqliteFilepath(fp string) string {
	params := []string{
		fmt.Sprintf("_busy_timeout=%d", busyTimeout),
		"_foreign_keys=true",
		"_journal_mode=WAL",
		"_secure_delete=false",
		"_cache_size=-65536", // 64MiB
	}
	return "file:" + fp + "?" + strings.Join(params, "&")
}

// doTransaction is a helper function to execute a function within a transaction. If fn returns
// an error, the transaction is rolled back. Otherwise, the transaction is
// committed.
func doTransaction(db *sql.DB, log *zap.Logger, fn func(tx *txn) error) error {
	dbtx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	start := time.Now()
	defer func() {
		if err := dbtx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Error("failed to rollback transaction", zap.Error(err))
		}
		// log the transaction if it took longer than txn duration
		if time.Since(start) > longTxnDuration {
			log.Debug("long transaction", zap.Duration("elapsed", time.Since(start)), zap.Stack("stack"), zap.Bool("failed", err != nil))
		}
	}()

	tx := &txn{
		Tx:  dbtx,
		log: log,
	}
	if err := fn(tx); err != nil {
		return err
	} else if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// OpenDatabase creates a new SQLite store and initializes the database. If the
// database does not exist, it is created.
func OpenDatabase(fp string, log *zap.Logger) (*Store, error) {
	db, err := sql.Open("sqlite3", sqliteFilepath(fp))
	if err != nil {
		return nil, err
	}
	store := &Store{
		db:  db,
		log: log,
	}
	if err := store.init(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	return store, nil
}
