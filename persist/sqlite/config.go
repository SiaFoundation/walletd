package sqlite

import (
	"go.uber.org/zap"
)

// An Option is a functional option for configuring a Store.
type Option func(*Store)

// WithLogger sets the logger used by the Store.
func WithLogger(log *zap.Logger) Option {
	return func(s *Store) {
		s.log = log
	}
}

// WithFullIndex sets the store to index all transactions and outputs, rather
// than just those relevant to the wallet.
func WithFullIndex() Option {
	return func(s *Store) {
		s.fullIndex = true
	}
}
