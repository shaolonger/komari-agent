package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
)

const defaultTaskExecutionTimeout = 5 * time.Minute
const defaultTaskOutputLimit = 128 * 1024
const defaultTaskConcurrencyLimit = 1

var taskExecutionTimeout = defaultTaskExecutionTimeout
var taskOutputLimit = defaultTaskOutputLimit
var taskConcurrencyLimit = defaultTaskConcurrencyLimit
var taskResultUploader = uploadTaskResult

var taskCommandAuditPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s'";]+)`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(--(?:token|password|passwd|secret|api[-_]?key|cf-access-client-secret)(?:=|\s+))([^\s'";]+)`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)([?&](?:token|password|passwd|secret|api[-_]?key)=)([^&\s'";]+)`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\b((?:token|password|passwd|secret|api[-_]?key)\s*=\s*)([^\s'";]+)`),
		replacement: `${1}[REDACTED]`,
	},
}

var taskExecutionSlotsMu sync.Mutex
var taskExecutionSlots chan struct{}
var taskExecutionSlotsLimit int

func NewTask(task_id, command string) {
	if task_id == "" {
		return
	}
	if command == "" {
		taskResultUploader(task_id, "No command provided", 0, time.Now())
		return
	}
	if !flags.RemoteExecEnabled() {
		taskResultUploader(task_id, "Remote task execution is disabled.", -1, time.Now())
		return
	}
	releaseTaskSlot := acquireTaskExecutionSlot()
	defer releaseTaskSlot()

	startedAt := time.Now()
	if flags.AuditTaskCommands {
		log.Printf("Task audit task_id=%s command=%s", task_id, redactTaskCommand(command))
	}
	log.Printf("Task started task_id=%s started_at=%s", task_id, startedAt.UTC().Format(time.RFC3339))
	result, exitCode, outputBytes, finishedAt := executeTaskCommand(command)
	log.Printf("Task finished task_id=%s finished_at=%s exit_code=%d output_bytes=%d", task_id, finishedAt.UTC().Format(time.RFC3339), exitCode, outputBytes)
	taskResultUploader(task_id, result, exitCode, finishedAt)
}

func redactTaskCommand(command string) string {
	redacted := command
	for _, auditPattern := range taskCommandAuditPatterns {
		redacted = auditPattern.pattern.ReplaceAllString(redacted, auditPattern.replacement)
	}
	return redacted
}

func acquireTaskExecutionSlot() func() {
	limit := taskConcurrencyLimit
	if limit < 1 {
		limit = 1
	}

	taskExecutionSlotsMu.Lock()
	if taskExecutionSlots == nil || taskExecutionSlotsLimit != limit {
		taskExecutionSlots = make(chan struct{}, limit)
		taskExecutionSlotsLimit = limit
	}
	slots := taskExecutionSlots
	taskExecutionSlotsMu.Unlock()

	slots <- struct{}{}
	return func() {
		<-slots
	}
}

func executeTaskCommand(command string) (string, int, int, time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), taskExecutionTimeout)
	defer cancel()

	cmd := newTaskCommand(ctx, command)
	stdout := newTaskOutputBuffer(taskOutputLimit)
	stderr := newTaskOutputBuffer(taskOutputLimit)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	finishedAt := time.Now()
	outputBytes := stdout.TotalBytes() + stderr.TotalBytes()

	result := collectTaskOutput(stdout, stderr)
	result = strings.ReplaceAll(result, "\r\n", "\n")

	exitCode := 0
	if err == nil {
		return result, exitCode, outputBytes, finishedAt
	}

	var exitError *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		exitCode = -1
		result = appendTaskOutput(result, "Task execution timed out.")
	case errors.Is(err, context.DeadlineExceeded):
		exitCode = -1
		result = appendTaskOutput(result, "Task execution timed out.")
	case errors.As(err, &exitError):
		exitCode = exitError.ExitCode()
	default:
		exitCode = -1
		result = appendTaskOutput(result, err.Error())
	}

	return result, exitCode, outputBytes, finishedAt
}

func collectTaskOutput(stdout, stderr taskOutputBuffer) string {
	result := stdout.String()
	if stderr.String() != "" {
		result = appendTaskOutput(result, stderr.String())
	}
	if stdout.Truncated() || stderr.Truncated() {
		result = appendTaskOutput(result, fmt.Sprintf("Task output truncated after %d bytes per stream.", taskOutputLimit))
	}
	return result
}

type taskOutputBuffer struct {
	buffer     bytes.Buffer
	limit      int
	totalBytes int
	truncated  bool
}

func newTaskOutputBuffer(limit int) taskOutputBuffer {
	if limit < 0 {
		limit = 0
	}
	return taskOutputBuffer{limit: limit}
}

func (buffer *taskOutputBuffer) Write(data []byte) (int, error) {
	buffer.totalBytes += len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		if len(data) > 0 {
			buffer.truncated = true
		}
		return len(data), nil
	}

	if len(data) > remaining {
		_, _ = buffer.buffer.Write(data[:remaining])
		buffer.truncated = true
		return len(data), nil
	}

	_, _ = buffer.buffer.Write(data)
	return len(data), nil
}

func (buffer *taskOutputBuffer) String() string {
	return buffer.buffer.String()
}

func (buffer *taskOutputBuffer) TotalBytes() int {
	return buffer.totalBytes
}

func (buffer *taskOutputBuffer) Truncated() bool {
	return buffer.truncated
}

func appendTaskOutput(result, addition string) string {
	if addition == "" {
		return result
	}
	if result == "" {
		return addition
	}
	return result + "\n" + addition
}

func uploadTaskResult(taskID, result string, exitCode int, finishedAt time.Time) {
	payload := map[string]interface{}{
		"task_id":     taskID,
		"result":      result,
		"exit_code":   exitCode,
		"finished_at": finishedAt,
	}

	jsonData, _ := json.Marshal(payload)
	endpoint := buildClientAPIEndpoint("/api/clients/task/result", nil)

	req, err := newJSONClientRequest("POST", endpoint, jsonData)
	if err != nil {
		log.Printf("Failed to create task result request: %v", err)
		return
	}

	client := newControlPlaneHTTPClient()
	maxRetry := flags.MaxRetries
	if maxRetry < 0 {
		maxRetry = 0
	}
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			log.Printf("Failed to upload task result, retrying %d/%d", attempt, maxRetry)
			time.Sleep(2 * time.Second)
			if resetErr := resetRequestBody(req); resetErr != nil {
				log.Printf("Failed to reset task result request body: %v", resetErr)
				return
			}
		}
		timedRequest, cancel := requestWithTimeout(req, 30*time.Second)
		requestStarted := time.Now()
		resp, err := client.Do(timedRequest)
		diagnostics.ObserveHTTP(requestStarted, err)
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
		}
		cancel()
		if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
			return
		}
		if attempt == maxRetry {
			if resp != nil {
				log.Printf("Failed to upload task result: %s", resp.Status)
			} else {
				log.Printf("Failed to upload task result after %d attempt(s)", attempt+1)
			}
		}
	}
}
