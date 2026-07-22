package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync/atomic"
	"time"
)

type Sampler uint8

const (
	SamplerCPU Sampler = iota
	SamplerRAM
	SamplerSwap
	SamplerLoad
	SamplerDisk
	SamplerNetwork
	SamplerConnections
	SamplerUptime
	SamplerProcess
	SamplerGPU
	samplerCount
)

var samplerNames = [...]string{
	"cpu",
	"ram",
	"swap",
	"load",
	"disk",
	"network",
	"connections",
	"uptime",
	"process",
	"gpu",
}

type durationMetric struct {
	count      atomic.Uint64
	errors     atomic.Uint64
	timeouts   atomic.Uint64
	totalNanos atomic.Uint64
	maxNanos   atomic.Uint64
}

type metrics struct {
	samplers [samplerCount]durationMetric
	report   durationMetric
	dns      durationMetric
	dial     durationMetric
	http     durationMetric
	ping     durationMetric
	netQuery durationMetric
	netSave  durationMetric

	reportBytes       atomic.Uint64
	wsConnects        atomic.Uint64
	wsDisconnects     atomic.Uint64
	wsMessagesSent    atomic.Uint64
	wsMessagesRead    atomic.Uint64
	pingRejected      atomic.Uint64
	telemetryQueue    atomic.Int64
	controlQueue      atomic.Int64
	telemetryMerged   atomic.Uint64
	controlQueueRetry atomic.Uint64
	controlQueueDrops atomic.Uint64
	queueDrainTimeout atomic.Uint64
}

type DurationSnapshot struct {
	Count        uint64 `json:"count"`
	Errors       uint64 `json:"errors"`
	Timeouts     uint64 `json:"timeouts"`
	TotalNanos   uint64 `json:"total_nanos"`
	MaximumNanos uint64 `json:"maximum_nanos"`
}

type QueueSnapshot struct {
	TelemetryDepth  int64  `json:"telemetry_depth"`
	ControlDepth    int64  `json:"control_depth"`
	TelemetryMerged uint64 `json:"telemetry_merged"`
	ControlRetries  uint64 `json:"control_retries"`
	ControlDrops    uint64 `json:"control_drops"`
	DrainTimeouts   uint64 `json:"drain_timeouts"`
}

type WebSocketSnapshot struct {
	Connects     uint64 `json:"connects"`
	Disconnects  uint64 `json:"disconnects"`
	MessagesSent uint64 `json:"messages_sent"`
	MessagesRead uint64 `json:"messages_read"`
}

type Snapshot struct {
	Enabled      bool                        `json:"enabled"`
	GeneratedAt  time.Time                   `json:"generated_at"`
	Samplers     map[string]DurationSnapshot `json:"samplers"`
	Report       DurationSnapshot            `json:"report"`
	ReportBytes  uint64                      `json:"report_bytes"`
	DNS          DurationSnapshot            `json:"dns"`
	Dial         DurationSnapshot            `json:"dial"`
	HTTP         DurationSnapshot            `json:"http"`
	Ping         DurationSnapshot            `json:"ping"`
	PingRejected uint64                      `json:"ping_rejected"`
	NetQuery     DurationSnapshot            `json:"netstatic_query"`
	NetSave      DurationSnapshot            `json:"netstatic_save"`
	Queue        QueueSnapshot               `json:"queue"`
	WebSocket    WebSocketSnapshot           `json:"websocket"`
}

var enabled atomic.Bool
var registry metrics

func SetEnabled(value bool) {
	enabled.Store(value)
}

func Enabled() bool {
	return enabled.Load()
}

func ObserveSampler(kind Sampler, started time.Time, err error) {
	if !Enabled() || kind >= samplerCount {
		return
	}
	registry.samplers[kind].observe(time.Since(started), err)
}

func ObserveReport(started time.Time, size int, err error) {
	if !Enabled() {
		return
	}
	registry.report.observe(time.Since(started), err)
	if size > 0 {
		registry.reportBytes.Add(uint64(size))
	}
}

func ObserveDNS(started time.Time, err error) {
	if Enabled() {
		registry.dns.observe(time.Since(started), err)
	}
}

func ObserveDial(started time.Time, err error) {
	if Enabled() {
		registry.dial.observe(time.Since(started), err)
	}
}

func ObserveHTTP(started time.Time, err error) {
	if Enabled() {
		registry.http.observe(time.Since(started), err)
	}
}

