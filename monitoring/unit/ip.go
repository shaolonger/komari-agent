package monitoring

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/dnsresolver"
)

const (
	defaultPublicIPBudget       = 5 * time.Second
	defaultPublicIPCacheTTL     = time.Hour
	defaultPublicIPFailureTTL   = time.Minute
	defaultPublicIPDialTimeout  = 3 * time.Second
	maximumPublicIPResponseSize = 16 * 1024
	maximumPublicIPEndpoints    = 8
	maximumPublicIPCacheEntries = 8
)

type ipFamily uint8

const (
	ipFamilyV4 ipFamily = iota + 1
	ipFamilyV6
)

var (
	ipCandidatePattern = regexp.MustCompile(`[0-9A-Fa-f:.]+`)
	publicIPv4Services = []string{
		"https://www.visa.cn/cdn-cgi/trace",
		"https://www.qualcomm.cn/cdn-cgi/trace",
		"https://www.toutiao.com/stream/widget/local_weather/data/",
		"https://edge-ip.html.zone/geo",
		"https://vercel-ip.html.zone/geo",
		"https://api.ipify.org?format=json",
	}
	publicIPv6Services = []string{
		"https://v6.ip.zxinc.org/info.php?type=json",
		"https://api6.ipify.org?format=json",
		"https://ipv6.icanhazip.com",
	}
	publicIPDeniedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	publicIPv4HTTPClient    = newPublicIPHTTPClient("tcp4")
	publicIPv6HTTPClient    = newPublicIPHTTPClient("tcp6")
	defaultPublicIPResolver = newPublicIPResolver()
)

type ipAddressOptions struct {
	fromNIC     bool
	customIPv4  string
	customIPv6  string
	includeNICs string
	excludeNICs string
}

type ipAddressResult struct {
	ipv4 string
	ipv6 string
	err  error
}

type publicIPCacheEntry struct {
	result     ipAddressResult
	expiresAt  time.Time
	lastAccess uint64
}

type publicIPLookupCall struct {
	done   chan struct{}
	result ipAddressResult
	epoch  uint64
}

type publicIPCache struct {
	mu       sync.Mutex
	entries  map[ipAddressOptions]publicIPCacheEntry
	inflight map[ipAddressOptions]*publicIPLookupCall
	now      func() time.Time
	ttl      time.Duration
	failTTL  time.Duration
	sequence uint64
	epoch    uint64
}

type publicIPResolver struct {
	cache         *publicIPCache
	ipv4Client    *http.Client
	ipv6Client    *http.Client
	ipv4Endpoints []string
	ipv6Endpoints []string
	nicAddresses  func() (string, string, error)
}

func newPublicIPHTTPClient(network string) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, _ string, endpoint string) (net.Conn, error) {
			return dnsresolver.GetDialContext(defaultPublicIPDialTimeout)(ctx, network, endpoint)
		},
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Transport: transport}
}

func newPublicIPResolver() *publicIPResolver {
	return &publicIPResolver{
		cache:         newPublicIPCache(time.Now, defaultPublicIPCacheTTL, defaultPublicIPFailureTTL),
		ipv4Client:    publicIPv4HTTPClient,
		ipv6Client:    publicIPv6HTTPClient,
		ipv4Endpoints: append([]string(nil), publicIPv4Services...),
		ipv6Endpoints: append([]string(nil), publicIPv6Services...),
		nicAddresses:  loadNICAddresses,
	}
}

func newPublicIPCache(now func() time.Time, ttl, failTTL time.Duration) *publicIPCache {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = defaultPublicIPCacheTTL
	}
	if failTTL <= 0 {
		failTTL = defaultPublicIPFailureTTL
	}
	return &publicIPCache{
		entries:  make(map[ipAddressOptions]publicIPCacheEntry),
		inflight: make(map[ipAddressOptions]*publicIPLookupCall),
		now:      now,
		ttl:      ttl,
		failTTL:  failTTL,
	}
}

func GetIPv4Address() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultPublicIPBudget)
	defer cancel()
	return racePublicIPAddress(ctx, ipFamilyV4, publicIPv4HTTPClient, publicIPv4Services)
}

func GetIPv6Address() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultPublicIPBudget)
	defer cancel()
	return racePublicIPAddress(ctx, ipFamilyV6, publicIPv6HTTPClient, publicIPv6Services)
}

