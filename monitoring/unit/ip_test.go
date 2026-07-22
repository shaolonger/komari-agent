package monitoring

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExtractPublicIPAddressValidatesFamilyAndRejectsSensitiveRanges(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		family  ipFamily
		want    string
		wantErr bool
	}{
		{name: "IPv4 JSON", body: `{"ip":"8.8.8.8"}`, family: ipFamilyV4, want: "8.8.8.8"},
		{name: "IPv6 text", body: "2606:4700:4700::1111\n", family: ipFamilyV6, want: "2606:4700:4700::1111"},
		{name: "private IPv4", body: "10.0.0.1", family: ipFamilyV4, wantErr: true},
		{name: "CGNAT IPv4", body: "100.64.0.1", family: ipFamilyV4, wantErr: true},
		{name: "documentation IPv4", body: "192.0.2.1", family: ipFamilyV4, wantErr: true},
		{name: "private IPv6", body: "fd00::1", family: ipFamilyV6, wantErr: true},
		{name: "wrong family", body: "8.8.8.8", family: ipFamilyV6, wantErr: true},
		{name: "malformed", body: "not an address", family: ipFamilyV4, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address, err := extractPublicIPAddress([]byte(test.body), test.family)
			if test.wantErr {
				if err == nil {
					t.Fatalf("address = %s, want error", address)
				}
				return
			}
			if err != nil || address.String() != test.want {
				t.Fatalf("address = %s, err = %v, want %s", address, err, test.want)
			}
		})
	}
}

func TestPublicIPRaceReturnsFastValidResponseAndCancelsSlowRequests(t *testing.T) {
	slowStarted := make(chan struct{})
	slowCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/slow":
			close(slowStarted)
			<-request.Context().Done()
			close(slowCanceled)
		case "/invalid":
			_, _ = writer.Write([]byte("10.0.0.1"))
		case "/good":
			<-slowStarted
			_, _ = writer.Write([]byte(`{"ip":"8.8.4.4"}`))
		}
	}))
	defer server.Close()
	started := time.Now()
	address, err := racePublicIPAddress(context.Background(), ipFamilyV4, server.Client(), []string{
		server.URL + "/slow",
		server.URL + "/invalid",
		server.URL + "/good",
	})
	if err != nil || address != "8.8.4.4" {
		t.Fatalf("race address = %q, err = %v", address, err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("parallel race took %s", elapsed)
	}
	select {
	case <-slowCanceled:
	case <-time.After(time.Second):
		t.Fatal("winning public IP request did not cancel slow peer")
	}
}

func TestPublicIPResponseLimitRejectsDeclaredAndStreamingBombs(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"declared": func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Length", fmt.Sprint(maximumPublicIPResponseSize+1))
			writer.WriteHeader(http.StatusOK)
		},
		"streaming": func(writer http.ResponseWriter, _ *http.Request) {
			writer.(http.Flusher).Flush()
			_, _ = writer.Write([]byte(strings.Repeat("x", maximumPublicIPResponseSize+1)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			if _, err := fetchPublicIPAddress(context.Background(), ipFamilyV4, server.Client(), server.URL); err == nil || !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("response limit error = %v", err)
			}
		})
	}
}

func TestPublicIPRaceReturnsErrorWhenEveryServiceFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	_, err := racePublicIPAddress(context.Background(), ipFamilyV4, server.Client(), []string{server.URL, server.URL})
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("all-service failure error = %v", err)
	}
}

func TestIPAddressResolverNICModeNeverContactsPublicServices(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	resolver := &publicIPResolver{
		cache:         newPublicIPCache(time.Now, time.Hour, time.Minute),
		ipv4Client:    server.Client(),
		ipv6Client:    server.Client(),
		ipv4Endpoints: []string{server.URL},
		ipv6Endpoints: []string{server.URL},
		nicAddresses: func() (string, string, error) {
			return "10.0.0.5", "fd00::5", nil
		},
	}
	result := resolver.Resolve(context.Background(), ipAddressOptions{fromNIC: true})
	if result.err != nil || result.ipv4 != "10.0.0.5" || result.ipv6 != "fd00::5" {
		t.Fatalf("NIC result = %+v", result)
	}
	if requests.Load() != 0 {
		t.Fatalf("NIC privacy mode made %d public requests", requests.Load())
	}
}

func TestIPAddressResolverHonorsCustomFamiliesAndRejectsInvalidValues(t *testing.T) {
	var ipv4Requests atomic.Int32
	var ipv6Requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v4" {
			ipv4Requests.Add(1)
			_, _ = writer.Write([]byte("8.8.8.8"))
			return
		}
		ipv6Requests.Add(1)
		_, _ = writer.Write([]byte("2606:4700:4700::1111"))
	}))
	defer server.Close()
	resolver := &publicIPResolver{
		cache:         newPublicIPCache(time.Now, time.Hour, time.Minute),
		ipv4Client:    server.Client(),
		ipv6Client:    server.Client(),
		ipv4Endpoints: []string{server.URL + "/v4"},
		ipv6Endpoints: []string{server.URL + "/v6"},
		nicAddresses:  loadNICAddresses,
	}
	result := resolver.Resolve(context.Background(), ipAddressOptions{customIPv4: "1.1.1.1"})
	if result.err != nil || result.ipv4 != "1.1.1.1" || result.ipv6 != "2606:4700:4700::1111" {
		t.Fatalf("custom result = %+v", result)
	}
	if ipv4Requests.Load() != 0 || ipv6Requests.Load() != 1 {
		t.Fatalf("family requests = %d/%d", ipv4Requests.Load(), ipv6Requests.Load())
	}
	result = resolver.Resolve(context.Background(), ipAddressOptions{customIPv4: "not-an-ip"})
	if result.err == nil || ipv4Requests.Load() != 0 {
		t.Fatalf("invalid custom result = %+v, requests = %d", result, ipv4Requests.Load())
	}
}

