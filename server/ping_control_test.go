package server

import "testing"

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

func useServerFlagsSnapshot(t *testing.T) {
	t.Helper()

	previous := *flags
	t.Cleanup(func() {
		*flags = previous
	})
}