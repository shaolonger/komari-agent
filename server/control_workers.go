package server

import (
	"context"
	"errors"
	"sync"
)

type controlWorkerTracker struct {
	mu    sync.Mutex
	count int
	zero  chan struct{}
}

func newControlWorkerTracker() *controlWorkerTracker {
	zero := make(chan struct{})
	close(zero)
	return &controlWorkerTracker{zero: zero}
}

func (tracker *controlWorkerTracker) launch(run func()) {
	if run == nil {
		return
	}
	tracker.mu.Lock()
	if tracker.count == 0 {
		tracker.zero = make(chan struct{})
	}
	tracker.count++
	tracker.mu.Unlock()
	go func() {
		defer tracker.done()
		run()
	}()
}

func (tracker *controlWorkerTracker) done() {
	tracker.mu.Lock()
	tracker.count--
	if tracker.count == 0 {
		close(tracker.zero)
	}
	tracker.mu.Unlock()
}

func (tracker *controlWorkerTracker) wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("control worker wait requires a parent context")
	}
	tracker.mu.Lock()
	zero := tracker.zero
	tracker.mu.Unlock()
	select {
	case <-zero:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var activeControlWorkers = newControlWorkerTracker()

// WaitForControlWorkers waits for remote exec, ping and terminal work accepted
// before shutdown. Their parent contexts are canceled by RunTelemetryWebSocket.
func WaitForControlWorkers(ctx context.Context) error {
	return activeControlWorkers.wait(ctx)
}
