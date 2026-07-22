package dnsresolver

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDNSAddressCacheHitExpiryAndLRUEviction(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newDNSAddressCache(2, time.Minute, func() time.Time { return now })
	var loads atomic.Int32
	load := func(address string) func(context.Context) ([]netip.Addr, error) {
		return func(context.Context) ([]netip.Addr, error) {
			loads.Add(1)
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}
	}

	a, err := cache.Lookup(context.Background(), "a.example", load("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	a[0] = netip.MustParseAddr("192.0.2.99")
	cachedA, err := cache.Lookup(context.Background(), "a.example", load("192.0.2.2"))
	if err != nil || cachedA[0].String() != "192.0.2.1" {
		t.Fatalf("cached A = %v, err = %v", cachedA, err)
	}
	if _, err := cache.Lookup(context.Background(), "b.example", load("192.0.2.2")); err != nil {
		t.Fatal(err)
	}
	// Touch A so B becomes the least-recently-used entry.
	if _, err := cache.Lookup(context.Background(), "a.example", load("192.0.2.3")); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Lookup(context.Background(), "c.example", load("192.0.2.3")); err != nil {
		t.Fatal(err)
	}
	if cache.Len() != 2 {
		t.Fatalf("cache length = %d, want bounded length 2", cache.Len())
	}
	if _, err := cache.Lookup(context.Background(), "b.example", load("192.0.2.22")); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 4 {
		t.Fatalf("loads after LRU eviction = %d, want 4", loads.Load())
	}

	now = now.Add(2 * time.Minute)
	expired, err := cache.Lookup(context.Background(), "a.example", load("192.0.2.10"))
	if err != nil || expired[0].String() != "192.0.2.10" {
		t.Fatalf("expired lookup = %v, err = %v", expired, err)
	}
}

func TestDNSAddressCacheCollapsesConcurrentLookups(t *testing.T) {
	cache := newDNSAddressCache(16, time.Minute, time.Now)
	var loads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func(context.Context) ([]netip.Addr, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return []netip.Addr{netip.MustParseAddr("2001:db8::1")}, nil
	}

	const workers = 64
	errorsChannel := make(chan error, workers)
	var waiters sync.WaitGroup
	waiters.Add(workers)
	for range workers {
		go func() {
			defer waiters.Done()
			addresses, err := cache.Lookup(context.Background(), "shared.example", loader)
			if err == nil && (len(addresses) != 1 || addresses[0].String() != "2001:db8::1") {
				err = errors.New("unexpected shared lookup result")
			}
			errorsChannel <- err
		}()
	}
	<-started
	close(release)
	waiters.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("concurrent loader calls = %d, want 1", loads.Load())
	}
}

func TestDNSAddressCacheWaiterCancellationAndClearDuringInflight(t *testing.T) {
	cache := newDNSAddressCache(4, time.Minute, time.Now)
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldFinished := make(chan struct{})
	go func() {
		defer close(oldFinished)
		_, _ = cache.Lookup(context.Background(), "change.example", func(context.Context) ([]netip.Addr, error) {
			close(oldStarted)
			<-releaseOld
			return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
		})
	}()
	<-oldStarted

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if _, err := cache.Lookup(waiterContext, "change.example", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}

	cache.Clear()
	newAddress, err := cache.Lookup(context.Background(), "change.example", func(context.Context) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.2")}, nil
	})
	if err != nil || newAddress[0].String() != "192.0.2.2" {
		t.Fatalf("post-clear address = %v, err = %v", newAddress, err)
	}
	close(releaseOld)
	<-oldFinished
	cached, err := cache.Lookup(context.Background(), "change.example", func(context.Context) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.3")}, nil
	})
	if err != nil || cached[0].String() != "192.0.2.2" {
		t.Fatalf("stale in-flight lookup overwrote cache: %v, err = %v", cached, err)
	}
}

func TestDNSLookupErrorsAreNotCached(t *testing.T) {
	cache := newDNSAddressCache(4, time.Minute, time.Now)
	var calls atomic.Int32
	loader := func(context.Context) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary DNS failure")
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	}
	if _, err := cache.Lookup(context.Background(), "retry.example", loader); err == nil {
		t.Fatal("temporary DNS error was hidden")
	}
	if addresses, err := cache.Lookup(context.Background(), "retry.example", loader); err != nil || len(addresses) != 1 {
		t.Fatalf("retry addresses = %v, err = %v", addresses, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want retry after error", calls.Load())
	}
}

func TestCustomDNSChangeInvalidatesAddressAndTransportCaches(t *testing.T) {
	useHTTPClientTestState(t)
	if _, err := resolvedHostCache.Lookup(context.Background(), "change.example", func(context.Context) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	oldClient := GetControlHTTPClient()
	if resolvedHostCache.Len() != 1 {
		t.Fatalf("cache length before DNS change = %d", resolvedHostCache.Len())
	}
	SetCustomDNSServer("1.1.1.1")
	if resolvedHostCache.Len() != 0 {
		t.Fatalf("cache length after DNS change = %d, want 0", resolvedHostCache.Len())
	}
	if GetControlHTTPClient() == oldClient {
		t.Fatal("DNS change reused a Transport with the previous resolver")
	}
}
