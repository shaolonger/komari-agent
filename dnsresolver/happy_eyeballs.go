package dnsresolver

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
)

const (
	defaultFallbackDelay     = 250 * time.Millisecond
	maximumResolvedAddresses = 16
)

type addressDialFunc func(context.Context, string, string) (net.Conn, error)

type dialAttemptResult struct {
	connection net.Conn
	err        error
}

func dialWithResolver(
	ctx context.Context,
	network string,
	endpoint string,
	totalTimeout time.Duration,
	resolver *net.Resolver,
) (net.Conn, error) {
	if ctx == nil {
		return nil, errors.New("dial requires a context")
	}
	if resolver == nil {
		return nil, errors.New("dial requires a DNS resolver")
	}
	if totalTimeout <= 0 {
		totalTimeout = defaultHTTPDialTimeout
	}
	budgetContext, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, err
	}

	var addresses []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{literal.Unmap()}
	} else {
		lookupStarted := time.Now()
		cacheKey := strings.ToLower(strings.TrimSuffix(host, "."))
		addresses, err = resolvedHostCache.Lookup(budgetContext, cacheKey, func(loadContext context.Context) ([]netip.Addr, error) {
			hosts, lookupErr := resolver.LookupHost(loadContext, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			parsed := parseResolvedAddresses(hosts)
			if len(parsed) == 0 {
				return nil, errors.New("DNS response contained no usable IP addresses")
			}
			return parsed, nil
		})
		diagnostics.ObserveDNS(lookupStarted, err)
		if err != nil {
			return nil, err
		}
	}

	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	dialStarted := time.Now()
	connection, err := dialResolvedAddresses(
		budgetContext,
		network,
		port,
		addresses,
		preferIPv4First(),
		defaultFallbackDelay,
		dialer.DialContext,
	)
	diagnostics.ObserveDial(dialStarted, err)
	return connection, err
}

func parseResolvedAddresses(hosts []string) []netip.Addr {
	addresses := make([]netip.Addr, 0, min(len(hosts), maximumResolvedAddresses*2))
	seen := make(map[netip.Addr]struct{}, min(len(hosts), maximumResolvedAddresses*2))
	for _, host := range hosts {
		address, err := netip.ParseAddr(strings.TrimSpace(host))
		if err != nil {
			continue
		}
		address = address.Unmap()
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
		if len(addresses) >= maximumResolvedAddresses*2 {
			break
		}
	}
	return addresses
}

func dialResolvedAddresses(
	ctx context.Context,
	network string,
	port string,
	addresses []netip.Addr,
	preferIPv4 bool,
	fallbackDelay time.Duration,
	dial addressDialFunc,
) (net.Conn, error) {
	ordered := interleaveAddressFamilies(addresses, network, preferIPv4)
	if len(ordered) == 0 {
		return nil, errors.New("no resolved addresses match the requested network")
	}
	if fallbackDelay <= 0 {
		fallbackDelay = defaultFallbackDelay
	}
	attemptContext, cancel := context.WithCancel(ctx)
	results := make(chan dialAttemptResult, len(ordered))
	startAttempt := func(address netip.Addr) {
		go func() {
			connection, err := dial(attemptContext, network, net.JoinHostPort(address.String(), port))
			results <- dialAttemptResult{connection: connection, err: err}
		}()
	}

	next := 1
	active := 1
	startAttempt(ordered[0])
	timer := time.NewTimer(fallbackDelay)
	defer timer.Stop()
	var attemptErrors []error
	for {
		var timerChannel <-chan time.Time
		if next < len(ordered) {
			timerChannel = timer.C
		}
		select {
		case result := <-results:
			active--
			if result.err == nil && result.connection != nil {
				cancel()
				drainDialResults(results, active)
				return result.connection, nil
			}
			if result.connection != nil {
				_ = result.connection.Close()
			}
			if result.err == nil {
				result.err = errors.New("dial returned no connection")
			}
			if result.err != nil {
				attemptErrors = append(attemptErrors, result.err)
			}
			if active == 0 && next < len(ordered) {
				stopAndDrainTimer(timer)
				startAttempt(ordered[next])
				next++
				active++
				if next < len(ordered) {
					timer.Reset(fallbackDelay)
				}
				continue
			}
			if active == 0 && next >= len(ordered) {
				cancel()
				return nil, errors.Join(attemptErrors...)
			}
		case <-timerChannel:
			startAttempt(ordered[next])
			next++
			active++
			if next < len(ordered) {
				timer.Reset(fallbackDelay)
			}
		case <-ctx.Done():
			cancel()
			drainDialResults(results, active)
			return nil, ctx.Err()
		}
	}
}

func interleaveAddressFamilies(addresses []netip.Addr, network string, preferIPv4 bool) []netip.Addr {
	var ipv4 []netip.Addr
	var ipv6 []netip.Addr
	forceIPv4 := strings.HasSuffix(network, "4")
	forceIPv6 := strings.HasSuffix(network, "6")
	for _, address := range addresses {
		switch {
		case address.Is4() && !forceIPv6:
			ipv4 = append(ipv4, address)
		case address.Is6() && !forceIPv4:
			ipv6 = append(ipv6, address)
		}
	}
	primary, secondary := ipv6, ipv4
	if preferIPv4 {
		primary, secondary = ipv4, ipv6
	}
	if len(primary) == 0 {
		primary, secondary = secondary, primary
	}
	ordered := make([]netip.Addr, 0, min(len(primary)+len(secondary), maximumResolvedAddresses))
	for index := 0; len(ordered) < maximumResolvedAddresses && (index < len(primary) || index < len(secondary)); index++ {
		if index < len(primary) {
			ordered = append(ordered, primary[index])
		}
		if index < len(secondary) && len(ordered) < maximumResolvedAddresses {
			ordered = append(ordered, secondary[index])
		}
	}
	return ordered
}

func drainDialResults(results <-chan dialAttemptResult, count int) {
	if count <= 0 {
		return
	}
	go func() {
		for range count {
			result := <-results
			if result.connection != nil {
				_ = result.connection.Close()
			}
		}
	}()
}

func stopAndDrainTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
