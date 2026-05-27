package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type pingResultCapture struct {
	payload map[string]interface{}
	writes  int
}

func (capture *pingResultCapture) WriteJSON(v interface{}) error {
	capture.writes++
	payload, _ := v.(map[string]interface{})
	capture.payload = payload
	return nil
}

func TestNewPingTaskReturnsDisabledResultWhenPingCapabilityIsOff(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.DisableWebSsh = true
	flags.EnableRemoteControl = false
	flags.EnablePing = false

	capture := &pingResultCapture{}
	NewPingTask(capture, 7, "tcp", "127.0.0.1:80")

	if capture.writes != 1 {
		t.Fatalf("expected one ping result write, got %d", capture.writes)
	}
	if got := capture.payload["type"]; got != "ping_result" {
		t.Fatalf("payload type = %v, want %q", got, "ping_result")
	}
	if got := capture.payload["task_id"]; got != uint(7) {
		t.Fatalf("payload task_id = %v, want %d", got, 7)
	}
	if got := capture.payload["ping_type"]; got != "tcp" {
		t.Fatalf("payload ping_type = %v, want %q", got, "tcp")
	}
	if got := capture.payload["value"]; got != -1 {
		t.Fatalf("payload value = %v, want %d", got, -1)
	}
}

func TestPingTargetAllowedRejectsPrivateTargetsByDefault(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.AllowedPingTypes = "tcp,http"
	flags.AllowedPingTCPPorts = "80,443"
	flags.AllowPrivatePingTargets = false

	if err := pingTargetAllowed("tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("expected private loopback target to be rejected by default")
	}
}

func TestPingTargetAllowedAllowsPrivateTargetsWhenExplicitlyEnabled(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.AllowedPingTypes = "tcp,http"
	flags.AllowedPingTCPPorts = "80,443"
	flags.AllowPrivatePingTargets = true

	if err := pingTargetAllowed("tcp", "127.0.0.1:80"); err != nil {
		t.Fatalf("expected explicit private target opt-in to allow loopback ping, got %v", err)
	}
}

func TestPingTargetAllowedRejectsDisallowedTypeAndPort(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.AllowPrivatePingTargets = true
	flags.AllowedPingTypes = "tcp,http"
	flags.AllowedPingTCPPorts = "80,443"

	if err := pingTargetAllowed("icmp", "8.8.8.8"); err == nil {
		t.Fatal("expected icmp ping to be rejected when not in the allowlist")
	}
	if err := pingTargetAllowed("tcp", "8.8.8.8:22"); err == nil {
		t.Fatal("expected tcp ping to a disallowed port to be rejected")
	}
}

func TestNewPingTaskRejectsWhenRateLimitReached(t *testing.T) {
	useServerFlagsSnapshot(t)
	resetPingLimiters(t)

	flags.EnablePing = true
	flags.AllowPrivatePingTargets = true
	flags.AllowedPingTypes = "tcp"
	flags.AllowedPingTCPPorts = "80"
	flags.MaxConcurrentPings = 2
	flags.PingMinIntervalMillis = 500

	capture := &pingResultCapture{}
	lastAcceptedPingAt = time.Now()
	NewPingTask(capture, 8, "tcp", "127.0.0.1:80")

	if capture.writes != 1 {
		t.Fatalf("expected one ping result write, got %d", capture.writes)
	}
	if got := capture.payload["value"]; got != -1 {
		t.Fatalf("payload value = %v, want %d", got, -1)
	}
}

func TestNewPingTaskRejectsWhenConcurrencyLimitReached(t *testing.T) {
	useServerFlagsSnapshot(t)
	resetPingLimiters(t)

	flags.EnablePing = true
	flags.AllowPrivatePingTargets = true
	flags.AllowedPingTypes = "tcp"
	flags.AllowedPingTCPPorts = "80"
	flags.MaxConcurrentPings = 1
	flags.PingMinIntervalMillis = 0

	release, ok := tryAcquirePingExecutionSlot()
	if !ok {
		t.Fatal("expected first ping slot acquisition to succeed")
	}
	defer release()

	capture := &pingResultCapture{}
	NewPingTask(capture, 9, "tcp", "127.0.0.1:80")

	if capture.writes != 1 {
		t.Fatalf("expected one ping result write, got %d", capture.writes)
	}
	if got := capture.payload["value"]; got != -1 {
		t.Fatalf("payload value = %v, want %d", got, -1)
	}
}

func TestTCPPingUsesLocalListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	latency, err := tcpPing(listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("tcpPing() error = %v", err)
	}
	if latency < 0 {
		t.Fatalf("tcpPing() latency = %d, want >= 0", latency)
	}
	<-acceptDone
}

func TestHTTPPingUsesLocalServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	latency, err := httpPing(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("httpPing() error = %v", err)
	}
	if latency < 0 {
		t.Fatalf("httpPing() latency = %d, want >= 0", latency)
	}
}

func useServerFlagsSnapshot(t *testing.T) {
	t.Helper()

	previous := *flags
	t.Cleanup(func() {
		*flags = previous
	})
}

func resetPingLimiters(t *testing.T) {
	t.Helper()

	pingExecutionSlotsMu.Lock()
	originalSlots := pingExecutionSlots
	originalLimit := pingExecutionSlotsLimit
	pingExecutionSlots = nil
	pingExecutionSlotsLimit = 0
	pingExecutionSlotsMu.Unlock()

	pingRateLimitMu.Lock()
	originalLastAccepted := lastAcceptedPingAt
	lastAcceptedPingAt = time.Time{}
	pingRateLimitMu.Unlock()

	t.Cleanup(func() {
		pingExecutionSlotsMu.Lock()
		pingExecutionSlots = originalSlots
		pingExecutionSlotsLimit = originalLimit
		pingExecutionSlotsMu.Unlock()

		pingRateLimitMu.Lock()
		lastAcceptedPingAt = originalLastAccepted
		pingRateLimitMu.Unlock()
	})
}