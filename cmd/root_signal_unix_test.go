//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestSIGTERMContextTriggersGracefulLifecycle(t *testing.T) {
	signalContext, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	telemetryStarted := make(chan struct{})
	stopped := make(chan struct{})
	services := agentServices{
		runTelemetry: func(ctx context.Context) error {
			close(telemetryStarted)
			<-ctx.Done()
			return ctx.Err()
		},
		updateBasicInfo: func(context.Context) error { return nil },
		waitControl:     func(context.Context) error { return nil },
		stopNetstatic: func(context.Context) error {
			close(stopped)
			return nil
		},
	}
	result := make(chan error, 1)
	go func() { result <- runAgentLifecycle(signalContext, true, services) }()
	<-telemetryStarted
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("lifecycle after SIGTERM: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SIGTERM did not stop lifecycle")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("SIGTERM lifecycle skipped final netstatic stop")
	}
}
