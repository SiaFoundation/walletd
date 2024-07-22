package api

type ServerOption func(*server)

// WithChainManager sets the chain manager used by the server.
func WithChainManager(cm ChainManager, mode string) ServerOption {
	return func(s *server) {
		s.cm = cm
		s.chainMode = mode
	}
}

// WithWalletManager sets the wallet manager used by the server.
func WithWalletManager(wm WalletManager) ServerOption {
	return func(s *server) {
		s.wm = wm
	}
}

// WithSyncer sets the syncer used by the server.
func WithSyncer(syncer Syncer) ServerOption {
	return func(s *server) {
		s.s = syncer
	}
}
