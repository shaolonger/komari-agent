package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func prepareLocalHTTPPingTarget(t *testing.T, rawURL string) *resolvedPingTarget {
	t.Helper()
	parsedPort := uint16(80)
	request, err := http.NewRequest(http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.Scheme == "https" {
		parsedPort = 443
	}
	if request.URL.Port() != "" {
		value, parseErr := strconv.ParseUint(request.URL.Port(), 10, 16)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		parsedPort = uint16(value)
	}
	policy := testPingPolicy("http", strconv.Itoa(int(parsedPort)), true)
	target, err := preparePingTarget(context.Background(), policy, "http", rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPingHTTPTransportReusesKeepAliveForSamePinnedTarget(t *testing.T) {
	defaultPingHTTPClients.Clear()
	t.Cleanup(defaultPingHTTPClients.Clear)
	var connections atomic.Int32
	var methodsMu sync.Mutex
	var methods []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methodsMu.Lock()
		methods = append(methods, request.Method)
		methodsMu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	target := prepareLocalHTTPPingTarget(t, server.URL)
	for range 2 {
		if _, err := httpPingResolved(target, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	methodsMu.Lock()
	gotMethods := append([]string(nil), methods...)
	methodsMu.Unlock()
	if connections.Load() != 1 || len(gotMethods) != 2 || gotMethods[0] != http.MethodHead || gotMethods[1] != http.MethodHead {
		t.Fatalf("connections = %d, methods = %v", connections.Load(), gotMethods)
	}
}

func TestPingHTTPClientCacheIsBoundedExpiresAndIsolatesPinnedIPs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newPingHTTPClientCache(2, time.Minute, func() time.Time { return now })
	t.Cleanup(cache.Clear)
	base := prepareLocalHTTPPingTarget(t, "http://127.0.0.1:80")
	targetA := *base
	targetA.definition.host = "cache.example"
	targetA.definition.url.Host = "cache.example"
	targetA.pinned = netip.MustParseAddr("127.0.0.1")
	targetB := targetA
	targetB.pinned = netip.MustParseAddr("127.0.0.2")
	targetC := targetA
	targetC.pinned = netip.MustParseAddr("127.0.0.3")
	targetD := targetA
	targetD.definition.host = "other.example"
	targetD.definition.url.Host = "other.example"
	clientA := cache.Get(&targetA)
	if cache.Get(&targetA) != clientA {
		t.Fatal("same pinned target did not reuse client")
	}
	clientB := cache.Get(&targetB)
	if clientB == clientA {
		t.Fatal("different pinned IP shared a transport")
	}
	isolatedCache := newPingHTTPClientCache(2, time.Minute, func() time.Time { return now })
	t.Cleanup(isolatedCache.Clear)
	if isolatedCache.Get(&targetA) == isolatedCache.Get(&targetD) {
		t.Fatal("different TLS/Host identities shared a transport")
	}
	_ = cache.Get(&targetA) // B is now least recently used.
	clientC := cache.Get(&targetC)
	if cache.Len() != 2 || cache.Get(&targetB) == clientB {
		t.Fatal("bounded LRU did not evict the least-recently-used client")
	}
	now = now.Add(2 * time.Minute)
	if cache.Get(&targetC) == clientC {
		t.Fatal("expired HTTP client was reused")
	}
}

func TestPingHTTPClientCacheCollapsesConcurrentColdReads(t *testing.T) {
	cache := newPingHTTPClientCache(8, time.Minute, time.Now)
	t.Cleanup(cache.Clear)
	target := prepareLocalHTTPPingTarget(t, "http://127.0.0.1:80")
	const workers = 64
	clients := make(chan *http.Client, workers)
	var waiters sync.WaitGroup
	for range workers {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			clients <- cache.Get(target)
		}()
	}
	waiters.Wait()
	close(clients)
	var first *http.Client
	for client := range clients {
		if first == nil {
			first = client
		} else if client != first {
			t.Fatal("concurrent cache miss created multiple clients")
		}
	}
	if cache.Len() != 1 {
		t.Fatalf("concurrent cache entries = %d", cache.Len())
	}
}

func TestHTTPPingFallsBackFromHEADToBoundedRangeGET(t *testing.T) {
	defaultPingHTTPClients.Clear()
	t.Cleanup(defaultPingHTTPClients.Clear)
	var headRequests atomic.Int32
	var getRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodHead:
			headRequests.Add(1)
			writer.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			getRequests.Add(1)
			if request.Header.Get("Range") != "bytes=0-0" || request.Header.Get("Accept-Encoding") != "identity" {
				t.Errorf("fallback headers = Range %q Accept-Encoding %q", request.Header.Get("Range"), request.Header.Get("Accept-Encoding"))
			}
			writer.Header().Set("Content-Range", "bytes 0-0/1000000000")
			writer.Header().Set("Content-Length", "1")
			writer.WriteHeader(http.StatusPartialContent)
			_, _ = writer.Write([]byte("x"))
		}
	}))
	defer server.Close()
	if _, err := httpPingResolved(prepareLocalHTTPPingTarget(t, server.URL), time.Second); err != nil {
		t.Fatal(err)
	}
	if headRequests.Load() != 1 || getRequests.Load() != 1 {
		t.Fatalf("HEAD/GET requests = %d/%d", headRequests.Load(), getRequests.Load())
	}
}