func GetIPAddress() (ipv4, ipv6 string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultPublicIPBudget)
	defer cancel()
	return GetIPAddressContext(ctx)
}

func GetIPAddressContext(ctx context.Context) (ipv4, ipv6 string, err error) {
	if ctx == nil {
		return "", "", errors.New("IP address lookup requires a context")
	}
	options := ipAddressOptions{
		fromNIC:     flags.GetIpAddrFromNic,
		customIPv4:  strings.TrimSpace(flags.CustomIpv4),
		customIPv6:  strings.TrimSpace(flags.CustomIpv6),
		includeNICs: flags.IncludeNics,
		excludeNICs: flags.ExcludeNics,
	}
	result := defaultPublicIPResolver.Resolve(ctx, options)
	return result.ipv4, result.ipv6, result.err
}

func InvalidateIPAddressCache() {
	defaultPublicIPResolver.cache.Clear()
}

func (resolver *publicIPResolver) Resolve(ctx context.Context, options ipAddressOptions) ipAddressResult {
	if options.fromNIC {
		ipv4, ipv6, err := resolver.nicAddresses()
		return ipAddressResult{ipv4: ipv4, ipv6: ipv6, err: err}
	}
	customIPv4, err4 := validateCustomIPAddress(options.customIPv4, ipFamilyV4)
	customIPv6, err6 := validateCustomIPAddress(options.customIPv6, ipFamilyV6)
	if err := errors.Join(err4, err6); err != nil {
		return ipAddressResult{err: err}
	}
	if customIPv4 != "" && customIPv6 != "" {
		return ipAddressResult{ipv4: customIPv4, ipv6: customIPv6}
	}

	return resolver.cache.Resolve(ctx, options, func(loadContext context.Context) ipAddressResult {
		result := ipAddressResult{ipv4: customIPv4, ipv6: customIPv6}
		type familyResult struct {
			family  ipFamily
			address string
			err     error
		}
		results := make(chan familyResult, 2)
		requests := 0
		if customIPv4 == "" {
			requests++
			go func() {
				address, err := racePublicIPAddress(loadContext, ipFamilyV4, resolver.ipv4Client, resolver.ipv4Endpoints)
				results <- familyResult{family: ipFamilyV4, address: address, err: err}
			}()
		}
		if customIPv6 == "" {
			requests++
			go func() {
				address, err := racePublicIPAddress(loadContext, ipFamilyV6, resolver.ipv6Client, resolver.ipv6Endpoints)
				results <- familyResult{family: ipFamilyV6, address: address, err: err}
			}()
		}
		var lookupErrors []error
		for range requests {
			select {
			case <-loadContext.Done():
				return ipAddressResult{ipv4: result.ipv4, ipv6: result.ipv6, err: loadContext.Err()}
			case family := <-results:
				if family.family == ipFamilyV4 {
					result.ipv4 = family.address
				} else {
					result.ipv6 = family.address
				}
				if family.err != nil {
					lookupErrors = append(lookupErrors, family.err)
				}
			}
		}
		result.err = errors.Join(lookupErrors...)
		return result
	})
}

func (cache *publicIPCache) Resolve(
	ctx context.Context,
	key ipAddressOptions,
	load func(context.Context) ipAddressResult,
) ipAddressResult {
	now := cache.now()
	cache.mu.Lock()
	cache.sequence++
	if entry, ok := cache.entries[key]; ok && now.Before(entry.expiresAt) {
		entry.lastAccess = cache.sequence
		cache.entries[key] = entry
		cache.mu.Unlock()
		return entry.result
	}
	delete(cache.entries, key)
	if call := cache.inflight[key]; call != nil {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return ipAddressResult{err: ctx.Err()}
		case <-call.done:
			return call.result
		}
	}
	call := &publicIPLookupCall{done: make(chan struct{}), epoch: cache.epoch}
	cache.inflight[key] = call
	cache.mu.Unlock()

	result := load(ctx)
	cache.mu.Lock()
	if cache.inflight[key] == call {
		delete(cache.inflight, key)
	}
	call.result = result
	if call.epoch == cache.epoch && !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
		ttl := cache.ttl
		if result.err != nil || result.ipv4 == "" && result.ipv6 == "" {
			ttl = cache.failTTL
		}
		cache.evictOneLocked()
		cache.sequence++
		cache.entries[key] = publicIPCacheEntry{result: result, expiresAt: cache.now().Add(ttl), lastAccess: cache.sequence}
	}
	close(call.done)
	cache.mu.Unlock()
	return result
}

