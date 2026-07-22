package server

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStaticBasicInfoCacheLoadsOnceConcurrentlyAndInvalidates(t *testing.T) {
	var loads atomic.Int32
	cache := newStaticBasicInfoCache(func() staticBasicInfo {
		generation := loads.Add(1)
		return staticBasicInfo{CPUName: fmt.Sprintf("cpu-%d", generation)}
	})
	const workers = 64
	var waiters sync.WaitGroup
	results := make(chan staticBasicInfo, workers)
	for range workers {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			results <- cache.Get()
		}()
	}
	waiters.Wait()
	close(results)
	for result := range results {
		if result.CPUName != "cpu-1" {
			t.Fatalf("concurrent result = %+v", result)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", loads.Load())
	}
	cache.Invalidate()
	if result := cache.Get(); result.CPUName != "cpu-2" || loads.Load() != 2 {
		t.Fatalf("refreshed result = %+v, loads = %d", result, loads.Load())
	}
}
