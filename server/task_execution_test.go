package server

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

type capturedTaskResult struct {
	taskID     string
	result     string
	exitCode   int
	finishedAt time.Time
}

func TestNewTaskReturnsCommandOutput(t *testing.T) {
	result := captureTaskResult(t)
	setTaskExecutionTimeout(t, 2*time.Second)

	flags.DisableWebSsh = false
	t.Cleanup(func() {
		flags.DisableWebSsh = false
	})

	NewTask("task-output", successTaskCommand())

	if result.taskID != "task-output" {
		t.Fatalf("unexpected task id: %q", result.taskID)
	}
	if result.exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with result %q", result.exitCode, result.result)
	}
	if !strings.Contains(result.result, "task-ok") {
		t.Fatalf("expected command output in result, got %q", result.result)
	}
	if result.finishedAt.IsZero() {
		t.Fatal("expected finishedAt to be recorded")
	}
}

func TestNewTaskTimesOutLongRunningCommand(t *testing.T) {
	result := captureTaskResult(t)
	setTaskExecutionTimeout(t, 150*time.Millisecond)

	flags.DisableWebSsh = false
	t.Cleanup(func() {
		flags.DisableWebSsh = false
	})

	startedAt := time.Now()
	NewTask("task-timeout", slowTaskCommand())
	elapsed := time.Since(startedAt)

	if elapsed >= time.Second {
		t.Fatalf("expected task timeout to stop execution quickly, elapsed=%s", elapsed)
	}
	if result.taskID != "task-timeout" {
		t.Fatalf("unexpected task id: %q", result.taskID)
	}
	if result.exitCode == 0 {
		t.Fatalf("expected non-zero exit code on timeout, got %d with result %q", result.exitCode, result.result)
	}
	if !strings.Contains(result.result, "Task execution timed out.") {
		t.Fatalf("expected timeout marker in result, got %q", result.result)
	}
	if result.finishedAt.IsZero() {
		t.Fatal("expected finishedAt to be recorded")
	}
}

func captureTaskResult(t *testing.T) *capturedTaskResult {
	t.Helper()

	captured := &capturedTaskResult{}
	originalUploader := taskResultUploader
	taskResultUploader = func(taskID, result string, exitCode int, finishedAt time.Time) {
		captured.taskID = taskID
		captured.result = result
		captured.exitCode = exitCode
		captured.finishedAt = finishedAt
	}
	t.Cleanup(func() {
		taskResultUploader = originalUploader
	})

	return captured
}

func setTaskExecutionTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	originalTimeout := taskExecutionTimeout
	taskExecutionTimeout = timeout
	t.Cleanup(func() {
		taskExecutionTimeout = originalTimeout
	})
}

func successTaskCommand() string {
	if runtime.GOOS == "windows" {
		return "Write-Output 'task-ok'"
	}
	return "printf 'task-ok'"
}

func slowTaskCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 2"
	}
	return "sleep 2"
}