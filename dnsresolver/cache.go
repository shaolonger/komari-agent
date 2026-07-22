package dnsresolver

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

const (
	defaultDNSCacheCapacity = 256
	defaultDNSCacheTTL      = time.Minute
)

var resolvedHostCache = newDNSAddressCache(defaultDNSCacheCapacity, defaultDNSCacheTTL, time.Now)

type dnsCacheEntry struct {
	addresses  []netip.Addr
	expiresAt  time.Time
	lastAccess uint64
}

type dnsLookupCall struct {
	done      chan struct{}
	addresses []netip.Addr
	err       error
	epoch     uint64
}

type dnsAddressCache struct {
	mu       sync.Mutex
	entries  map[string]dnsCacheEntry
	inflight map[string]*dnsLookupCall
	capacity int
	ttl      time.Duration
	now      func() time.Time
	sequence uint64
	epoch    uint64
}

func newDNSAddressCache(capacity int, ttl time.Duration, now func() time.Time) *dnsAddressCache {
	if capacity <= 0 {
		capacity = defaultDNSCacheCapacity
	}
	if ttl <= 0 {
		ttl = defaultDNSCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &dnsAddressCache{
		entries:  make(map[string]dnsCacheEntry, capacity),
		inflight: make(map[string]*dnsLookupCall),
		capacity: capacity,
		ttl:      ttl,
		now:      now,
	}
}

func (cache *dnsAddressCache) Lookup(
	ctx context.Context,
	key string,
	load func(context.Context) ([]netip.Addr, error),
) ([]netip.Addr, error) {
	now := cache.now()
	cache.mu.Lock()
	cache.sequence++
	if entry, ok := cache.entries[key]; ok {
		if now.Before(entry.expiresAt) {
			entry.lastAccess = cache.sequence
			cache.entries[key] = entry
			addresses := append([]netip.Addr(nil), entry.addresses...)
			cache.mu.Unlock()
			return addresses, nil
		}
		delete(cache.entries, key)
	}
	if call := cache.inflight[key]; call != nil {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			return append([]netip.Addr(nil), call.addresses...), call.err
		}
	}
	call := &dnsLookupCall{done: make(chan struct{}), epoch: cache.epoch}
	cache.inflight[key] = call
	cache.mu.Unlock()

	addresses, err := load(ctx)
	addresses = append([]netip.Addr(nil), addresses...)

	cache.mu.Lock()
	if cache.inflight[key] == call {
		delete(cache.inflight, key)
	}
	call.addresses = addresses
	call.err = err
	if err == nil && len(addresses) > 0 && call.epoch == cache.epoch {
		cache.sequence++
		cache.evictOneLocked()
		cache.entries[key] = dnsCacheEntry{
			addresses:  append([]netip.Addr(nil), addresses...),
			expiresAt:  cache.now().Add(cache.ttl),
			lastAccess: cache.sequence,
		}
	}
	close(call.done)
	cache.mu.Unlock()
	return addresses, err
}

func (cache *dnsAddressCache) Clear() {
	cache.mu.Lock()
	cache.entries = make(map[string]dnsCacheEntry, cache.capacity)
	cache.inflight = make(map[string]*dnsLookupCall)
	cache.epoch++
	cache.mu.Unlock()
}

func (cache *dnsAddressCache) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}

func (cache *dnsAddressCache) evictOneLocked() {
	if len(cache.entries) < cache.capacity {
		return
	}
	var oldestKey string
	var oldestAccess uint64
	first := true
	for key, entry := range cache.entries {
		if first || entry.lastAccess < oldestAccess {
			oldestKey = key
			oldestAccess = entry.lastAccess
			first = false
		}
	}
	delete(cache.entries, oldestKey)
}
