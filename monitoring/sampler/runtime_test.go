package sampler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeSamplesImmediatelyAndPublishesImmutableSnapshot(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	attempted := make(chan struct{}, 1)
	runtime, err := New([]Spec{{
		Name:       "cpu",
		Interval:   time.Second,
		Timeout:    time.Second,
		StaleAfter: 3 * time.Second,
		Sample: func(context.Context) (any, error) {
			attempted <- struct{}{}
			return 42, nil
		},
	}}, WithClock(clock))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(runtime.Stop)
	waitSignal(t, attempted)
	waitResult(t, runtime, "cpu", func(result Result) bool { return result.Value == 42 })

	snapshot := runtime.Snapshot()
	result := snapshot.Results["cpu"]
	if result.Stale || result.Generation != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	delete(snapshot.Results, "cpu")
	if _, exists := runtime.Snapshot().Results["cpu"]; !exists {
		t.Fatal("mutating a returned snapshot changed runtime state")
	}
}

func TestRuntimeMarksLastSuccessfulValueStaleAfterErrors(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	var calls atomic.Int64
	runtime, err := New([]Spec{{
		Name:            "network",
		Interval:        time.Second,
		Timeout:         time.Second,
		StaleAfter:      2 * time.Second,
		ErrorBackoffMin: time.Second,
		ErrorBackoffMax: 4 * time.Second,
		Sample: func(context.Context) (any, error) {
			if calls.Add(1) == 1 {
				return uint64(100), nil
			}
			return nil, errors.New("source unavailable")
		},
	}}, WithClock(clock))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(runtime.Stop)
	waitResult(t, runtime, "network", func(result Result) bool { return result.Value == uint64(100) })

	clock.Advance(time.Second)
	waitResult(t, runtime, "network", func(result Result) bool { return result.ConsecutiveErrors == 1 })
	clock.Advance(2 * time.Second)
	result := runtime.Snapshot().Results["network"]
	if !result.Stale || result.Value != uint64(100) || result.Error == "" {
		t.Fatalf("unexpected stale result: %+v", result)
	}
}

func TestRuntimeTimeoutDoesNotBlockStop(t *testing.T) {
	runtime, err := New([]Spec{{
		Name:       "slow",
		Interval:   time.Hour,
		Timeout:    20 * time.Millisecond,
		StaleAfter: time.Second,
		Sample: func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitResult(t, runtime, "slow", func(result Result) bool { return result.Error != "" })
	stopped := make(chan struct{})
	go func() {
		runtime.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("runtime Stop did not wait for a bounded worker exit")
	}
}

func TestErrorBackoffDoublesAndCaps(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	attempts := make(chan struct{}, 8)
	runtime, err := New([]Spec{{
		Name:            "failing",
		Interval:        10 * time.Second,
		Timeout:         time.Second,
		StaleAfter:      time.Minute,
		ErrorBackoffMin: time.Second,
		ErrorBackoffMax: 4 * time.Second,
		Sample: func(context.Context) (any, error) {
			attempts <- struct{}{}
			return nil, errors.New("failed")
		},
	}}, WithClock(clock))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(runtime.Stop)

	for _, expectedDelay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		waitSignal(t, attempts)
		waitPendingDelay(t, clock, expectedDelay)
		clock.Advance(expectedDelay)
	}
}

func TestReloadCancelsOldGeneration(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	oldStarted := make(chan struct{})
	oldExited := make(chan struct{})
	runtime, err := New([]Spec{{
		Name:       "metric",
		Interval:   time.Hour,
		Timeout:    time.Hour,
		StaleAfter: time.Hour,
		Sample: func(ctx context.Context) (any, error) {
			close(oldStarted)
			<-ctx.Done()
			close(oldExited)
			return nil, ctx.Err()
		},
	}}, WithClock(clock))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(runtime.Stop)
	waitSignal(t, oldStarted)

	if err := runtime.Reload([]Spec{{
		Name:       "metric",
		Interval:   time.Hour,
		Timeout:    time.Second,
		StaleAfter: time.Hour,
		Sample: func(context.Context) (any, error) {
			return "new-generation", nil
		},
	}}); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	waitSignal(t, oldExited)
	waitResult(t, runtime, "metric", func(result Result) bool {
		return result.Generation == 2 && result.Value == "new-generation"
	})
}

func TestConcurrentSnapshotsAndReloadAreRaceFree(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	spec := Spec{
		Name:       "metric",
		Interval:   time.Second,
		Timeout:    time.Second,
		StaleAfter: time.Minute,
		Sample: func(context.Context) (any, error) {
			return 1, nil
		},
	}
	runtime, err := New([]Spec{spec}, WithClock(clock))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 1_000 {
				_ = runtime.Snapshot()
			}
		}()
	}
	for range 10 {
		if err := runtime.Reload([]Spec{spec}); err != nil {
			t.Fatalf("Reload failed: %v", err)
		}
	}
	readers.Wait()
	runtime.Stop()
}

func TestSpecValidationAndDeterministicJitter(t *testing.T) {
	if _, err := New([]Spec{{Name: "", Interval: time.Second, Sample: func(context.Context) (any, error) { return nil, nil }}}); err == nil {
		t.Fatal("expected empty name to fail")
	}
	if _, err := New([]Spec{{Name: "x", Interval: 0, Sample: func(context.Context) (any, error) { return nil, nil }}}); err == nil {
		t.Fatal("expected invalid interval to fail")
	}
	first := deterministicJitter("cpu", 7, 3, time.Second)
	second := deterministicJitter("cpu", 7, 3, time.Second)
	if first != second || first < 0 || first > time.Second {
		t.Fatalf("unexpected deterministic jitter: %s, %s", first, second)
	}
}

func waitResult(t *testing.T, runtime *Runtime, name string, predicate func(Result) bool) Result {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, exists := runtime.Snapshot().Results[name]
		if exists && predicate(result) {
			return result
		}
		time.Sleep(time.Millisecond)
	}
	result := runtime.Snapshot().Results[name]
	t.Fatalf("timed out waiting for sampler %q result: %+v", name, result)
	return Result{}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

type fakeWaiter struct {
	deadline time.Time
	ready    chan struct{}
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Wait(ctx context.Context, duration time.Duration) bool {
	ready := make(chan struct{})
	clock.mu.Lock()
	waiter := fakeWaiter{deadline: clock.now.Add(duration), ready: ready}
	clock.waiters = append(clock.waiters, waiter)
	clock.mu.Unlock()
	select {
	case <-ctx.Done():
		return false
	case <-ready:
		return true
	}
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	remaining := clock.waiters[:0]
	for _, waiter := range clock.waiters {
		if waiter.deadline.After(clock.now) {
			remaining = append(remaining, waiter)
			continue
		}
		close(waiter.ready)
	}
	clock.waiters = remaining
	clock.mu.Unlock()
}

func (clock *fakeClock) pendingDelays() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	delays := make([]time.Duration, len(clock.waiters))
	for index, waiter := range clock.waiters {
		delays[index] = waiter.deadline.Sub(clock.now)
	}
	return delays
}

func waitPendingDelay(t *testing.T, clock *fakeClock, expected time.Duration) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		delays := clock.pendingDelays()
		if len(delays) == 1 && delays[0] == expected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for delay %s; pending=%v", expected, clock.pendingDelays())
}
