package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	ping "github.com/prometheus-community/pro-bing"
	"golang.org/x/net/idna"
)

type pingResultWriter interface {
	WriteJSON(v interface{}) error
}

const (
	defaultMaxConcurrentPings     = 2
	defaultPingMinInterval        = 500 * time.Millisecond
	maximumPingTargetLength       = 2048
	maximumPingResolvedAddresses  = 32
	maximumPingPolicyStringLength = 1024
	maximumAllowedPingPorts       = 128
	maximumConcurrentPings        = 64
	maximumPingMinInterval        = time.Hour
	pingResolutionTimeout         = 3 * time.Second
	pingProbeTimeout              = 3 * time.Second
	pingTaskTimeout               = 10 * time.Second
	maximumPingResponseHeaderSize = 64 * 1024
	maximumPingHTTPClients        = 64
	pingHTTPClientTTL             = 10 * time.Minute
	pingHTTPIdleTimeout           = 90 * time.Second
)

type pingProbeType uint8

const (
	pingProbeTCP pingProbeType = iota + 1
	pingProbeHTTP
	pingProbeICMP
)

const (
	pingTypeMaskTCP uint8 = 1 << iota
	pingTypeMaskHTTP
	pingTypeMaskICMP
	defaultPingTypeMask = pingTypeMaskTCP | pingTypeMaskHTTP | pingTypeMaskICMP
)

var defaultAllowedPingTCPPorts = []int{80, 443, 8443}

type pingPolicyKey struct {
	types        string
	ports        string
	allowPrivate bool
}

type compiledPingPolicy struct {
	key          pingPolicyKey
	typeMask     uint8
	ports        map[uint16]struct{}
	allowPrivate bool
}

var (
	pingPolicyValue atomic.Pointer[compiledPingPolicy]
	pingPolicyMu    sync.Mutex

	pingExecutionSlotsMu    sync.Mutex
	pingExecutionSlots      chan struct{}
	pingExecutionSlotsLimit int

	pingRateLimitMu    sync.Mutex
	lastAcceptedPingAt time.Time

	defaultPingDialer      = &net.Dialer{KeepAlive: 30 * time.Second}
	defaultPingHTTPClients = newPingHTTPClientCache(maximumPingHTTPClients, pingHTTPClientTTL, time.Now)
)

type pingIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type pingTargetDefinition struct {
	probeType pingProbeType
	host      string
	port      uint16
	url       *url.URL
}

type resolvedPingTarget struct {
	definition pingTargetDefinition
	addresses  []netip.Addr
	pinned     netip.Addr
}

type pingHTTPClientKey struct {
	scheme  string
	host    string
	port    uint16
	address netip.Addr
}

type pingHTTPClientEntry struct {
	client     *http.Client
	expiresAt  time.Time
	lastAccess uint64
}

type pingHTTPClientCache struct {
	mu       sync.Mutex
	entries  map[pingHTTPClientKey]pingHTTPClientEntry
	capacity int
	ttl      time.Duration
	now      func() time.Time
	sequence uint64
}

var restrictedPingPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func currentPingPolicy() *compiledPingPolicy {
	key := pingPolicyKey{
		types:        flags.AllowedPingTypes,
		ports:        flags.AllowedPingTCPPorts,
		allowPrivate: flags.AllowPrivatePingTargets,
	}
	if cached := pingPolicyValue.Load(); cached != nil && cached.key == key {
		return cached
	}
	pingPolicyMu.Lock()
	defer pingPolicyMu.Unlock()
	if cached := pingPolicyValue.Load(); cached != nil && cached.key == key {
		return cached
	}
	compiled := compilePingPolicy(key)
	pingPolicyValue.Store(compiled)
	return compiled
}

func compilePingPolicy(key pingPolicyKey) *compiledPingPolicy {
	policy := &compiledPingPolicy{key: key, allowPrivate: key.allowPrivate}
	if strings.TrimSpace(key.types) == "" {
		policy.typeMask = defaultPingTypeMask
	} else if len(key.types) <= maximumPingPolicyStringLength {
		for _, value := range strings.Split(key.types, ",") {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "tcp":
				policy.typeMask |= pingTypeMaskTCP
			case "http":
				policy.typeMask |= pingTypeMaskHTTP
			case "icmp":
				policy.typeMask |= pingTypeMaskICMP
			}
		}
	}
	policy.ports = make(map[uint16]struct{})
	if strings.TrimSpace(key.ports) == "" {
		for _, port := range defaultAllowedPingTCPPorts {
			policy.ports[uint16(port)] = struct{}{}
		}
		return policy
	}
	if len(key.ports) > maximumPingPolicyStringLength {
		return policy
	}
	for _, value := range strings.Split(key.ports, ",") {
		port, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
		if err == nil && port != 0 {
			policy.ports[uint16(port)] = struct{}{}
			if len(policy.ports) > maximumAllowedPingPorts {
				clear(policy.ports)
				return policy
			}
		}
	}
	return policy
}

