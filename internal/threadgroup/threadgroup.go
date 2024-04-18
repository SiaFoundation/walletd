// Package threadgroup exposes a ThreadGroup object which can be used to
// facilitate clean shutdown. A ThreadGroup is similar to a sync.WaitGroup,
// but with two important additions: The ability to detect when shutdown has
// been initiated, and protections against adding more threads after shutdown
// has completed.
//
// ThreadGroup was designed with the following shutdown sequence in mind:
//
// 1. Call Stop, signaling that shutdown has begun. After Stop is called, no
// new goroutines should be created.
//
// 2. Wait for Stop to return. When Stop returns, all goroutines should have
// returned.
//
// 3. Free any resources used by the goroutines.
package threadgroup

import (
	"context"
	"errors"
	"sync"
)

type (
	// A ThreadGroup is a sync.WaitGroup with additional functionality for
	// facilitating clean shutdown.
	ThreadGroup struct {
		mu     sync.Mutex
		wg     sync.WaitGroup
		closed chan struct{}
	}
)

// ErrClosed is returned when the threadgroup has already been stopped
var ErrClosed = errors.New("threadgroup closed")

// Done returns a channel that will be closed when the threadgroup is stopped
func (tg *ThreadGroup) Done() <-chan struct{} {
	return tg.closed
}

// Add adds a new thread to the group, done must be called to signal that the
// thread is done. Returns ErrClosed if the threadgroup is already closed.
func (tg *ThreadGroup) Add() (func(), error) {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	select {
	case <-tg.closed:
		return nil, ErrClosed
	default:
	}
	tg.wg.Add(1)
	return func() { tg.wg.Done() }, nil
}

// WithContext returns a copy of the parent context. The returned context will
// be cancelled if the parent context is cancelled or if the threadgroup is
// stopped.
func (tg *ThreadGroup) WithContext(parent context.Context) (context.Context, context.CancelFunc) {
	// wrap the parent context in a cancellable context
	ctx, cancel := context.WithCancel(parent)
	// start a goroutine to wait for either the parent context being cancelled
	// or the threagroup being stopped
	go func() {
		select {
		case <-ctx.Done():
		case <-tg.closed:
		}
		cancel() // threadgroup is stopping or context cancelled, cancel the context
	}()
	return ctx, cancel
}

// AddWithContext adds a new thread to the group and returns a copy of the parent
// context. It is a convenience function combining Add and WithContext.
func (tg *ThreadGroup) AddWithContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	// try to add to the group
	done, err := tg.Add()
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := tg.WithContext(parent)
	var once sync.Once
	return ctx, func() {
		cancel()
		// it must be safe to call cancel multiple times, but it is not safe to
		// call done multiple times since it's decrementing the waitgroup
		once.Do(done)
	}, nil
}

// Stop stops accepting new threads and waits for all existing threads to close
func (tg *ThreadGroup) Stop() {
	tg.mu.Lock()
	select {
	case <-tg.closed:
	default:
		close(tg.closed)
	}
	tg.mu.Unlock()
	tg.wg.Wait()
}

// New creates a new threadgroup
func New() *ThreadGroup {
	return &ThreadGroup{
		closed: make(chan struct{}),
	}
}
