package dnsresolver

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func successfulPipe(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client
}

func TestHappyEyeballsStartsOtherFamilyAfterFallbackDelay(t *testing.T) {
	addresses := []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.1"),
	}
	var callsMu sync.Mutex
	var calls []string
	dial := func(ctx context.Context, _, endpoint string) (net.Conn, error) {
		callsMu.Lock()
		calls = append(calls, endpoint)
		callsMu.Unlock()
		if strings.Contains(endpoint, "2001:db8") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return successfulPipe(t), nil
	}
	started := time.Now()
	connection, err := dialResolvedAddresses(context.Background(), "tcp", "443", addresses, false, 20*time.Millisecond, dial)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	elapsed := time.Since(started)
	if elapsed < 15*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("fallback elapsed = %s", elapsed)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if len(calls) != 2 || !strings.Contains(calls[0], "2001:db8") || !strings.Contains(calls[1], "192.0.2.1") {
		t.Fatalf("dial calls = %v", calls)
	}
}

func TestHappyEyeballsImmediateFailureDoesNotWaitFallbackDelay(t *testing.T) {
	addresses := []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.1"),
	}
	dial := func(_ context.Context, _, endpoint string) (net.Conn, error) {
		if strings.Contains(endpoint, "2001:db8") {
			return nil, errors.New("unreachable IPv6")
		}
		return successfulPipe(t), nil
	}
	started := time.Now()
	connection, err := dialResolvedAddresses(context.Background(), "tcp", "443", addresses, false, 250*time.Millisecond, dial)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("immediate fallback waited %s", elapsed)
	}
}

func TestHappyEyeballsHonorsTotalCancellationBudget(t *testing.T) {
	addresses := []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("2001:db8::2"),
	}
	var attempts atomic.Int32
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		attempts.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := dialResolvedAddresses(ctx, "tcp", "443", addresses, false, 5*time.Millisecond, dial)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("canceled dial took %s", elapsed)
	}
	if attempts.Load() < 2 || attempts.Load() > int32(len(addresses)) {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestAddressParsingDeduplicatesBoundsAndInterleavesFamilies(t *testing.T) {
	hosts := []string{"invalid", "192.0.2.1", "::ffff:192.0.2.1", "2001:db8::1", "2001:db8::2"}
	for index := 2; index < 100; index++ {
		hosts = append(hosts, "192.0.2."+strconv.Itoa(index))
	}
	parsed := parseResolvedAddresses(hosts)
	if len(parsed) != maximumResolvedAddresses*2 {
		t.Fatalf("parsed addresses = %d, want bounded %d", len(parsed), maximumResolvedAddresses*2)
	}
	ordered := interleaveAddressFamilies(parsed, "tcp", false)
	if len(ordered) != maximumResolvedAddresses || !ordered[0].Is6() || !ordered[1].Is4() {
		t.Fatalf("interleaved addresses = %v", ordered)
	}
	ipv4Only := interleaveAddressFamilies(parsed, "tcp4", false)
	for _, address := range ipv4Only {
		if !address.Is4() {
			t.Fatalf("tcp4 list contains %s", address)
		}
	}
	ipv6Only := interleaveAddressFamilies(parsed, "tcp6", true)
	for _, address := range ipv6Only {
		if !address.Is6() {
			t.Fatalf("tcp6 list contains %s", address)
		}
	}
}

func TestAllFailedDialAttemptsReturnError(t *testing.T) {
	addresses := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}
	_, err := dialResolvedAddresses(context.Background(), "tcp", "443", addresses, true, time.Millisecond, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture failure")
	})
	if err == nil || !strings.Contains(err.Error(), "fixture failure") {
		t.Fatalf("all-failed error = %v", err)
	}
}
