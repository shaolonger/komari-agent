package monitoring

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStaticHostInfoCacheLoadsOnceConcurrently(t *testing.T) {
	var calls atomic.Int32
	cache := newStaticHostInfoCache(func() (StaticHostInfo, error) {
		calls.Add(1)
		return StaticHostInfo{CPUName: "fixture", CPUCores: 8}, nil
	})

	const readers = 32
	var wait sync.WaitGroup
	wait.Add(readers)
	for range readers {
		go func() {
			defer wait.Done()
			if got := cache.Get(); got.CPUName != "fixture" || got.CPUCores != 8 {
				t.Errorf("unexpected cached value: %+v", got)
			}
		}()
	}
	wait.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("loader called %d times, want 1", got)
	}
}

func TestStaticHostInfoCacheRefreshAndErrorKeepsStaleValue(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	cache := newStaticHostInfoCache(func() (StaticHostInfo, error) {
		call := calls.Add(1)
		if fail.Load() {
			return StaticHostInfo{}, errors.New("fixture refresh failure")
		}
		return StaticHostInfo{CPUName: "fixture", CPUCores: int(call)}, nil
	})

	if got := cache.Get(); got.CPUCores != 1 {
		t.Fatalf("first value = %+v, want generation 1", got)
	}
	if got := cache.Refresh(); got.CPUCores != 2 {
		t.Fatalf("refreshed value = %+v, want generation 2", got)
	}
	fail.Store(true)
	if got := cache.Refresh(); got.CPUCores != 2 {
		t.Fatalf("failed refresh replaced stale value: %+v", got)
	}
	if got := cache.Get(); got.CPUCores != 2 {
		t.Fatalf("cached value changed after failed refresh: %+v", got)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("loader called %d times, want 3", got)
	}
}

func TestStaticHostInfoCacheRetriesFailedInitialLoad(t *testing.T) {
	var calls atomic.Int32
	cache := newStaticHostInfoCache(func() (StaticHostInfo, error) {
		if calls.Add(1) == 1 {
			return StaticHostInfo{}, errors.New("fixture initial failure")
		}
		return StaticHostInfo{CPUName: "recovered"}, nil
	})

	if got := cache.Get(); got != (StaticHostInfo{}) {
		t.Fatalf("failed initial load returned %+v", got)
	}
	if got := cache.Get(); got.CPUName != "recovered" {
		t.Fatalf("retry did not recover: %+v", got)
	}
}
