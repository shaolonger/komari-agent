package monitoring

import (
	"sync"
	"time"
)

// lowFrequencySampler serializes an expensive platform source and retains its
// last valid value across transient errors. It is intentionally small; the
// context-driven sampler runtime owns long-lived scheduling after snapshots are
// wired in A-106.
type lowFrequencySampler[T any] struct {
	mu        sync.Mutex
	source    func() (T, error)
	now       func() time.Time
	interval  time.Duration
	value     T
	loaded    bool
	sampledAt time.Time
	lastErr   error
}

func newLowFrequencySampler[T any](source func() (T, error), now func() time.Time, interval time.Duration) *lowFrequencySampler[T] {
	return &lowFrequencySampler[T]{source: source, now: now, interval: interval}
}

func (sampler *lowFrequencySampler[T]) Sample(force bool) (T, error) {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()

	now := sampler.now()
	if !force && sampler.loaded && !durationElapsed(now, sampler.sampledAt, sampler.interval) {
		return sampler.value, sampler.lastErr
	}
	value, err := sampler.source()
	sampler.sampledAt = now
	sampler.lastErr = err
	if err == nil {
		sampler.value = value
		sampler.loaded = true
	}
	return sampler.value, err
}