func (policy *compiledPingPolicy) allowsType(probeType pingProbeType) bool {
	switch probeType {
	case pingProbeTCP:
		return policy.typeMask&pingTypeMaskTCP != 0
	case pingProbeHTTP:
		return policy.typeMask&pingTypeMaskHTTP != 0
	case pingProbeICMP:
		return policy.typeMask&pingTypeMaskICMP != 0
	default:
		return false
	}
}

func (policy *compiledPingPolicy) allowsPort(port uint16) bool {
	_, allowed := policy.ports[port]
	return allowed
}

func pingAllowedTypes() []string {
	policy := currentPingPolicy()
	types := make([]string, 0, 3)
	if policy.typeMask&pingTypeMaskTCP != 0 {
		types = append(types, "tcp")
	}
	if policy.typeMask&pingTypeMaskHTTP != 0 {
		types = append(types, "http")
	}
	if policy.typeMask&pingTypeMaskICMP != 0 {
		types = append(types, "icmp")
	}
	return types
}

func pingAllowedPorts() []int {
	policy := currentPingPolicy()
	ports := make([]int, 0, len(policy.ports))
	for port := range policy.ports {
		ports = append(ports, int(port))
	}
	sort.Ints(ports)
	return ports
}

func pingTypeAllowed(value string) bool {
	probeType, err := parsePingProbeType(value)
	return err == nil && currentPingPolicy().allowsType(probeType)
}

func pingPortAllowed(port int) bool {
	return port > 0 && port <= 65535 && currentPingPolicy().allowsPort(uint16(port))
}

func parsePingProbeType(value string) (pingProbeType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp":
		return pingProbeTCP, nil
	case "http":
		return pingProbeHTTP, nil
	case "icmp":
		return pingProbeICMP, nil
	default:
		return 0, errors.New("unsupported ping type")
	}
}

func parsePingTarget(pingType, pingTarget string) (string, int, error) {
	definition, err := parseAuthorizedPingTarget(currentPingPolicy(), pingType, pingTarget)
	if err != nil {
		return "", 0, err
	}
	return definition.host, int(definition.port), nil
}

func parseAuthorizedPingTarget(policy *compiledPingPolicy, pingType, pingTarget string) (pingTargetDefinition, error) {
	probeType, err := parsePingProbeType(pingType)
	if err != nil || !policy.allowsType(probeType) {
		return pingTargetDefinition{}, errors.New("ping type is not allowed")
	}
	trimmedTarget := strings.TrimSpace(pingTarget)
	if trimmedTarget == "" || len(trimmedTarget) > maximumPingTargetLength {
		return pingTargetDefinition{}, errors.New("invalid ping target length")
	}
	definition := pingTargetDefinition{probeType: probeType}
	switch probeType {
	case pingProbeICMP:
		host := trimmedTarget
		if splitHost, _, splitErr := net.SplitHostPort(trimmedTarget); splitErr == nil {
			host = splitHost
		}
		definition.host, err = normalizePingHost(host)
	case pingProbeTCP:
		host, portText, splitErr := net.SplitHostPort(trimmedTarget)
		if splitErr != nil {
			host, portText = trimmedTarget, "80"
		}
		definition.host, err = normalizePingHost(host)
		if err == nil {
			definition.port, err = parsePingPort(portText)
		}
	case pingProbeHTTP:
		definition, err = parseHTTPPingTarget(trimmedTarget)
	}
	if err != nil {
		return pingTargetDefinition{}, err
	}
	if definition.port != 0 && !policy.allowsPort(definition.port) {
		return pingTargetDefinition{}, errors.New("ping port is not allowed")
	}
	return definition, nil
}