func ObservePing(started time.Time, err error) {
	if Enabled() {
		registry.ping.observe(time.Since(started), err)
	}
}

func RecordPingRejected() {
	if Enabled() {
		registry.pingRejected.Add(1)
	}
}

func ObserveNetstaticQuery(started time.Time, err error) {
	if Enabled() {
		registry.netQuery.observe(time.Since(started), err)
	}
}

func ObserveNetstaticSave(started time.Time, err error) {
	if Enabled() {
		registry.netSave.observe(time.Since(started), err)
	}
}

func RecordWebSocketConnected() {
	if Enabled() {
		registry.wsConnects.Add(1)
	}
}

func RecordWebSocketDisconnected() {
	if Enabled() {
		registry.wsDisconnects.Add(1)
	}
}

func RecordWebSocketMessageSent() {
	if Enabled() {
		registry.wsMessagesSent.Add(1)
	}
}

func RecordWebSocketMessageRead() {
	if Enabled() {
		registry.wsMessagesRead.Add(1)
	}
}

func SetQueueDepth(telemetry, control int) {
	if !Enabled() {
		return
	}
	registry.telemetryQueue.Store(int64(telemetry))
	registry.controlQueue.Store(int64(control))
}

func RecordTelemetryMerged() {
	if Enabled() {
		registry.telemetryMerged.Add(1)
	}
}

func RecordControlQueueRetry() {
	if Enabled() {
		registry.controlQueueRetry.Add(1)
	}
}

func RecordControlQueueDrop() {
	if Enabled() {
		registry.controlQueueDrops.Add(1)
	}
}

func RecordQueueDrainTimeout() {
	if Enabled() {
		registry.queueDrainTimeout.Add(1)
	}
}

func CurrentSnapshot() Snapshot {
	samplers := make(map[string]DurationSnapshot, samplerCount)
	for index := Sampler(0); index < samplerCount; index++ {
		samplers[samplerNames[index]] = registry.samplers[index].snapshot()
	}
	return Snapshot{
		Enabled:      Enabled(),
		GeneratedAt:  time.Now().UTC(),
		Samplers:     samplers,
		Report:       registry.report.snapshot(),
		ReportBytes:  registry.reportBytes.Load(),
		DNS:          registry.dns.snapshot(),
		Dial:         registry.dial.snapshot(),
		HTTP:         registry.http.snapshot(),
		Ping:         registry.ping.snapshot(),
		PingRejected: registry.pingRejected.Load(),
		NetQuery:     registry.netQuery.snapshot(),
		NetSave:      registry.netSave.snapshot(),
		Queue: QueueSnapshot{
			TelemetryDepth:  registry.telemetryQueue.Load(),
			ControlDepth:    registry.controlQueue.Load(),
			TelemetryMerged: registry.telemetryMerged.Load(),
			ControlRetries:  registry.controlQueueRetry.Load(),
			ControlDrops:    registry.controlQueueDrops.Load(),
			DrainTimeouts:   registry.queueDrainTimeout.Load(),
		},
		WebSocket: WebSocketSnapshot{
			Connects:     registry.wsConnects.Load(),
			Disconnects:  registry.wsDisconnects.Load(),
			MessagesSent: registry.wsMessagesSent.Load(),
			MessagesRead: registry.wsMessagesRead.Load(),
		},
	}
}

func RunLogger(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !Enabled() {
				continue
			}
			encoded, err := json.Marshal(CurrentSnapshot())
			if err == nil {
				log.Printf("diagnostics=%s", encoded)
			}
		}
	}
}

func (metric *durationMetric) observe(duration time.Duration, err error) {
	nanos := uint64(max(duration.Nanoseconds(), 0))
	metric.count.Add(1)
	metric.totalNanos.Add(nanos)
	if err != nil {
		metric.errors.Add(1)
		if errors.Is(err, context.DeadlineExceeded) {
			metric.timeouts.Add(1)
		}
	}
	for {
		current := metric.maxNanos.Load()
		if nanos <= current || metric.maxNanos.CompareAndSwap(current, nanos) {
			break
		}
	}
}

func (metric *durationMetric) snapshot() DurationSnapshot {
	return DurationSnapshot{
		Count:        metric.count.Load(),
		Errors:       metric.errors.Load(),
		Timeouts:     metric.timeouts.Load(),
		TotalNanos:   metric.totalNanos.Load(),
		MaximumNanos: metric.maxNanos.Load(),
	}
}

func resetForTest() {
	registry = metrics{}
}
