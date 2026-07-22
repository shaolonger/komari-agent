package dnsresolver

import (
	"net/http"
	"testing"
	"time"
)

var benchmarkHTTPClient *http.Client

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
