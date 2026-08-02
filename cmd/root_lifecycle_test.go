package cmd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/komari-monitor/komari-agent/update"
)

func TestRunAgentLifecycleCancelsAndJoinsAllServices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	telemetryStarted := make(chan struct{})
	var backgroundExited atomic.Int32
	var controlWaited atomic.Bool
	var netstaticStopped atomic.Bool
	worker := func(ctx context.Context) error {
		<-ctx.Done()
		backgroundExited.Add(1)
		return ctx.Err()
	}
	services := agentServices{
		runTelemetry: func(ctx context.Context) error {
			close(telemetryStarted)
			<-ctx.Done()
			return ctx.Err()
		},
		updateBasicInfo: func(context.Context) error { return nil },
		runBasicInfo:    worker,
		runUpdater:      worker,
		runDiagnostics:  worker,
		waitControl: func(context.Context) error {
			controlWaited.Store(true)
			return nil
		},
		stopNetstatic: func(context.Context) error {
			netstaticStopped.Store(true)
			return nil
		},
		reconnectInterval: time.Millisecond,
	}
	result := make(chan error, 1)
	go func() { result <- runAgentLifecycle(ctx, true, services) }()
	<-telemetryStarted
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runAgentLifecycle() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not stop after cancellation")
	}
	if backgroundExited.Load() != 3 || !controlWaited.Load() || !netstaticStopped.Load() {
		t.Fatalf("incomplete shutdown: background=%d control=%v netstatic=%v", backgroundExited.Load(), controlWaited.Load(), netstaticStopped.Load())
	}
}

func TestRunAgentLifecyclePropagatesFinalSaveFailure(t *testing.T) {
	saveFailure := errors.New("disk is read-only")
	ctx, cancel := context.WithCancel(context.Background())
	services := agentServices{
		runTelemetry: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		updateBasicInfo: func(context.Context) error { cancel(); return nil },
		waitControl:     func(context.Context) error { return nil },
		stopNetstatic:   func(context.Context) error { return saveFailure },
	}
	err := runAgentLifecycle(ctx, true, services)
	if !errors.Is(err, saveFailure) {
		t.Fatalf("runAgentLifecycle() error = %v, want save failure", err)
	}
}

func TestShutdownBudgetBoundsIgnoringWorker(t *testing.T) {
	originalBudget := agentShutdownBudget
	agentShutdownBudget = 20 * time.Millisecond
	t.Cleanup(func() { agentShutdownBudget = originalBudget })
	release := make(chan struct{})
	services := agentServices{
		waitControl: func(context.Context) error {
			<-release
			return nil
		},
	}
	started := time.Now()
	err := finishAgentShutdown(false, nil, services)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finishAgentShutdown() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("shutdown exceeded bound: %s", elapsed)
	}
	close(release)
}

func TestLoadFromEnvRejectsMalformedValuesWithoutLeakingValue(t *testing.T) {
	useGlobalFlagsSnapshot(t)
	secretLikeValue := "not-a-number-secret"
	t.Setenv("AGENT_MAX_RETRIES", secretLikeValue)
	err := loadFromEnv()
	if err == nil || !strings.Contains(err.Error(), "AGENT_MAX_RETRIES") {
		t.Fatalf("loadFromEnv() error = %v", err)
	}
	if strings.Contains(err.Error(), secretLikeValue) {
		t.Fatalf("environment error leaked value: %v", err)
	}
}

func TestCommandExitCode(t *testing.T) {
	if code := commandExitCode(nil); code != 0 {
		t.Fatalf("success exit code = %d, want 0", code)
	}
	if code := commandExitCode(errors.New("invalid config")); code != 1 {
		t.Fatalf("failure exit code = %d, want 1", code)
	}
	if code := commandExitCode(update.ErrUpdateInstalled); code != update.RestartExitCode {
		t.Fatalf("update restart exit code = %d, want %d", code, update.RestartExitCode)
	}
}

func TestFinishAgentShutdownJoinsBackgroundBeforeReturning(t *testing.T) {
	var group sync.WaitGroup
	group.Add(1)
	release := make(chan struct{})
	go func() {
		defer group.Done()
		<-release
	}()
	services := agentServices{waitControl: func(context.Context) error { return nil }}
	result := make(chan error, 1)
	go func() { result <- finishAgentShutdown(false, &group, services) }()
	select {
	case <-result:
		t.Fatal("shutdown returned before background worker exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("finishAgentShutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after background worker exited")
	}
}
