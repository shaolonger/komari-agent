package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

type pingResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (function pingResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return function(ctx, network, host)
}

func testPingPolicy(types, ports string, allowPrivate bool) *compiledPingPolicy {
	return compilePingPolicy(pingPolicyKey{types: types, ports: ports, allowPrivate: allowPrivate})
}

func TestPingPolicyCacheCompilesImmutableSetsOnce(t *testing.T) {
	useServerFlagsSnapshot(t)
	flags.AllowedPingTypes = " TCP, http,tcp,unknown "
	flags.AllowedPingTCPPorts = "443,80,443,invalid,0,65536"
	flags.AllowPrivatePingTargets = false
	first := currentPingPolicy()
	if !first.allowsType(pingProbeTCP) || !first.allowsType(pingProbeHTTP) || first.allowsType(pingProbeICMP) {
		t.Fatalf("compiled type mask = %03b", first.typeMask)
	}
	if got := pingAllowedPorts(); len(got) != 2 || got[0] != 80 || got[1] != 443 {
		t.Fatalf("compiled ports = %v", got)
	}

	const workers = 64
	pointers := make(chan *compiledPingPolicy, workers)
	var waiters sync.WaitGroup
	for range workers {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			pointers <- currentPingPolicy()
		}()
	}
	waiters.Wait()
	close(pointers)
	for policy := range pointers {
		if policy != first {
			t.Fatal("concurrent policy read recompiled the same configuration")
		}
	}

	flags.AllowPrivatePingTargets = true
	second := currentPingPolicy()
	if second == first || !second.allowPrivate {
		t.Fatal("policy key change did not compile a new immutable snapshot")
	}
}

func TestPingPolicyFailsClosedForInvalidOrOversizedConfiguration(t *testing.T) {
	invalid := testPingPolicy("unknown", "invalid", false)
	if invalid.typeMask != 0 || len(invalid.ports) != 0 {
		t.Fatalf("invalid policy = mask %d ports %v", invalid.typeMask, invalid.ports)
	}
	oversized := testPingPolicy(strings.Repeat("tcp,", maximumPingPolicyStringLength), strings.Repeat("80,", maximumPingPolicyStringLength), false)
	if oversized.typeMask != 0 || len(oversized.ports) != 0 {
		t.Fatalf("oversized policy = mask %d ports %d", oversized.typeMask, len(oversized.ports))
	}
	defaults := testPingPolicy("", "", false)
	if defaults.typeMask != defaultPingTypeMask || len(defaults.ports) != 3 {
		t.Fatalf("default policy = mask %d ports %v", defaults.typeMask, defaults.ports)
	}
}

func TestResolvePingTargetRejectsMixedPublicAndRestrictedDNSAnswers(t *testing.T) {
	policy := testPingPolicy("tcp", "443", false)
	definition, err := parseAuthorizedPingTarget(policy, "tcp", "mixed.example:443")
	if err != nil {
		t.Fatal(err)
	}
	for name, addresses := range map[string][]netip.Addr{
		"IPv4": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
		"IPv6": {netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("fd00::1")},
	} {
		t.Run(name, func(t *testing.T) {
			resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return addresses, nil
			})
			if _, err := resolvePingTarget(context.Background(), policy, definition, resolver); err == nil || !strings.Contains(err.Error(), "restricted") {
				t.Fatalf("mixed DNS error = %v", err)
			}
		})
	}
}

func TestResolvePingTargetPinsSingleValidatedDNSGeneration(t *testing.T) {
	policy := testPingPolicy("tcp", "443", false)
	definition, err := parseAuthorizedPingTarget(policy, "tcp", "rebind.example:443")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	target, err := resolvePingTarget(context.Background(), policy, definition, resolver)
	if err != nil || target.pinned.String() != "8.8.8.8" || calls.Load() != 1 {
		t.Fatalf("pinned target = %+v, calls = %d, err = %v", target, calls.Load(), err)
	}
	endpoint := net.JoinHostPort(target.pinned.String(), strconv.Itoa(int(target.definition.port)))
	if endpoint != "8.8.8.8:443" || calls.Load() != 1 {
		t.Fatalf("execution endpoint = %q, resolver calls = %d", endpoint, calls.Load())
	}
}

func TestTCPPingExecutionUsesPinnedAddressWithoutSecondLookup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	policy := testPingPolicy("tcp", portText, true)
	definition, err := parseAuthorizedPingTarget(policy, "tcp", "rebind.example:"+portText)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
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
	if _, err := tcpPingResolved(target, time.Second); err != nil {
		t.Fatalf("pinned TCP ping failed: %v", err)
	}
	<-accepted
	if calls.Load() != 1 {
		t.Fatalf("TCP execution performed %d DNS lookups", calls.Load())
	}
}

