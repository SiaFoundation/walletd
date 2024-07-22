package remote

import "time"

type ChainManagerOption func(*ChainManager)

func WithRefreshInterval(interval time.Duration) ChainManagerOption {
	return func(cm *ChainManager) {
		cm.refreshInterval = interval
	}
}
