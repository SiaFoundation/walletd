package wallet

import (
	"time"

	"go.uber.org/zap"
)

// An Option configures a wallet Manager.
type Option func(*Manager)

// WithLogger sets the logger used by the manager.
func WithLogger(log *zap.Logger) Option {
	return func(m *Manager) {
		m.log = log
	}
}

// WithIndexMode sets the index mode used by the manager.
func WithIndexMode(mode IndexMode) Option {
	return func(m *Manager) {
		m.indexMode = mode
	}
}

// WithSyncBatchSize sets the number of blocks to batch when scanning
// the blockchain. The default is 64. Increasing this value can
// improve performance at the cost of memory usage.
func WithSyncBatchSize(size int) Option {
	return func(m *Manager) {
		m.syncBatchSize = size
	}
}

// WithLockDuration sets the duration that a UTXO is locked after
// being selected as an input to a transaction. The default is 1 hour.
func WithLockDuration(d time.Duration) Option {
	return func(m *Manager) {
		m.lockDuration = d
	}
}
