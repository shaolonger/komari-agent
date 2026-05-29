package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/http"
	"os/exec"
	"strconv"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	ping "github.com/prometheus-community/pro-bing"
)

type pingResultWriter interface {
	WriteJSON(v interface{}) error
}

const defaultTaskExecutionTimeout = 5 * time.Minute
const defaultTaskOutputLimit = 128 * 1024
const defaultTaskConcurrencyLimit = 1
const defaultMaxConcurrentPings = 2
const defaultPingMinInterval = 500 * time.Millisecond

var defaultAllowedPingTypes = []string{"tcp", "http"}
var defaultAllowedPingTCPPorts = []int{80, 443}

var taskExecutionTimeout = defaultTaskExecutionTimeout
var taskOutputLimit = defaultTaskOutputLimit
var taskConcurrencyLimit = defaultTaskConcurrencyLimit
var taskResultUploader = uploadTaskResult

var pingExecutionSlotsMu sync.Mutex
var pingExecutionSlots chan struct{}
var pingExecutionSlotsLimit int

var pingRateLimitMu sync.Mutex
var lastAcceptedPingAt time.Time

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
	buffer    bytes.Buffer
	limit     int
	totalBytes int
	truncated bool
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

	// 创建HTTP请求以支持自定义头部
	req, err := newJSONClientRequest("POST", endpoint, jsonData)
	if err != nil {
		log.Printf("Failed to create task result request: %v", err)
		return
	}

	client := newControlPlaneHTTPClient(30 * time.Second)
	resp, err := client.Do(req)
	maxRetry := flags.MaxRetries
	for i := 0; i < maxRetry && (err != nil || resp.StatusCode != http.StatusOK); i++ {
		log.Printf("Failed to upload task result, retrying %d/%d", i+1, maxRetry)
		time.Sleep(2 * time.Second) // Wait before retrying
		if resetErr := resetRequestBody(req); resetErr != nil {
			log.Printf("Failed to reset task result request body: %v", resetErr)
			break
		}
		resp, err = client.Do(req)
	}
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("Failed to upload task result: %s", resp.Status)
		}
	}
}

// resolveIP 解析域名到 IP 地址，排除 DNS 查询时间
func resolveIP(target string) (string, error) {
	// 如果已经是 IP 地址，直接返回
	if ip := net.ParseIP(target); ip != nil {
		return target, nil
	}
	// 解析域名到 IP
	addrs, err := net.LookupHost(target)
	if err != nil || len(addrs) == 0 {
		return "", errors.New("failed to resolve target")
	}
	return addrs[0], nil // 返回第一个解析的 IP
}

func pingAllowedTypes() []string {
	if strings.TrimSpace(flags.AllowedPingTypes) == "" {
		return slices.Clone(defaultAllowedPingTypes)
	}

	rawTypes := strings.Split(flags.AllowedPingTypes, ",")
	types := make([]string, 0, len(rawTypes))
	for _, rawType := range rawTypes {
		pingType := strings.ToLower(strings.TrimSpace(rawType))
		if pingType == "" {
			continue
		}
		types = append(types, pingType)
	}
	if len(types) == 0 {
		return slices.Clone(defaultAllowedPingTypes)
	}
	return types
}

func pingAllowedPorts() []int {
	if strings.TrimSpace(flags.AllowedPingTCPPorts) == "" {
		return slices.Clone(defaultAllowedPingTCPPorts)
	}

	rawPorts := strings.Split(flags.AllowedPingTCPPorts, ",")
	ports := make([]int, 0, len(rawPorts))
	for _, rawPort := range rawPorts {
		portValue, err := strconv.Atoi(strings.TrimSpace(rawPort))
		if err != nil || portValue <= 0 || portValue > 65535 {
			continue
		}
		ports = append(ports, portValue)
	}
	if len(ports) == 0 {
		return slices.Clone(defaultAllowedPingTCPPorts)
	}
	return ports
}

func pingConcurrencyLimit() int {
	if flags.MaxConcurrentPings > 0 {
		return flags.MaxConcurrentPings
	}
	return defaultMaxConcurrentPings
}

func pingMinInterval() time.Duration {
	if flags.PingMinIntervalMillis < 0 {
		return defaultPingMinInterval
	}
	return time.Duration(flags.PingMinIntervalMillis) * time.Millisecond
}

