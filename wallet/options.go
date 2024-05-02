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
