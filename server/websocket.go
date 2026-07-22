package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	"github.com/komari-monitor/komari-agent/monitoring"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
	"github.com/komari-monitor/komari-agent/terminal"
	"github.com/komari-monitor/komari-agent/utils"
	"github.com/komari-monitor/komari-agent/ws"
)

const defaultMaxControlRequests = 10
const defaultControlRequestWindow = 10 * time.Second

var controlRequestLimiterMu sync.Mutex
var controlRequestTimes []time.Time

type controlPlaneMessage struct {
	Message string `json:"message"`
	// Terminal
	TerminalId string `json:"request_id,omitempty"`
	// Remote Exec
	ExecCommand string `json:"command,omitempty"`
	ExecTaskID  string `json:"task_id,omitempty"`
	// Ping
	PingTaskID uint   `json:"ping_task_id,omitempty"`
	PingType   string `json:"ping_type,omitempty"`
	PingTarget string `json:"ping_target,omitempty"`
}

func controlRequestLimit() int {
	if flags.MaxControlRequests > 0 {
		return flags.MaxControlRequests
	}
	return defaultMaxControlRequests
}

func controlRequestWindow() time.Duration {
	if flags.ControlRequestWindow > 0 {
		return time.Duration(flags.ControlRequestWindow) * time.Second
	}
	return defaultControlRequestWindow
}

func allowControlRequest(now time.Time) bool {
	window := controlRequestWindow()
	limit := controlRequestLimit()

	controlRequestLimiterMu.Lock()
	defer controlRequestLimiterMu.Unlock()

	cutoff := now.Add(-window)
	filtered := controlRequestTimes[:0]
	for _, requestTime := range controlRequestTimes {
		if requestTime.After(cutoff) {
			filtered = append(filtered, requestTime)
		}
	}
	controlRequestTimes = filtered
	if len(controlRequestTimes) >= limit {
		return false
	}
	controlRequestTimes = append(controlRequestTimes, now)
	return true
}

func isTerminalControlMessage(message controlPlaneMessage) bool {
	return message.Message == "terminal" || message.TerminalId != ""
}

func isExecControlMessage(message controlPlaneMessage) bool {
	return message.Message == "exec"
}

func isPingControlMessage(message controlPlaneMessage) bool {
	return message.Message == "ping" || message.PingTaskID != 0 || message.PingType != "" || message.PingTarget != ""
}

func shouldRateLimitControlRequest(message controlPlaneMessage) bool {
	return isTerminalControlMessage(message) || isExecControlMessage(message)
}

func EstablishWebSocketConnection() {
	if err := monitoring.StartReportSampler(context.Background()); err != nil {
		log.Printf("Failed to start report sampler: %v", err)
		return
	}
	defer monitoring.StopReportSampler()

	websocketEndpoint := buildClientWebSocketEndpoint("/api/clients/report", nil)

	// 转换中文域名为 ASCII 兼容编码
	if convertedEndpoint, err := utils.ConvertIDNToASCII(websocketEndpoint); err == nil {
		websocketEndpoint = convertedEndpoint
	} else {
		log.Printf("Warning: Failed to convert WebSocket IDN to ASCII: %v", err)
	}

	var conn *ws.SafeConn
	wireProtocol := telemetryProtocolV1
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	var err error
	var interval float64
	if flags.Interval <= 1 {
		interval = 1
	} else {
		interval = flags.Interval - 1
	}

	dataTicker := time.NewTicker(time.Duration(interval * float64(time.Second)))
	defer dataTicker.Stop()

	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-dataTicker.C:
			if conn == nil {
				log.Println("Attempting to connect to WebSocket...")
				retry := 0
				for retry <= flags.MaxRetries {
					if retry > 0 {
						log.Println("Retrying websocket connection, attempt:", retry)
					}
					conn, wireProtocol, err = connectWebSocket(websocketEndpoint)
					if err == nil {
						log.Println("WebSocket connected")
						diagnostics.RecordWebSocketConnected()
						go handleWebSocketMessages(conn, make(chan struct{}))
						break
					} else {
						log.Println("Failed to connect to WebSocket:", err)
					}
					retry++
					time.Sleep(time.Duration(flags.ReconnectInterval) * time.Second)
				}

				if retry > flags.MaxRetries {
					log.Println("Max retries reached.")
					return
				}
			}

			messageType, data, frameErr := buildTelemetryFrame(wireProtocol)
			if frameErr != nil {
				log.Printf("Telemetry v2 encoding failed; sent JSON v1 fallback: %v", frameErr)
			}
			err = conn.WriteMessage(messageType, data)
			if err != nil {
				log.Println("Failed to send WebSocket message:", err)
				conn.Close()
				conn = nil // Mark connection as dead
				diagnostics.RecordWebSocketDisconnected()
				continue
			}
			diagnostics.RecordWebSocketMessageSent()
		case <-heartbeatTicker.C:
			if conn != nil {
				err := conn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					log.Println("Failed to send heartbeat:", err)
					conn.Close()
					conn = nil // Mark connection as dead
					diagnostics.RecordWebSocketDisconnected()
				}
			}
		}
	}
}