func parseHTTPPingTarget(target string) (pingTargetDefinition, error) {
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil || parsed.Opaque != "" || parsed.User != nil {
		return pingTargetDefinition{}, errors.New("invalid HTTP ping target")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return pingTargetDefinition{}, errors.New("invalid HTTP ping scheme")
	}
	host, err := normalizePingHost(parsed.Hostname())
	if err != nil {
		return pingTargetDefinition{}, err
	}
	if strings.Contains(host, "%") {
		return pingTargetDefinition{}, errors.New("scoped IPv6 is not supported for HTTP ping")
	}
	port := uint16(80)
	if parsed.Scheme == "https" {
		port = 443
	}
	explicitPort := parsed.Port() != ""
	if explicitPort {
		port, err = parsePingPort(parsed.Port())
		if err != nil {
			return pingTargetDefinition{}, err
		}
	}
	parsed.Host = formatPingURLHost(host, port, explicitPort)
	return pingTargetDefinition{probeType: pingProbeHTTP, host: host, port: port, url: parsed}, nil
}

func parsePingPort(value string) (uint16, error) {
	port, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
	if err != nil || port == 0 {
		return 0, errors.New("invalid ping port")
	}
	return uint16(port), nil
}

func normalizePingHost(value string) (string, error) {
	host := strings.TrimSpace(strings.Trim(value, "[]"))
	if host == "" {
		return "", errors.New("invalid ping host")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", errors.New("invalid ping host")
	}
	return strings.ToLower(ascii), nil
}

func formatPingURLHost(host string, port uint16, explicitPort bool) string {
	if explicitPort {
		return net.JoinHostPort(host, strconv.Itoa(int(port)))
	}
	if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
		return "[" + host + "]"
	}
	return host
}

func pingTargetAllowed(pingType, pingTarget string) error {
	ctx, cancel := context.WithTimeout(context.Background(), pingResolutionTimeout)
	defer cancel()
	_, err := preparePingTarget(ctx, currentPingPolicy(), pingType, pingTarget, dnsresolver.GetCustomResolver())
	return err
}

func preparePingTarget(
	ctx context.Context,
	policy *compiledPingPolicy,
	pingType, pingTarget string,
	resolver pingIPResolver,
) (*resolvedPingTarget, error) {
	definition, err := parseAuthorizedPingTarget(policy, pingType, pingTarget)
	if err != nil {
		return nil, err
	}
	return resolvePingTarget(ctx, policy, definition, resolver)
}

func resolvePingTarget(
	ctx context.Context,
	policy *compiledPingPolicy,
	definition pingTargetDefinition,
	resolver pingIPResolver,
) (*resolvedPingTarget, error) {
	if ctx == nil {
		return nil, errors.New("ping resolution requires a context")
	}
	var addresses []netip.Addr
	if address, err := netip.ParseAddr(definition.host); err == nil {
		addresses = []netip.Addr{address}
	} else {
		if resolver == nil {
			return nil, errors.New("ping resolver is unavailable")
		}
		lookupStarted := time.Now()
		resolved, err := resolver.LookupNetIP(ctx, "ip", definition.host)
		diagnostics.ObserveDNS(lookupStarted, err)
		if err != nil {
			return nil, errors.New("failed to resolve ping target")
		}
		addresses = resolved
	}
	if len(addresses) == 0 || len(addresses) > maximumPingResolvedAddresses {
		return nil, errors.New("ping target returned an invalid address count")
	}
	validated := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if err := validatePingAddress(address, policy.allowPrivate); err != nil {
			return nil, err
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		validated = append(validated, address)
	}
	if len(validated) == 0 {
		return nil, errors.New("ping target returned no unique addresses")
	}
	return &resolvedPingTarget{definition: definition, addresses: validated, pinned: validated[0]}, nil
}

func validatePingAddress(address netip.Addr, allowPrivate bool) error {
	if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
		return errors.New("ping target resolved to an invalid address")
	}
	if allowPrivate {
		return nil
	}
	if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return errors.New("ping target resolves to a restricted address")
	}
	for _, prefix := range restrictedPingPrefixes {
		if prefix.Contains(address) {
			return errors.New("ping target resolves to a restricted address")
		}
	}
	return nil
}

func pingConcurrencyLimit() int {
	if flags.MaxConcurrentPings <= 0 {
		return defaultMaxConcurrentPings
	}
	return min(flags.MaxConcurrentPings, maximumConcurrentPings)
}

func pingMinInterval() time.Duration {
	if flags.PingMinIntervalMillis < 0 {
		return defaultPingMinInterval
	}
	interval := time.Duration(flags.PingMinIntervalMillis) * time.Millisecond
	if interval < 0 || interval > maximumPingMinInterval {
		return maximumPingMinInterval
	}
	return interval
}

