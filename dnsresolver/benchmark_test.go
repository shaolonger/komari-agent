package dnsresolver

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

var benchmarkHTTPClient *http.Client
var benchmarkDNSAddresses []netip.Addr

func BenchmarkNewVerifiedHTTPClient(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkHTTPClient = GetVerifiedHTTPClient(30 * time.Second)
	}
}

func BenchmarkNewConfiguredHTTPClient(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkHTTPClient = GetHTTPClient(30 * time.Second)
	}
}

func BenchmarkDNSCacheHit(b *testing.B) {
	cache := newDNSAddressCache(256, time.Minute, time.Now)
	loader := func(context.Context) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("192.0.2.1"),
			netip.MustParseAddr("2001:db8::1"),
		}, nil
	}
	if _, err := cache.Lookup(context.Background(), "benchmark.example", loader); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var err error
		benchmarkDNSAddresses, err = cache.Lookup(context.Background(), "benchmark.example", loader)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSystemDNSLookupLocalhost(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		var err error
		_, err = net.DefaultResolver.LookupHost(context.Background(), "localhost")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseAndInterleaveDNSAddresses(b *testing.B) {
	hosts := []string{
		"192.0.2.1", "2001:db8::1", "192.0.2.2", "2001:db8::2",
		"192.0.2.3", "2001:db8::3", "192.0.2.4", "2001:db8::4",
	}
	b.ReportAllocs()
	for range b.N {
		benchmarkDNSAddresses = interleaveAddressFamilies(parseResolvedAddresses(hosts), "tcp", true)
	}
}
