package server

import (
	"testing"
	"time"
)

func resetControlRequestLimiter(t *testing.T) {
	t.Helper()

	controlRequestLimiterMu.Lock()
	originalTimes := append([]time.Time(nil), controlRequestTimes...)
	controlRequestTimes = nil
	controlRequestLimiterMu.Unlock()
	t.Cleanup(func() {
		controlRequestLimiterMu.Lock()
		controlRequestTimes = originalTimes
		controlRequestLimiterMu.Unlock()
	})
}

func TestAllowControlRequestRejectsBurstOverLimit(t *testing.T) {
	useServerFlagsSnapshot(t)
	resetControlRequestLimiter(t)

	flags.MaxControlRequests = 2
	flags.ControlRequestWindow = 10
	now := time.Unix(1000, 0)

	if !allowControlRequest(now) {
		t.Fatal("expected first control request to be allowed")
	}
	if !allowControlRequest(now.Add(2 * time.Second)) {
		t.Fatal("expected second control request inside window to be allowed")
	}
	if allowControlRequest(now.Add(3 * time.Second)) {
		t.Fatal("expected third control request inside window to be rejected")
	}
	if !allowControlRequest(now.Add(11 * time.Second)) {
		t.Fatal("expected control request to be allowed after the window expires")
	}
}

func TestShouldRateLimitControlRequestSkipsPingMessages(t *testing.T) {
	message := controlPlaneMessage{
		Message:    "ping",
		PingTaskID: 7,
		PingType:   "tcp",
		PingTarget: "127.0.0.1:80",
	}

	if shouldRateLimitControlRequest(message) {
		t.Fatal("expected ping messages to bypass the general control-request limiter")
	}
}

func TestShouldRateLimitControlRequestStillCoversExecAndTerminal(t *testing.T) {
	if !shouldRateLimitControlRequest(controlPlaneMessage{Message: "exec", ExecTaskID: "task-1"}) {
		t.Fatal("expected exec messages to stay behind the control-request limiter")
	}
	if !shouldRateLimitControlRequest(controlPlaneMessage{Message: "terminal", TerminalId: "term-1"}) {
		t.Fatal("expected terminal messages to stay behind the control-request limiter")
	}
}