func tryAcquirePingExecutionSlot() (func(), bool) {
	limit := pingConcurrencyLimit()
	if limit < 1 {
		limit = 1
	}
	pingExecutionSlotsMu.Lock()
	if pingExecutionSlots == nil || pingExecutionSlotsLimit != limit {
		pingExecutionSlots = make(chan struct{}, limit)
		pingExecutionSlotsLimit = limit
	}
	slots := pingExecutionSlots
	pingExecutionSlotsMu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return func() {}, false
	}
}

func allowPingNow() bool {
	interval := pingMinInterval()
	pingRateLimitMu.Lock()
	defer pingRateLimitMu.Unlock()
	now := time.Now()
	if !lastAcceptedPingAt.IsZero() && now.Sub(lastAcceptedPingAt) < interval {
		return false
	}
	lastAcceptedPingAt = now
	return true
}

func icmpPingResolvedContext(parent context.Context, target *resolvedPingTarget, timeout time.Duration) (int64, error) {
	if parent == nil {
		return -1, errors.New("ICMP ping requires a context")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	pinger, err := ping.NewPinger(target.pinned.String())
	if err != nil {
		return -1, err
	}
	pinger.Count = 1
	pinger.Timeout = timeout
	pinger.SetPrivileged(true)
	if err := pinger.RunWithContext(ctx); err != nil {
		return -1, err
	}
	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		return -1, errors.New("no packets received")
	}
	return stats.AvgRtt.Milliseconds(), nil
}

func icmpPingResolved(target *resolvedPingTarget, timeout time.Duration) (int64, error) {
	return icmpPingResolvedContext(context.Background(), target, timeout)
}

func tcpPingResolvedContext(parent context.Context, target *resolvedPingTarget, timeout time.Duration) (int64, error) {
	if parent == nil {
		return -1, errors.New("TCP ping requires a context")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	endpoint := net.JoinHostPort(target.pinned.String(), strconv.Itoa(int(target.definition.port)))
	started := time.Now()
	connection, err := defaultPingDialer.DialContext(ctx, "tcp", endpoint)
	diagnostics.ObserveDial(started, err)
	if err != nil {
		return -1, err
	}
	_ = connection.Close()
	return time.Since(started).Milliseconds(), nil
}

func tcpPingResolved(target *resolvedPingTarget, timeout time.Duration) (int64, error) {
	return tcpPingResolvedContext(context.Background(), target, timeout)
}

func newPingHTTPClientCache(capacity int, ttl time.Duration, now func() time.Time) *pingHTTPClientCache {
	if capacity <= 0 {
		capacity = maximumPingHTTPClients
	}
	if ttl <= 0 {
		ttl = pingHTTPClientTTL
	}
	if now == nil {
		now = time.Now
	}
	return &pingHTTPClientCache{
		entries:  make(map[pingHTTPClientKey]pingHTTPClientEntry, capacity),
		capacity: capacity,
		ttl:      ttl,
		now:      now,
	}
}

func pingHTTPKey(target *resolvedPingTarget) pingHTTPClientKey {
	return pingHTTPClientKey{
		scheme:  target.definition.url.Scheme,
		host:    target.definition.host,
		port:    target.definition.port,
		address: target.pinned,
	}
}

func (cache *pingHTTPClientCache) Get(target *resolvedPingTarget) *http.Client {
	key := pingHTTPKey(target)
	now := cache.now()
	cache.mu.Lock()
	cache.sequence++
	if entry, exists := cache.entries[key]; exists && now.Before(entry.expiresAt) {
		entry.lastAccess = cache.sequence
		cache.entries[key] = entry
		cache.mu.Unlock()
		return entry.client
	} else if exists {
		delete(cache.entries, key)
		entry.client.CloseIdleConnections()
	}
	client := buildPinnedHTTPClient(target, pingProbeTimeout)
	if len(cache.entries) >= cache.capacity {
		var oldestKey pingHTTPClientKey
		var oldestAccess uint64
		first := true
		for candidateKey, entry := range cache.entries {
			if first || entry.lastAccess < oldestAccess {
				oldestKey = candidateKey
				oldestAccess = entry.lastAccess
				first = false
			}
		}
		oldest := cache.entries[oldestKey]
		delete(cache.entries, oldestKey)
		oldest.client.CloseIdleConnections()
	}
	cache.sequence++
	cache.entries[key] = pingHTTPClientEntry{
		client:     client,
		expiresAt:  now.Add(cache.ttl),
		lastAccess: cache.sequence,
	}
	cache.mu.Unlock()
	return client
}

func (cache *pingHTTPClientCache) Clear() {
	cache.mu.Lock()
	entries := cache.entries
	cache.entries = make(map[pingHTTPClientKey]pingHTTPClientEntry, cache.capacity)
	cache.mu.Unlock()
	for _, entry := range entries {
		entry.client.CloseIdleConnections()
	}
}

func (cache *pingHTTPClientCache) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}