type telemetryProtocol uint8

const (
	telemetryProtocolV1 telemetryProtocol = iota + 1
	telemetryProtocolV2
)

func connectWebSocket(websocketEndpoint string) (*ws.SafeConn, telemetryProtocol, error) {
	dialer := newTelemetryWSDialer()

	headers := newWSHeaders()

	conn, resp, err := dialer.Dial(websocketEndpoint, headers)
	if err != nil {
		if resp != nil && resp.StatusCode != 101 {
			return nil, telemetryProtocolV1, fmt.Errorf("%s", resp.Status)
		}
		return nil, telemetryProtocolV1, err
	}
	protocol, err := negotiatedTelemetryProtocol(conn.Subprotocol())
	if err != nil {
		_ = conn.Close()
		return nil, telemetryProtocolV1, err
	}
	return ws.NewSafeConn(conn), protocol, nil
}

func negotiatedTelemetryProtocol(selected string) (telemetryProtocol, error) {
	switch selected {
	case "", telemetryv2.LegacySubprotocol:
		return telemetryProtocolV1, nil
	case telemetryv2.Subprotocol:
		return telemetryProtocolV2, nil
	default:
		return telemetryProtocolV1, fmt.Errorf("server selected unsupported telemetry subprotocol %q", selected)
	}
}

func buildTelemetryFrame(protocol telemetryProtocol) (messageType int, payload []byte, encodeErr error) {
	return buildTelemetryFrameWith(protocol, monitoring.GenerateReport, monitoring.GenerateReportV2)
}

func buildTelemetryFrameWith(
	protocol telemetryProtocol,
	generateV1 func() []byte,
	generateV2 func() ([]byte, error),
) (messageType int, payload []byte, encodeErr error) {
	if protocol == telemetryProtocolV2 {
		payload, encodeErr = generateV2()
		if encodeErr == nil {
			return websocket.BinaryMessage, payload, nil
		}
	}
	return websocket.TextMessage, generateV1(), encodeErr
}

func handleWebSocketMessages(conn *ws.SafeConn, done chan<- struct{}) {
	defer close(done)
	for {
		_, message_raw, err := conn.ReadMessage()
		if err != nil {
			log.Println("WebSocket read error:", err)
			return
		}
		diagnostics.RecordWebSocketMessageRead()
		var message controlPlaneMessage
		err = json.Unmarshal(message_raw, &message)
		if err != nil {
			log.Println("Bad ws message:", err)
			continue
		}
		if shouldRateLimitControlRequest(message) {
			if !allowControlRequest(time.Now()) {
				log.Printf("Remote control request rejected due to rate limiting: message=%s", message.Message)
				if message.Message == "exec" && message.ExecTaskID != "" {
					taskResultUploader(message.ExecTaskID, "Remote control request rejected due to rate limiting.", -1, time.Now())
				}
				continue
			}
		}

		if isTerminalControlMessage(message) {
			go establishTerminalConnection(message.TerminalId)
			continue
		}
		if isExecControlMessage(message) {
			go NewTask(message.ExecTaskID, message.ExecCommand)
			continue
		}
		if isPingControlMessage(message) {
			go NewPingTask(conn, message.PingTaskID, message.PingType, message.PingTarget)
			continue
		}
	}
}

// connectWebSocket attempts to establish a WebSocket connection and upload basic info

// establishTerminalConnection 建立终端连接并使用terminal包处理终端操作
func establishTerminalConnection(id string) {
	endpoint := buildClientWebSocketEndpoint("/api/clients/terminal", url.Values{"id": []string{id}})

	// 转换中文域名为 ASCII 兼容编码
	if convertedEndpoint, err := utils.ConvertIDNToASCII(endpoint); err == nil {
		endpoint = convertedEndpoint
	} else {
		log.Printf("Warning: Failed to convert Terminal WebSocket IDN to ASCII: %v", err)
	}

	// 使用与主 WS 相同的拨号策略
	dialer := newWSDialer()

	headers := newWSHeaders()

	conn, _, err := dialer.Dial(endpoint, headers)
	if err != nil {
		log.Println("Failed to establish terminal connection:", err)
		return
	}

	// 启动终端
	terminal.StartTerminal(conn)
	if conn != nil {
		conn.Close()
	}
}

// newWSDialer 构造统一的 WebSocket 拨号器（自定义解析、IPv4/IPv6 动态排序、可选 TLS 忽略）
func newWSDialer() *websocket.Dialer {
	d := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		NetDialContext:   dnsresolver.GetDialContext(15 * time.Second),
		Proxy:            http.ProxyFromEnvironment,
	}
	if flags.IgnoreUnsafeCert {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return d
}

func newTelemetryWSDialer() *websocket.Dialer {
	dialer := newWSDialer()
	dialer.Subprotocols = []string{telemetryv2.Subprotocol, telemetryv2.LegacySubprotocol}
	return dialer
}

// newWSHeaders 统一构造 WS 请求头（含 Cloudflare Access 头）
func newWSHeaders() http.Header {
	headers := http.Header{}
	applyClientAuthHeaders(headers)
	return headers
}