func TestHTTPPingDoesNotWaitForOrDownloadSlowUnboundedBody(t *testing.T) {
	defaultPingHTTPClients.Clear()
	t.Cleanup(defaultPingHTTPClients.Clear)
	bodyStarted := make(chan struct{})
	bodyCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		close(bodyStarted)
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(bodyCanceled)
	}))
	defer server.Close()
	started := time.Now()
	latency, err := httpPingResolved(prepareLocalHTTPPingTarget(t, server.URL), time.Second)
	if err != nil || time.Since(started) > 250*time.Millisecond || latency > 250 {
		t.Fatalf("slow body latency = %dms, elapsed = %s, err = %v", latency, time.Since(started), err)
	}
	<-bodyStarted
	select {
	case <-bodyCanceled:
	case <-time.After(time.Second):
		t.Fatal("closing bounded GET did not cancel slow body")
	}
}

func TestHTTPPingStatusAndCancellation(t *testing.T) {
	for name, status := range map[string]int{"success": http.StatusNoContent, "failure": http.StatusNotFound} {
		t.Run(name, func(t *testing.T) {
			defaultPingHTTPClients.Clear()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
			}))
			defer server.Close()
			_, err := httpPingResolved(prepareLocalHTTPPingTarget(t, server.URL), time.Second)
			if status < 300 && err != nil || status >= 300 && err == nil {
				t.Fatalf("status %d error = %v", status, err)
			}
		})
	}
	defaultPingHTTPClients.Clear()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := httpPingResolvedContext(ctx, prepareLocalHTTPPingTarget(t, server.URL), time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("HTTP cancellation error = %v, elapsed = %s", err, time.Since(started))
	}
}

func TestHTTPPingRejectsOversizedResponseHeaders(t *testing.T) {
	defaultPingHTTPClients.Clear()
	t.Cleanup(defaultPingHTTPClients.Clear)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Oversized", strings.Repeat("x", maximumPingResponseHeaderSize+1))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if _, err := httpPingResolved(prepareLocalHTTPPingTarget(t, server.URL), time.Second); err == nil {
		t.Fatal("oversized response headers were accepted")
	}
}

func TestPingRetryLoopHonorsOneParentBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	started := time.Now()
	_, err := measurePingWithRetries(ctx, pingProbeHTTP, func(ctx context.Context) (int64, error) {
		if calls.Add(1) == 1 {
			return 2000, nil
		}
		<-ctx.Done()
		return -1, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 2 || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("retry budget error = %v, calls = %d, elapsed = %s", err, calls.Load(), time.Since(started))
	}
}

func TestPingLatencyExcludesValidatedDNSTime(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	policy := testPingPolicy("tcp", portText, true)
	definition, _ := parseAuthorizedPingTarget(policy, "tcp", "latency.example:"+portText)
	resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		time.Sleep(50 * time.Millisecond)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	totalStarted := time.Now()
	target, err := resolvePingTarget(context.Background(), policy, definition, resolver)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
		close(accepted)
	}()
	latency, err := tcpPingResolved(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	<-accepted
	if time.Since(totalStarted) < 50*time.Millisecond || latency >= 40 {
		t.Fatalf("reported TCP latency %dms included DNS; total = %s", latency, time.Since(totalStarted))
	}
}