func buildPinnedHTTPClient(target *resolvedPingTarget, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = pingProbeTimeout
	}
	serverName := strings.TrimSuffix(target.definition.host, ".")
	endpoint := net.JoinHostPort(target.pinned.String(), strconv.Itoa(int(target.definition.port)))
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			started := time.Now()
			connection, err := defaultPingDialer.DialContext(ctx, network, endpoint)
			diagnostics.ObserveDial(started, err)
			return connection, err
		},
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        4,
		IdleConnTimeout:        pingHTTPIdleTimeout,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maximumPingResponseHeaderSize,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newPinnedHTTPClient(target *resolvedPingTarget, timeout time.Duration) *http.Client {
	return buildPinnedHTTPClient(target, timeout)
}

func httpPingResolvedContext(parent context.Context, target *resolvedPingTarget, timeout time.Duration) (latency int64, resultErr error) {
	if parent == nil {
		return -1, errors.New("HTTP ping requires a context")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	client := defaultPingHTTPClients.Get(target)
	started := time.Now()
	defer func() { diagnostics.ObserveHTTP(started, resultErr) }()
	response, err := executePingHTTPRequest(ctx, client, target, http.MethodHead)
	if err != nil {
		return -1, err
	}
	if response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented {
		_ = response.Body.Close()
		response, err = executePingHTTPRequest(ctx, client, target, http.MethodGet)
		if err != nil {
			return -1, err
		}
	}
	latency = time.Since(started).Milliseconds()
	_ = response.Body.Close()
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return latency, nil
	}
	return latency, errors.New("HTTP ping returned a non-success status")
}

func executePingHTTPRequest(
	ctx context.Context,
	client *http.Client,
	target *resolvedPingTarget,
	method string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, target.definition.url.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "komari-agent")
	if method == http.MethodGet {
		request.Header.Set("Range", "bytes=0-0")
	}
	return client.Do(request)
}

func httpPingResolved(target *resolvedPingTarget, timeout time.Duration) (int64, error) {
	return httpPingResolvedContext(context.Background(), target, timeout)
}

func prepareDirectPingTarget(pingType, target string) (*resolvedPingTarget, error) {
	policy := compilePingPolicy(pingPolicyKey{
		types:        "tcp,http,icmp",
		ports:        "1,80,443,8443,65535",
		allowPrivate: true,
	})
	definition, err := parseAuthorizedPingTarget(policy, pingType, target)
	if err != nil {
		// Compatibility wrappers accept arbitrary valid ports.
		policy.ports = map[uint16]struct{}{}
		if probeType, probeErr := parsePingProbeType(pingType); probeErr == nil {
			if probeType == pingProbeTCP {
				_, portText, splitErr := net.SplitHostPort(target)
				if splitErr == nil {
					if port, portErr := parsePingPort(portText); portErr == nil {
						policy.ports[port] = struct{}{}
					}
				}
			} else if probeType == pingProbeHTTP {
				candidate := target
				if !strings.Contains(candidate, "://") {
					candidate = "http://" + candidate
				}
				if parsed, parseErr := url.Parse(candidate); parseErr == nil {
					port := uint16(80)
					if parsed.Scheme == "https" {
						port = 443
					}
					if parsed.Port() != "" {
						port, _ = parsePingPort(parsed.Port())
					}
					policy.ports[port] = struct{}{}
				}
			}
		}
		definition, err = parseAuthorizedPingTarget(policy, pingType, target)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingResolutionTimeout)
	defer cancel()
	return resolvePingTarget(ctx, policy, definition, dnsresolver.GetCustomResolver())
}

func icmpPing(target string, timeout time.Duration) (int64, error) {
	resolved, err := prepareDirectPingTarget("icmp", target)
	if err != nil {
		return -1, err
	}
	return icmpPingResolved(resolved, timeout)
}

func tcpPing(target string, timeout time.Duration) (int64, error) {
	resolved, err := prepareDirectPingTarget("tcp", target)
	if err != nil {
		return -1, err
	}
	return tcpPingResolved(resolved, timeout)
}

func httpPing(target string, timeout time.Duration) (int64, error) {
	resolved, err := prepareDirectPingTarget("http", target)
	if err != nil {
		return -1, err
	}
	return httpPingResolved(resolved, timeout)
}

