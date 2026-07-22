package server

import (
	"net/http"
	"testing"
)

var (
	benchmarkRequest *http.Request
	benchmarkHeaders http.Header
)

func BenchmarkNewJSONClientRequest(b *testing.B) {
	originalEndpoint := flags.Endpoint
	originalToken := flags.Token
	flags.Endpoint = "https://monitor.example.test"
	flags.Token = "benchmark-token-value"
	b.Cleanup(func() {
		flags.Endpoint = originalEndpoint
		flags.Token = originalToken
	})

	payload := []byte(`{"cpu":{"usage":12.5},"network":{"up":1024,"down":2048}}`)
	endpoint := buildClientAPIEndpoint("/api/clients/report", nil)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		benchmarkRequest, _ = newJSONClientRequest(http.MethodPost, endpoint, payload)
	}
}

func BenchmarkNewWSHeaders(b *testing.B) {
	originalToken := flags.Token
	originalID := flags.CFAccessClientID
	originalSecret := flags.CFAccessClientSecret
	flags.Token = "benchmark-token-value"
	flags.CFAccessClientID = "benchmark-client-id"
	flags.CFAccessClientSecret = "benchmark-client-secret"
	b.Cleanup(func() {
		flags.Token = originalToken
		flags.CFAccessClientID = originalID
		flags.CFAccessClientSecret = originalSecret
	})

	b.ReportAllocs()
	for range b.N {
		benchmarkHeaders = newWSHeaders()
	}
}
