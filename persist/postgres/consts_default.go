//go:build !testing

package postgres

import "time"

const (
	busyTimeout      = 10000 // 10 seconds
	maxRetryAttempts = 30    // 30 attempts
	factor           = 1.8   // factor ^ retryAttempts = backoff time in milliseconds
	maxBackoff       = 15 * time.Second
)