func TestResolvePingTargetBoundsAndValidatesEveryAnswer(t *testing.T) {
	policy := testPingPolicy("tcp", "443", false)
	definition, _ := parseAuthorizedPingTarget(policy, "tcp", "many.example:443")
	tooMany := make([]netip.Addr, maximumPingResolvedAddresses+1)
	for index := range tooMany {
		tooMany[index] = netip.AddrFrom4([4]byte{8, 8, byte(index), 1})
	}
	resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return tooMany, nil })
	if _, err := resolvePingTarget(context.Background(), policy, definition, resolver); err == nil || !strings.Contains(err.Error(), "address count") {
		t.Fatalf("too-many-address error = %v", err)
	}
	resolver = pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil })
	if _, err := resolvePingTarget(context.Background(), policy, definition, resolver); err == nil {
		t.Fatal("empty DNS answer was accepted")
	}
}

func TestResolvePingTargetHonorsContext(t *testing.T) {
	policy := testPingPolicy("tcp", "443", false)
	definition, _ := parseAuthorizedPingTarget(policy, "tcp", "slow.example:443")
	resolver := pingResolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := resolvePingTarget(ctx, policy, definition, resolver)
	if err == nil || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("resolver context error = %v, elapsed = %s", err, time.Since(started))
	}
}

func TestPrivatePingOptInStillRejectsInvalidAddressClasses(t *testing.T) {
	if err := validatePingAddress(netip.MustParseAddr("127.0.0.1"), true); err != nil {
		t.Fatalf("private opt-in rejected loopback: %v", err)
	}
	for _, address := range []string{"0.0.0.0", "224.0.0.1", "::", "ff02::1"} {
		if err := validatePingAddress(netip.MustParseAddr(address), true); err == nil {
			t.Fatalf("private opt-in accepted invalid address %s", address)
		}
	}
}

func TestHTTPPingDisablesRedirectsIncludingPrivateTargets(t *testing.T) {
	var destinationRequests atomic.Int32
	var redirectRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationRequests.Add(1)
	}))
	defer destination.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectRequests.Add(1)
		http.Redirect(writer, request, destination.URL, http.StatusFound)
	}))
	defer redirector.Close()
	_, portText, _ := net.SplitHostPort(redirector.Listener.Addr().String())
	policy := testPingPolicy("http", portText, true)
	target, err := preparePingTarget(context.Background(), policy, "http", redirector.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := httpPingResolved(target, time.Second); err == nil || destinationRequests.Load() != 0 || redirectRequests.Load() != 1 {
		t.Fatalf("redirect error = %v, source/destination requests = %d/%d", err, redirectRequests.Load(), destinationRequests.Load())
	}
}

func TestPinnedHTTPSPreservesSNIHostAndCertificateVerification(t *testing.T) {
	serverName := make(chan string, 1)
	hostHeader := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverName <- request.TLS.ServerName
		hostHeader <- request.Host
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	policy := testPingPolicy("http", portText, true)
	resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	target, err := preparePingTarget(context.Background(), policy, "http", "https://example.com:"+portText+"/health", resolver)
	if err != nil {
		t.Fatal(err)
	}
	client := newPinnedHTTPClient(target, 2*time.Second)
	transport := client.Transport.(*http.Transport)
	if transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "example.com" || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("TLS policy = %+v", transport.TLSClientConfig)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig.RootCAs = roots
	response, err := client.Get(target.definition.url.String())
	if err != nil {
		t.Fatalf("pinned HTTPS request failed: %v", err)
	}
	response.Body.Close()
	if got := <-serverName; got != "example.com" {
		t.Fatalf("TLS SNI = %q", got)
	}
	if got := <-hostHeader; got != "example.com:"+portText {
		t.Fatalf("HTTP Host = %q", got)
	}
}

func TestPingTargetParsesPublicIPv6AndAllowedPort(t *testing.T) {
	policy := testPingPolicy("http,tcp", "443", false)
	target, err := preparePingTarget(context.Background(), policy, "http", "https://[2606:4700:4700::1111]/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !target.pinned.Is6() || target.definition.url.Host != "[2606:4700:4700::1111]" || target.definition.port != 443 {
		t.Fatalf("IPv6 target = %+v", target)
	}
	if _, err := preparePingTarget(context.Background(), policy, "tcp", "[2606:4700:4700::1111]:80", nil); err == nil {
		t.Fatal("disallowed IPv6 TCP port was accepted")
	}
}

func TestPingConcurrencyAndIntervalConfigurationAreBounded(t *testing.T) {
	useServerFlagsSnapshot(t)
	flags.MaxConcurrentPings = maximumConcurrentPings * 100
	flags.PingMinIntervalMillis = int(maximumPingMinInterval/time.Millisecond) + 1
	if got := pingConcurrencyLimit(); got != maximumConcurrentPings {
		t.Fatalf("concurrency limit = %d", got)
	}
	if got := pingMinInterval(); got != maximumPingMinInterval {
		t.Fatalf("minimum interval = %s", got)
	}
}

func TestResolvePingTargetPreservesResolverErrorsAsGenericFailures(t *testing.T) {
	policy := testPingPolicy("tcp", "443", false)
	definition, _ := parseAuthorizedPingTarget(policy, "tcp", "error.example:443")
	resolver := pingResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("sensitive resolver detail")
	})
	_, err := resolvePingTarget(context.Background(), policy, definition, resolver)
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("resolver error was not sanitized: %v", err)
	}
}
