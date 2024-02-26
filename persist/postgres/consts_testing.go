//go:build testing

package postgres

import "time"

const (
	busyTimeout      = 100 // 100ms
	maxRetryAttempts = 10  // 10 attempts
	factor           = 2.0 // factor ^ retryAttempts = backoff time in milliseconds
	maxBackoff       = 15 * time.Second
)