func TestPublicIPCacheTTLConfigKeyFailureTTLAndConcurrency(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newPublicIPCache(func() time.Time { return now }, time.Hour, time.Minute)
	key := ipAddressOptions{customIPv4: "1.1.1.1"}
	var loads atomic.Int32
	loader := func(context.Context) ipAddressResult {
		loads.Add(1)
		return ipAddressResult{ipv4: "1.1.1.1", ipv6: "2606:4700:4700::1111"}
	}
	for range 2 {
		result := cache.Resolve(context.Background(), key, loader)
		if result.ipv4 == "" || result.ipv6 == "" {
			t.Fatalf("cache result = %+v", result)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("positive cache loads = %d", loads.Load())
	}
	now = now.Add(61 * time.Minute)
	_ = cache.Resolve(context.Background(), key, loader)
	if loads.Load() != 2 {
		t.Fatalf("expired positive cache loads = %d", loads.Load())
	}

	failureKey := ipAddressOptions{customIPv4: "2.2.2.2"}
	failureLoader := func(context.Context) ipAddressResult {
		loads.Add(1)
		return ipAddressResult{err: errors.New("all services failed")}
	}
	_ = cache.Resolve(context.Background(), failureKey, failureLoader)
	_ = cache.Resolve(context.Background(), failureKey, failureLoader)
	if loads.Load() != 3 {
		t.Fatalf("negative cache loads = %d", loads.Load())
	}
	now = now.Add(2 * time.Minute)
	_ = cache.Resolve(context.Background(), failureKey, failureLoader)
	if loads.Load() != 4 {
		t.Fatalf("expired negative cache loads = %d", loads.Load())
	}

	concurrentKey := ipAddressOptions{customIPv4: "3.3.3.3"}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	concurrentLoader := func(context.Context) ipAddressResult {
		loads.Add(1)
		once.Do(func() { close(started) })
		<-release
		return ipAddressResult{ipv4: "3.3.3.3"}
	}
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_ = cache.Resolve(context.Background(), concurrentKey, concurrentLoader)
		}()
	}
	<-started
	close(release)
	workers.Wait()
	if loads.Load() != 5 {
		t.Fatalf("concurrent cache loads = %d, want one additional load", loads.Load())
	}

	for index := range maximumPublicIPCacheEntries + 4 {
		config := ipAddressOptions{customIPv4: fmt.Sprintf("4.4.4.%d", index)}
		_ = cache.Resolve(context.Background(), config, func(context.Context) ipAddressResult {
			return ipAddressResult{ipv4: config.customIPv4}
		})
	}
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries > maximumPublicIPCacheEntries {
		t.Fatalf("cache entries = %d, limit = %d", entries, maximumPublicIPCacheEntries)
	}
}

func TestPublicIPCacheExplicitInvalidationReloads(t *testing.T) {
	cache := newPublicIPCache(time.Now, time.Hour, time.Minute)
	key := ipAddressOptions{}
	var loads atomic.Int32
	loader := func(context.Context) ipAddressResult {
		loads.Add(1)
		return ipAddressResult{ipv4: "8.8.8.8"}
	}
	_ = cache.Resolve(context.Background(), key, loader)
	_ = cache.Resolve(context.Background(), key, loader)
	cache.Clear()
	_ = cache.Resolve(context.Background(), key, loader)
	if loads.Load() != 2 {
		t.Fatalf("loads after explicit invalidation = %d, want 2", loads.Load())
	}
}

func TestPublicIPCacheUsesFailureTTLForPartialResults(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newPublicIPCache(func() time.Time { return now }, time.Hour, time.Minute)
	var loads atomic.Int32
	loader := func(context.Context) ipAddressResult {
		loads.Add(1)
		return ipAddressResult{ipv4: "8.8.8.8", err: errors.New("IPv6 services unavailable")}
	}
	_ = cache.Resolve(context.Background(), ipAddressOptions{}, loader)
	now = now.Add(2 * time.Minute)
	_ = cache.Resolve(context.Background(), ipAddressOptions{}, loader)
	if loads.Load() != 2 {
		t.Fatalf("partial-result loads = %d, want failure TTL reload", loads.Load())
	}
}

func TestIPAddressResolverHonorsGlobalBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	resolver := &publicIPResolver{
		cache:         newPublicIPCache(time.Now, time.Hour, time.Minute),
		ipv4Client:    server.Client(),
		ipv6Client:    server.Client(),
		ipv4Endpoints: []string{server.URL},
		ipv6Endpoints: []string{server.URL},
		nicAddresses:  loadNICAddresses,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := resolver.Resolve(ctx, ipAddressOptions{})
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("budget result error = %v", result.err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("global IP budget took %s", elapsed)
	}
}