func (cache *publicIPCache) Clear() {
	cache.mu.Lock()
	cache.entries = make(map[ipAddressOptions]publicIPCacheEntry)
	cache.inflight = make(map[ipAddressOptions]*publicIPLookupCall)
	cache.epoch++
	cache.mu.Unlock()
}

func (cache *publicIPCache) evictOneLocked() {
	if len(cache.entries) < maximumPublicIPCacheEntries {
		return
	}
	var oldestKey ipAddressOptions
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

func racePublicIPAddress(
	ctx context.Context,
	family ipFamily,
	client *http.Client,
	endpoints []string,
) (string, error) {
	if client == nil || len(endpoints) == 0 {
		return "", errors.New("no public IP services configured")
	}
	if len(endpoints) > maximumPublicIPEndpoints {
		endpoints = endpoints[:maximumPublicIPEndpoints]
	}
	raceContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		address string
		err     error
	}
	results := make(chan result, len(endpoints))
	for _, endpoint := range endpoints {
		go func() {
			address, err := fetchPublicIPAddress(raceContext, family, client, endpoint)
			results <- result{address: address, err: err}
		}()
	}
	lookupErrors := make([]error, 0, len(endpoints))
	for range len(endpoints) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case response := <-results:
			if response.err == nil && response.address != "" {
				return response.address, nil
			}
			if response.err != nil {
				lookupErrors = append(lookupErrors, response.err)
			}
		}
	}
	return "", errors.Join(lookupErrors...)
}

func fetchPublicIPAddress(ctx context.Context, family ipFamily, client *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "komari-agent")
	started := time.Now()
	resp, err := client.Do(req)
	diagnostics.ObserveHTTP(started, err)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("public IP service returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > maximumPublicIPResponseSize {
		return "", errors.New("public IP response exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumPublicIPResponseSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > maximumPublicIPResponseSize {
		return "", errors.New("public IP response exceeds size limit")
	}
	address, err := extractPublicIPAddress(body, family)
	if err != nil {
		return "", err
	}
	return address.String(), nil
}

func extractPublicIPAddress(body []byte, family ipFamily) (netip.Addr, error) {
	for _, candidate := range ipCandidatePattern.FindAll(body, -1) {
		address, err := netip.ParseAddr(string(candidate))
		if err != nil {
			continue
		}
		address = address.Unmap()
		if family == ipFamilyV4 && !address.Is4() || family == ipFamilyV6 && !address.Is6() {
			continue
		}
		if isPublicIPAddress(address) {
			return address, nil
		}
	}
	return netip.Addr{}, errors.New("public IP response contained no valid public address")
}

func isPublicIPAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() ||
		address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, prefix := range publicIPDeniedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validateCustomIPAddress(value string, family ipFamily) (string, error) {
	if value == "" {
		return "", nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return "", errors.New("invalid custom IP address")
	}
	address = address.Unmap()
	if family == ipFamilyV4 && !address.Is4() || family == ipFamilyV6 && !address.Is6() {
		return "", errors.New("custom IP address has the wrong family")
	}
	return address.String(), nil
}

func loadNICAddresses() (string, string, error) {
	allowNICs, err := InterfaceList()
	if err != nil {
		return "", "", err
	}
	ipv4, ipv6 := getIPFromInterfaces(allowNICs)
	return ipv4, ipv6, nil
}

// getIPFromInterfaces returns addresses only from the already filtered NIC set.
func getIPFromInterfaces(nicNames []string) (ipv4, ipv6 string) {
	allowed := make(map[string]struct{}, len(nicNames))
	for _, name := range nicNames {
		allowed[name] = struct{}{}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, networkInterface := range interfaces {
		if _, ok := allowed[networkInterface.Name]; !ok || networkInterface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addresses {
			var address net.IP
			switch value := raw.(type) {
			case *net.IPNet:
				address = value.IP
			case *net.IPAddr:
				address = value.IP
			}
			if address == nil || address.IsLoopback() {
				continue
			}
			if ipv4 == "" && address.To4() != nil {
				ipv4 = address.String()
			}
			if ipv6 == "" && address.To4() == nil && !address.IsLinkLocalUnicast() {
				ipv6 = address.String()
			}
			if ipv4 != "" && ipv6 != "" {
				return ipv4, ipv6
			}
		}
	}
	return ipv4, ipv6
}
