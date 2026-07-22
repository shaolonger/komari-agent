package monitoring

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLowFrequencySamplerCachesRefreshesAndKeepsStaleValue(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	var calls atomic.Int32
	var fail atomic.Bool
	sampler := newLowFrequencySampler(func() (int, error) {
		call := calls.Add(1)
		if fail.Load() {
			return 0, errors.New("fixture source failure")
		}
		return int(call), nil
	}, func() time.Time { return current }, 5*time.Second)

	if got, err := sampler.Sample(false); err != nil || got != 1 {
		t.Fatalf("first sample = %d, err = %v", got, err)
	}
	if got, err := sampler.Sample(false); err != nil || got != 1 || calls.Load() != 1 {
		t.Fatalf("cached sample = %d, err = %v, calls = %d", got, err, calls.Load())
	}
	current = current.Add(5 * time.Second)
	if got, err := sampler.Sample(false); err != nil || got != 2 {
		t.Fatalf("interval sample = %d, err = %v", got, err)
	}

	fail.Store(true)
	if got, err := sampler.Sample(true); err == nil || got != 2 {
		t.Fatalf("failed refresh = %d, err = %v", got, err)
	}
	if got, err := sampler.Sample(false); err == nil || got != 2 || calls.Load() != 3 {
		t.Fatalf("cached failure = %d, err = %v, calls = %d", got, err, calls.Load())
	}

	fail.Store(false)
	if got, err := sampler.Sample(true); err != nil || got != 4 {
		t.Fatalf("forced recovery = %d, err = %v", got, err)
	}
	current = current.Add(-time.Minute)
	if got, err := sampler.Sample(false); err != nil || got != 5 {
		t.Fatalf("clock rollback sample = %d, err = %v", got, err)
	}
}

func TestLowFrequencySamplerConcurrentColdReadUsesOneSourceCall(t *testing.T) {
	var calls atomic.Int32
	sampler := newLowFrequencySampler(func() (int, error) {
		calls.Add(1)
		return 42, nil
	}, time.Now, time.Hour)

	const readers = 64
	var wait sync.WaitGroup
	wait.Add(readers)
	for range readers {
		go func() {
			defer wait.Done()
			if got, err := sampler.Sample(false); err != nil || got != 42 {
				t.Errorf("sample = %d, err = %v", got, err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("source calls = %d, want 1", calls.Load())
	}
}

func TestLowFrequencySamplerInitialErrorRetriesAfterInterval(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	var calls atomic.Int32
	sampler := newLowFrequencySampler(func() (int, error) {
		if calls.Add(1) == 1 {
			return 0, errors.New("fixture first failure")
		}
		return 42, nil
	}, func() time.Time { return current }, 5*time.Second)

	if got, err := sampler.Sample(false); err == nil || got != 0 {
		t.Fatalf("initial failure = %d, err = %v", got, err)
	}
	// There is no valid value yet, so callers should be allowed to recover on
	// the next call instead of serving an empty value for the whole interval.
	if got, err := sampler.Sample(false); err != nil || got != 42 {
		t.Fatalf("initial retry = %d, err = %v", got, err)
	}
}