func tryAcquirePingExecutionSlot() (func(), bool) {
	limit := pingConcurrencyLimit()
	if limit < 1 {
		limit = 1
	}

	pingExecutionSlotsMu.Lock()
	if pingExecutionSlots == nil || pingExecutionSlotsLimit != limit {
		pingExecutionSlots = make(chan struct{}, limit)
		pingExecutionSlotsLimit = limit
	}
	slots := pingExecutionSlots
	pingExecutionSlotsMu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() {
			<-slots
		}, true
	default:
		return func() {}, false
	}
}

func allowPingNow() bool {
	interval := pingMinInterval()
	pingRateLimitMu.Lock()
	defer pingRateLimitMu.Unlock()

	now := time.Now()
	if !lastAcceptedPingAt.IsZero() && now.Sub(lastAcceptedPingAt) < interval {
		return false
	}
	lastAcceptedPingAt = now
	return true
}

func pingTypeAllowed(pingType string) bool {
	normalizedType := strings.ToLower(strings.TrimSpace(pingType))
	for _, allowedType := range pingAllowedTypes() {
		if normalizedType == allowedType {
			return true
		}
	}
	return false
}

func pingPortAllowed(port int) bool {
	for _, allowedPort := range pingAllowedPorts() {
		if port == allowedPort {
			return true
		}
	}
	return false
}

func parsePingTarget(pingType, pingTarget string) (string, int, error) {
	pingType = strings.ToLower(strings.TrimSpace(pingType))
	trimmedTarget := strings.TrimSpace(pingTarget)
	if trimmedTarget == "" {
		return "", 0, errors.New("empty ping target")
	}

	switch pingType {
	case "icmp":
		host := trimmedTarget
		if splitHost, _, err := net.SplitHostPort(trimmedTarget); err == nil {
			host = splitHost
		}
		host = strings.Trim(host, "[]")
		return host, 0, nil
	case "tcp":
		host, port, err := net.SplitHostPort(trimmedTarget)
		if err != nil {
			host = trimmedTarget
			port = "80"
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber <= 0 || portNumber > 65535 {
			return "", 0, errors.New("invalid tcp ping port")
		}
		return strings.Trim(host, "[]"), portNumber, nil
	case "http":
		urlValue := trimmedTarget
		if !strings.HasPrefix(urlValue, "http://") && !strings.HasPrefix(urlValue, "https://") {
			urlValue = "http://" + urlValue
		}
		parsedURL, err := http.NewRequest("GET", urlValue, nil)
		if err != nil {
			return "", 0, errors.New("invalid http ping target")
		}
		if parsedURL.URL.Hostname() == "" {
			return "", 0, errors.New("invalid http ping host")
		}
		portNumber := 80
		if parsedURL.URL.Scheme == "https" {
			portNumber = 443
		}
		if parsedURL.URL.Port() != "" {
			portNumber, err = strconv.Atoi(parsedURL.URL.Port())
			if err != nil || portNumber <= 0 || portNumber > 65535 {
				return "", 0, errors.New("invalid http ping port")
			}
		}
		return parsedURL.URL.Hostname(), portNumber, nil
	default:
		return "", 0, errors.New("unsupported ping type")
	}
}

func pingTargetAllowed(pingType, pingTarget string) error {
	if !pingTypeAllowed(pingType) {
		return fmt.Errorf("ping type %s is not allowed", pingType)
	}

	host, port, err := parsePingTarget(pingType, pingTarget)
	if err != nil {
		return err
	}
	if port != 0 && !pingPortAllowed(port) {
		return fmt.Errorf("ping port %d is not allowed", port)
	}

	resolvedIP, err := resolveIP(host)
	if err != nil {
		return err
	}
	if flags.AllowPrivatePingTargets {
		return nil
	}

	ip, ok := netip.AddrFromSlice(net.ParseIP(resolvedIP))
	if !ok {
		return errors.New("failed to parse ping target address")
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("ping target %s resolves to a restricted address", host)
	}

	return nil
}

func icmpPing(target string, timeout time.Duration) (int64, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	// For ICMP, we only need the host/IP, port is irrelevant.
	// If the host is an IPv6 literal, it might be wrapped in brackets.
	host = strings.Trim(host, "[]")

	// 先解析 IP 地址
	ip, err := resolveIP(host)
	if err != nil {
		return -1, err
	}

	pinger, err := ping.NewPinger(ip)
	if err != nil {
		return -1, err
	}
	pinger.Count = 1
	pinger.Timeout = timeout
	pinger.SetPrivileged(true)
	err = pinger.Run()
	if err != nil {
		return -1, err
	}
	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		return -1, errors.New("no packets received")
	}
	return stats.AvgRtt.Milliseconds(), nil
}

