package sqlite

import (
	"go.sia.tech/walletd/wallet"
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

func WithIndexMode(mode wallet.IndexMode) Option {
	return func(s *Store) {
		s.indexMode = mode
	}
}
