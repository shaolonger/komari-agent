package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBasicInfoAuthenticationRejectionIsNotRetried(t *testing.T) {
	useServerFlagsSnapshot(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(writer, "token rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	flags.Endpoint = server.URL
	flags.Token = "invalid-fixture-token"

	err := uploadBasicInfoContext(t.Context())
	if !isAuthenticationRejection(err) {
		t.Fatalf("upload error = %v, want authentication rejection", err)
	}
	if strings.Contains(err.Error(), "token rejected") || strings.Contains(err.Error(), flags.Token) {
		t.Fatalf("authentication response leaked sensitive detail: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("authentication request count = %d, want 1", requests.Load())
	}
}

func TestBasicInfoNonAuthenticationFailureKeepsLegacyCompatibilityRetry(t *testing.T) {
	useServerFlagsSnapshot(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(writer, "legacy fixture", http.StatusBadRequest)
	}))
	defer server.Close()
	flags.Endpoint = server.URL

	if err := uploadBasicInfoContext(t.Context()); err == nil {
		t.Fatal("legacy compatibility fixture unexpectedly succeeded")
	}
	if requests.Load() != 2 {
		t.Fatalf("compatibility request count = %d, want 2", requests.Load())
	}
}

func TestBuildCapabilityPayloadReflectsFlags(t *testing.T) {
	original := *flags
	t.Cleanup(func() {
		*flags = original
	})

	flags.IgnoreUnsafeCert = false
	flags.DisableAutoUpdate = false
	flags.DisableWebSsh = true
	flags.EnableRemoteControl = false
	flags.EnableRemoteExec = true
	flags.EnableTerminal = true
	flags.EnablePing = true
	flags.EnableGPU = true
	flags.AllowPrivatePingTargets = true

	payload := buildCapabilityPayload()

	expected := map[string]bool{
		"capability_ping":                 true,
		"capability_terminal":             true,
		"capability_remote_exec":          true,
		"capability_remote_control":       false,
		"capability_gpu":                  true,
		"capability_auto_update":          true,
		"capability_private_ping_targets": true,
	}

	for key, want := range expected {
		got, ok := payload[key].(bool)
		if !ok {
			t.Fatalf("%s is not a boolean", key)
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", key, got, want)
		}
	}
}

func TestBuildCapabilityPayloadRespectsUnsafeCertRestrictions(t *testing.T) {
	original := *flags
	t.Cleanup(func() {
		*flags = original
	})

	flags.IgnoreUnsafeCert = true
	flags.DisableAutoUpdate = false
	flags.DisableWebSsh = false
	flags.EnableRemoteControl = true
	flags.EnableRemoteExec = true
	flags.EnableTerminal = true
	flags.EnablePing = true

	payload := buildCapabilityPayload()

	for _, key := range []string{
		"capability_ping",
		"capability_terminal",
		"capability_remote_exec",
		"capability_remote_control",
		"capability_auto_update",
	} {
		got, ok := payload[key].(bool)
		if !ok {
			t.Fatalf("%s is not a boolean", key)
		}
		if got {
			t.Fatalf("%s = true, want false when ignore_unsafe_cert is enabled", key)
		}
	}
}

func TestBasicInfoWorkerValidatesIntervalAndStopsWithContext(t *testing.T) {
	original := flags.InfoReportInterval
	t.Cleanup(func() { flags.InfoReportInterval = original })
	flags.InfoReportInterval = 0
	if err := DoUploadBasicInfoWorksContext(context.Background()); err == nil {
		t.Fatal("worker accepted a zero interval")
	}
	flags.InfoReportInterval = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := DoUploadBasicInfoWorksContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("worker error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("worker cancellation took %s", elapsed)
	}
}
