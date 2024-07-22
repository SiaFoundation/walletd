package remote

import "time"

// ChainManagerOption is a functional option type for configuring a ChainManager.
type ChainManagerOption func(*ChainManager)

// WithRefreshInterval sets the polling interval used by the chain manager.
func WithRefreshInterval(interval time.Duration) ChainManagerOption {
	return func(cm *ChainManager) {
		cm.refreshInterval = interval
	}
}
