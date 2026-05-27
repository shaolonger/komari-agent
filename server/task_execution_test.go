package server

import (
	"bytes"
	"fmt"
	"log"
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
	setRemoteControlEnabled(t, true)

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
	setRemoteControlEnabled(t, true)

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

func TestNewTaskTruncatesLargeOutput(t *testing.T) {
	result := captureTaskResult(t)
	setTaskExecutionTimeout(t, 2*time.Second)
	setTaskOutputLimit(t, 32)
	setRemoteControlEnabled(t, true)

	NewTask("task-large-output", largeOutputTaskCommand(64))

	if result.exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with result %q", result.exitCode, result.result)
	}
	if !strings.Contains(result.result, "Task output truncated after 32 bytes per stream.") {
		t.Fatalf("expected truncation marker in result, got %q", result.result)
	}
	if strings.Count(result.result, "A") < 32 {
		t.Fatalf("expected captured output to retain the bounded prefix, got %q", result.result)
	}
	if len(result.result) >= 128 {
		t.Fatalf("expected truncated result to stay bounded, got length %d", len(result.result))
	}
	if result.finishedAt.IsZero() {
		t.Fatal("expected finishedAt to be recorded")
	}
}

func TestNewTaskLogsDoNotExposeCommandText(t *testing.T) {
	result := captureTaskResult(t)
	setTaskExecutionTimeout(t, 2*time.Second)
	setRemoteControlEnabled(t, true)

	logBuffer, restoreLogger := captureTaskLogs(t)
	defer restoreLogger()

	command := successTaskCommand()
	NewTask("task-logging", command)

	if result.exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with result %q", result.exitCode, result.result)
	}
	logOutput := logBuffer.String()
	if strings.Contains(logOutput, command) {
		t.Fatalf("expected logs to omit command text, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Task started task_id=task-logging") {
		t.Fatalf("expected start log entry, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Task finished task_id=task-logging") {
		t.Fatalf("expected finish log entry, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "output_bytes=") {
		t.Fatalf("expected finish log to include output_bytes, got %q", logOutput)
	}
}

func TestNewTaskAuditLoggingRedactsSensitiveCommandText(t *testing.T) {
	result := captureTaskResult(t)
	setTaskExecutionTimeout(t, 2*time.Second)
	setRemoteControlEnabled(t, true)
	setTaskCommandAudit(t, true)

	logBuffer, restoreLogger := captureTaskLogs(t)
	defer restoreLogger()

	command := auditedTaskCommand()
	NewTask("task-audit", command)

	if result.exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d with result %q", result.exitCode, result.result)
	}
	logOutput := logBuffer.String()
	if strings.Contains(logOutput, "token=secret") || strings.Contains(logOutput, "Bearer abc123") || strings.Contains(logOutput, "--token supersecret") {
		t.Fatalf("expected audit log to redact sensitive command content, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Task audit task_id=task-audit") {
		t.Fatalf("expected audit log entry, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "token=[REDACTED]") {
		t.Fatalf("expected token query redaction, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Authorization: Bearer [REDACTED]") {
		t.Fatalf("expected authorization header redaction, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "--token [REDACTED]") {
		t.Fatalf("expected CLI token redaction, got %q", logOutput)
	}
}

func TestNewTaskQueuesWhenConcurrencyLimitReached(t *testing.T) {
	results := captureTaskResults(t, 2)
	setTaskExecutionTimeout(t, 2*time.Second)
	setTaskConcurrencyLimit(t, 1)
	setRemoteControlEnabled(t, true)

	logBuffer, restoreLogger := captureTaskLogs(t)
	defer restoreLogger()

	go NewTask("task-queue-first", delayedOutputTaskCommand(300*time.Millisecond, "first"))
	waitForLogSubstring(t, logBuffer, "Task started task_id=task-queue-first")
	go NewTask("task-queue-second", delayedOutputTaskCommand(0, "second"))

	first := waitForTaskResult(t, results)
	second := waitForTaskResult(t, results)

	if first.taskID != "task-queue-first" {
		t.Fatalf("expected first queued result to belong to task-queue-first, got %+v", first)
	}
	if second.taskID != "task-queue-second" {
		t.Fatalf("expected second queued result to belong to task-queue-second, got %+v", second)
	}
	if !strings.Contains(first.result, "first") {
		t.Fatalf("expected first task output, got %q", first.result)
	}
	if !strings.Contains(second.result, "second") {
		t.Fatalf("expected second task output, got %q", second.result)
	}
	if !strings.Contains(logBuffer.String(), "Task started task_id=task-queue-second") {
		t.Fatalf("expected second task to start after the first acquired the slot, got logs %q", logBuffer.String())
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

func setRemoteControlEnabled(t *testing.T, enabled bool) {
	t.Helper()

	originalDisable := flags.DisableWebSsh
	originalEnable := flags.EnableRemoteControl
	flags.DisableWebSsh = !enabled
	flags.EnableRemoteControl = enabled
	t.Cleanup(func() {
		flags.DisableWebSsh = originalDisable
		flags.EnableRemoteControl = originalEnable
	})
}

func setTaskOutputLimit(t *testing.T, limit int) {
	t.Helper()

	originalLimit := taskOutputLimit
	taskOutputLimit = limit
	t.Cleanup(func() {
		taskOutputLimit = originalLimit
	})
}

func setTaskCommandAudit(t *testing.T, enabled bool) {
	t.Helper()

	originalAudit := flags.AuditTaskCommands
	flags.AuditTaskCommands = enabled
	t.Cleanup(func() {
		flags.AuditTaskCommands = originalAudit
	})
}

func setTaskConcurrencyLimit(t *testing.T, limit int) {
	t.Helper()

	taskExecutionSlotsMu.Lock()
	originalLimit := taskConcurrencyLimit
	originalSlots := taskExecutionSlots
	originalSlotsLimit := taskExecutionSlotsLimit
	taskConcurrencyLimit = limit
	taskExecutionSlots = nil
	taskExecutionSlotsLimit = 0
	taskExecutionSlotsMu.Unlock()

	t.Cleanup(func() {
		taskExecutionSlotsMu.Lock()
		taskConcurrencyLimit = originalLimit
		taskExecutionSlots = originalSlots
		taskExecutionSlotsLimit = originalSlotsLimit
		taskExecutionSlotsMu.Unlock()
	})
}

func captureTaskLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()

	logBuffer := &bytes.Buffer{}
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(logBuffer)
	log.SetFlags(0)

	return logBuffer, func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	}
}

func captureTaskResults(t *testing.T, bufferSize int) chan capturedTaskResult {
	t.Helper()

	results := make(chan capturedTaskResult, bufferSize)
	originalUploader := taskResultUploader
	taskResultUploader = func(taskID, result string, exitCode int, finishedAt time.Time) {
		results <- capturedTaskResult{
			taskID:     taskID,
			result:     result,
			exitCode:   exitCode,
			finishedAt: finishedAt,
		}
	}
	t.Cleanup(func() {
		taskResultUploader = originalUploader
	})

	return results
}

func waitForTaskResult(t *testing.T, results <-chan capturedTaskResult) capturedTaskResult {
	t.Helper()

	select {
	case result := <-results:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task result")
		return capturedTaskResult{}
	}
}

func waitForLogSubstring(t *testing.T, logBuffer *bytes.Buffer, needle string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuffer.String(), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for log substring %q in %q", needle, logBuffer.String())
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

func largeOutputTaskCommand(size int) string {
	payload := strings.Repeat("A", size)
	if runtime.GOOS == "windows" {
		return "[Console]::Out.Write('" + payload + "')"
	}
	return "printf '" + payload + "'"
}

func delayedOutputTaskCommand(delay time.Duration, output string) string {
	if runtime.GOOS == "windows" {
		if delay <= 0 {
			return "[Console]::Out.Write('" + output + "')"
		}
		return fmt.Sprintf("Start-Sleep -Milliseconds %d; [Console]::Out.Write('%s')", delay/time.Millisecond, output)
	}
	if delay <= 0 {
		return fmt.Sprintf("printf '%s'", output)
	}
	return fmt.Sprintf("sleep %.3f; printf '%s'", delay.Seconds(), output)
}

func auditedTaskCommand() string {
	if runtime.GOOS == "windows" {
		return "Write-Output 'audit-ok'; # token=secret Authorization: Bearer abc123 --token supersecret"
	}
	return "printf 'audit-ok' # token=secret Authorization: Bearer abc123 --token supersecret"
}