type pingMeasureFunc func(context.Context) (int64, error)

func measurePingWithRetries(
	ctx context.Context,
	probeType pingProbeType,
	measure pingMeasureFunc,
) (int64, error) {
	const highLatencyThreshold int64 = 1000
	const retryDropThresholdTCP int64 = 800
	if ctx == nil {
		return -1, errors.New("ping measurement requires a context")
	}
	latency, err := measure(ctx)
	if err != nil || latency <= highLatencyThreshold {
		return latency, err
	}
	firstLatency := latency
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		second, retryErr := measure(ctx)
		if retryErr != nil {
			return -1, retryErr
		}
		if second <= highLatencyThreshold {
			if probeType == pingProbeTCP && firstLatency-second > retryDropThresholdTCP {
				return -1, errors.New("suspicious retransmission detected in TCP handshake")
			}
			return second, nil
		}
	}
	return -1, errors.New("latency remains high after retries")
}

func NewPingTask(conn pingResultWriter, taskID uint, pingType, pingTarget string) {
	NewPingTaskContext(context.Background(), conn, taskID, pingType, pingTarget)
}

func NewPingTaskContext(parent context.Context, conn pingResultWriter, taskID uint, pingType, pingTarget string) {
	if parent == nil {
		parent = context.Background()
	}
	pingStarted := time.Now()
	if taskID == 0 {
		log.Printf("Invalid task ID: %d", taskID)
		diagnostics.RecordPingRejected()
		return
	}
	if !flags.PingEnabled() {
		log.Printf("Ping task %d rejected: ping capability is disabled", taskID)
		writePingResult(conn, taskID, pingType, -1)
		diagnostics.RecordPingRejected()
		return
	}
	policy := currentPingPolicy()
	definition, err := parseAuthorizedPingTarget(policy, pingType, pingTarget)
	if err != nil {
		log.Printf("Ping task %d rejected: %v", taskID, err)
		writePingResult(conn, taskID, pingType, -1)
		diagnostics.RecordPingRejected()
		return
	}
	releasePingSlot, ok := tryAcquirePingExecutionSlot()
	if !ok {
		log.Printf("Ping task %d rejected: concurrent ping limit reached", taskID)
		writePingResult(conn, taskID, pingType, -1)
		diagnostics.RecordPingRejected()
		return
	}
	defer releasePingSlot()
	if !allowPingNow() {
		log.Printf("Ping task %d rejected: ping rate limit reached", taskID)
		writePingResult(conn, taskID, pingType, -1)
		diagnostics.RecordPingRejected()
		return
	}
	taskContext, cancelTask := context.WithTimeout(parent, pingTaskTimeout)
	defer cancelTask()
	resolveContext, cancelResolve := context.WithTimeout(taskContext, pingResolutionTimeout)
	resolved, err := resolvePingTarget(resolveContext, policy, definition, dnsresolver.GetCustomResolver())
	cancelResolve()
	if err != nil {
		log.Printf("Ping task %d rejected: %v", taskID, err)
		writePingResult(conn, taskID, pingType, -1)
		diagnostics.RecordPingRejected()
		return
	}
	pingResult := -1
	measure := func(ctx context.Context) (int64, error) {
		switch resolved.definition.probeType {
		case pingProbeICMP:
			return icmpPingResolvedContext(ctx, resolved, pingProbeTimeout)
		case pingProbeTCP:
			return tcpPingResolvedContext(ctx, resolved, pingProbeTimeout)
		case pingProbeHTTP:
			return httpPingResolvedContext(ctx, resolved, pingProbeTimeout)
		default:
			return -1, errors.New("unsupported ping type")
		}
	}
	latency, err := measurePingWithRetries(taskContext, resolved.definition.probeType, measure)
	if err != nil {
		log.Printf("Ping task %d failed: %v", taskID, err)
	} else {
		pingResult = int(latency)
	}
	diagnostics.ObservePing(pingStarted, err)
	writePingResult(conn, taskID, pingType, pingResult)
}

func writePingResult(conn pingResultWriter, taskID uint, pingType string, pingResult int) {
	payload := map[string]interface{}{
		"type":        "ping_result",
		"task_id":     taskID,
		"ping_type":   pingType,
		"value":       pingResult,
		"finished_at": time.Now(),
	}
	if err := conn.WriteJSON(payload); err != nil {
		log.Printf("Failed to write JSON to WebSocket: %v", err)
	}
}
