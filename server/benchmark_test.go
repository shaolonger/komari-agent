package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var (
	benchmarkRequest        *http.Request
	benchmarkHeaders        http.Header
	benchmarkFrame          outboundFrame
	benchmarkWriter         outboundBenchmarkWriter = discardOutboundBenchmarkWriter{}
	benchmarkStatic         staticBasicInfo
	benchmarkPolicy         *compiledPingPolicy
	benchmarkTarget         *resolvedPingTarget
	benchmarkPingHTTPClient *http.Client
)

type outboundBenchmarkWriter interface {
	WriteMessageWithDeadline(time.Time, int, []byte) error
}

type discardOutboundBenchmarkWriter struct{}

func (discardOutboundBenchmarkWriter) WriteMessageWithDeadline(time.Time, int, []byte) error {
	return nil
}

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

func BenchmarkStaticBasicInfoCacheHit(b *testing.B) {
	cache := newStaticBasicInfoCache(func() staticBasicInfo {
		return staticBasicInfo{CPUName: "benchmark CPU", GPUName: "benchmark GPU"}
	})
	benchmarkStatic = cache.Get()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkStatic = cache.Get()
	}
}

func BenchmarkPingPolicyCompile(b *testing.B) {
	key := pingPolicyKey{types: "tcp,http,icmp", ports: "80,443,8443"}
	b.ReportAllocs()
	for range b.N {
		benchmarkPolicy = compilePingPolicy(key)
	}
}

func BenchmarkPingPolicyCacheHit(b *testing.B) {
	original := *flags
	flags.AllowedPingTypes = "tcp,http,icmp"
	flags.AllowedPingTCPPorts = "80,443,8443"
	flags.AllowPrivatePingTargets = false
	b.Cleanup(func() { *flags = original })
	benchmarkPolicy = currentPingPolicy()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkPolicy = currentPingPolicy()
	}
}

func BenchmarkPrepareLiteralPingTarget(b *testing.B) {
	policy := testPingPolicy("tcp", "443", false)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkTarget, _ = preparePingTarget(context.Background(), policy, "tcp", "8.8.8.8:443", nil)
	}
}

func BenchmarkBuildPinnedPingHTTPClient(b *testing.B) {
	policy := testPingPolicy("http", "443", false)
	target, err := preparePingTarget(context.Background(), policy, "http", "https://8.8.8.8/health", nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkPingHTTPClient = buildPinnedHTTPClient(target, pingProbeTimeout)
	}
}

func BenchmarkPingHTTPClientCacheHit(b *testing.B) {
	policy := testPingPolicy("http", "443", false)
	target, err := preparePingTarget(context.Background(), policy, "http", "https://8.8.8.8/health", nil)
	if err != nil {
		b.Fatal(err)
	}
	cache := newPingHTTPClientCache(64, time.Hour, time.Now)
	b.Cleanup(cache.Clear)
	benchmarkPingHTTPClient = cache.Get(target)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkPingHTTPClient = cache.Get(target)
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

func BenchmarkOutboundQueueTelemetryCoalesce(b *testing.B) {
	queue := newOutboundQueue(context.Background(), 128, 3)
	payload := []byte(`{"cpu":12.5,"network":{"up":1024,"down":2048}}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		if err := queue.EnqueueTelemetry(websocket.TextMessage, payload); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkOutboundDirectDispatch preserves the pre-A-202 dispatch cost. It
// intentionally excludes network I/O and demonstrates the bounded queue's
// safety cost separately from socket latency.
func BenchmarkOutboundDirectDispatch(b *testing.B) {
	payload := []byte(`{"type":"ping_result","task_id":42,"value":12}`)
	deadline := time.Now().Add(time.Second)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		if err := benchmarkWriter.WriteMessageWithDeadline(deadline, websocket.TextMessage, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOutboundQueueReliableRoundTrip(b *testing.B) {
	queue := newOutboundQueue(context.Background(), 128, 3)
	payload := []byte(`{"type":"ping_result","task_id":42,"value":12}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for range b.N {
		if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, payload); err != nil {
			b.Fatal(err)
		}
		var err error
		benchmarkFrame, err = queue.Take(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		queue.Ack(benchmarkFrame)
	}
}
