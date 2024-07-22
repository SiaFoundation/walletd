package wallet

import "go.uber.org/zap"

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

// WithChainManager sets the chain manager used by the manager.
func WithChainManager(cm ChainManager) Option {
	return func(m *Manager) {
		m.chain = cm
	}
}

// WithStore sets the store used by the manager.
func WithStore(s Store) Option {
	return func(m *Manager) {
		m.store = s
	}
}
