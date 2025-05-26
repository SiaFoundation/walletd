package sqlite

import "go.uber.org/zap"

// An Option is a function that configures the Store.
type Option func(*Store)

// WithLog sets the logger for the store.
func WithLog(log *zap.Logger) Option {
	return func(s *Store) {
		s.log = log
	}
}

// WithRetainSpentElements sets the number of blocks to retain
// spent elements.
func WithRetainSpentElements(blocks uint64) Option {
	return func(s *Store) {
		s.spentElementRetentionBlocks = blocks
	}
}