func tcpPing(target string, timeout time.Duration) (int64, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		// No port, assume port 80
		host = target
		port = "80"
	}

	ip, err := resolveIP(host)
	if err != nil {
		return -1, err
	}

	targetAddr := net.JoinHostPort(ip, port)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", targetAddr, timeout)
	if err != nil {
		return -1, err
	}
	defer conn.Close()
	return time.Since(start).Milliseconds(), nil
}

func httpPing(target string, timeout time.Duration) (int64, error) {
	// Handle raw IPv6 address for URL
	if strings.Contains(target, ":") && !strings.Contains(target, "[") {
		// check if it's a valid IP to avoid wrapping hostnames
		if ip := net.ParseIP(target); ip != nil && ip.To4() == nil {
			target = "[" + target + "]"
		}
	}

	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// 在 Dial 之前解析 IP，排除 DNS 时间
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ip, err := resolveIP(host)
				if err != nil {
					return nil, err
				}
				return net.DialTimeout(network, net.JoinHostPort(ip, port), timeout)
			},
		},
	}
	start := time.Now()
	resp, err := client.Get(target)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return latency, nil
	}
	return latency, errors.New("http status not ok")
}

func NewPingTask(conn pingResultWriter, taskID uint, pingType, pingTarget string) {
	if taskID == 0 {
		log.Printf("Invalid task ID: %d", taskID)
		return
	}
	if !flags.PingEnabled() {
		log.Printf("Ping task %d rejected: ping capability is disabled", taskID)
		writePingResult(conn, taskID, pingType, -1)
		return
	}
	if err := pingTargetAllowed(pingType, pingTarget); err != nil {
		log.Printf("Ping task %d rejected: %v", taskID, err)
		writePingResult(conn, taskID, pingType, -1)
		return
	}
	releasePingSlot, ok := tryAcquirePingExecutionSlot()
	if !ok {
		log.Printf("Ping task %d rejected: concurrent ping limit reached", taskID)
		writePingResult(conn, taskID, pingType, -1)
		return
	}
	defer releasePingSlot()
	if !allowPingNow() {
		log.Printf("Ping task %d rejected: ping rate limit reached", taskID)
		writePingResult(conn, taskID, pingType, -1)
		return
	}
	var err error = nil
	var latency int64
	pingResult := -1
	timeout := 3 * time.Second           // 默认超时时间
	const highLatencyThreshold = 1000    // ms 阈值
	const retryDropThresholdTcping = 800 // ms 重试中延迟降低超过此值则基本认为发生重传
	// 800ms = SYN/SYN-ACK 首次超时重传 1000ms - 防误判容许 200ms 延迟抖动

	measure := func() (int64, error) {
		switch pingType {
		case "icmp":
			return icmpPing(pingTarget, timeout)
		case "tcp":
			return tcpPing(pingTarget, timeout)
		case "http":
			return httpPing(pingTarget, timeout)
		default:
			return -1, errors.New("unsupported ping type")
		}
	}
	PingHighLatencyRetries := 3
	// 首次测量
	if latency, err = measure(); err == nil {
		firstLatency := latency
		if latency > int64(highLatencyThreshold) && PingHighLatencyRetries > 0 {
			attempts := PingHighLatencyRetries
			for i := 0; i < attempts; i++ {
				if second, err2 := measure(); err2 == nil {
					if second <= int64(highLatencyThreshold) {
						if pingType == "tcp" && firstLatency-second > int64(retryDropThresholdTcping) {
							err = errors.New("suspicious retransmission detected in tcp handshake")
							break
						}
						latency = second
						break
					}
					if i == attempts-1 { // 最后一次仍高
						err = errors.New("latency remains high after retries")
					}
				} else {
					err = err2
					break
				}
			}
		}
	}

	if err != nil {
		log.Printf("Ping task %d failed: %v", taskID, err)
		pingResult = -1 // 如果有错误，设置结果为 -1
	} else {
		pingResult = int(latency)
	}
	writePingResult(conn, taskID, pingType, pingResult)
}

func writePingResult(conn pingResultWriter, taskID uint, pingType string, pingResult int) {
	payload := map[string]interface{}{
		"type":        "ping_result",
		"task_id":     taskID,
		"ping_type":   pingType,
		"value":       pingResult,
		"finished_at": time.Now(),
	}
	// https://github.com/komari-monitor/komari/commit/eb87a4fc330b7d1c407fa4ff70177615a4f50a1f
	// -1 代表丢包，服务端计算
	//if pingResult == -1 {
	//	return
	//}
	if err := conn.WriteJSON(payload); err != nil {
		log.Printf("Failed to write JSON to WebSocket: %v", err)
	}
}
