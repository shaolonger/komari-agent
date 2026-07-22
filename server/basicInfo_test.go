package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
