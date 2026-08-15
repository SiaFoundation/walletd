package sqlite

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"go.sia.tech/walletd/v2/wallet"
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
// retried up to maxRetryAttempts times before returning.
func (s *Store) transaction(fn func(*txn) error) error {
	var err error
	txnID := hex.EncodeToString(frand.Bytes(4))
	log := s.log.Named("transaction").With(zap.String("id", txnID))
	start := time.Now()
	attempt := 1
	for ; attempt <= maxRetryAttempts; attempt++ {
		attemptStart := time.Now()
		log := log.With(zap.Int("attempt", attempt))
		err = doTransaction(s.db, log, fn)
		if err == nil {
			return nil
		}

		// return immediately if the error is not a busy error
		if !strings.Contains(err.Error(), "database is locked") {
			break
		}
		// exponential backoff
		sleep := min(time.Duration(math.Pow(factor, float64(attempt)))*time.Millisecond, maxBackoff)
		log.Debug("database locked", zap.Duration("elapsed", time.Since(attemptStart)), zap.Duration("totalElapsed", time.Since(start)), zap.Stack("stack"), zap.Duration("retry", sleep))
		jitterSleep(sleep)
	}
	return fmt.Errorf("transaction failed (attempt %d): %w", attempt, err)
}

// doTransaction executes fn within a database transaction. If fn returns an
// error, the transaction is rolled back. Otherwise, the transaction is
// committed.
func doTransaction(db *sql.DB, log *zap.Logger, fn func(tx *txn) error) (err error) {
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

// valuedTransaction executes fn within a database transaction and returns the
// value it produces. fn must build its result in local variables; nothing it
// produced is returned unless the transaction commits.
func valuedTransaction[T any](s *Store, fn func(*txn) (T, error)) (T, error) {
	var v T
	if err := s.transaction(func(tx *txn) error {
		var err error
		v, err = fn(tx)
		return err
	}); err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// valuedTransaction2 is [valuedTransaction] for functions returning two values.
func valuedTransaction2[T1, T2 any](s *Store, fn func(*txn) (T1, T2, error)) (T1, T2, error) {
	var v1 T1
	var v2 T2
	if err := s.transaction(func(tx *txn) error {
		var err error
		v1, v2, err = fn(tx)
		return err
	}); err != nil {
		var zero1 T1
		var zero2 T2
		return zero1, zero2, err
	}
	return v1, v2, nil
}

func sqliteFilepath(fp string) string {
	params := []string{
		fmt.Sprintf("_busy_timeout=%d", busyTimeout.Milliseconds()),
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

	store := &Store{
		db: db,

		log: zap.NewNop(),
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
