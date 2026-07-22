package monitoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPooledReportBuffer = 64 * 1024

type CPUReport struct {
	Usage float64 `json:"usage"`
}

type MemoryReport struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type DiskReport struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type LoadReport struct {
	Load1  float64 `json:"load1"`
	Load15 float64 `json:"load15"`
	Load5  float64 `json:"load5"`
}

type NetworkReport struct {
	Down      uint64 `json:"down"`
	TotalDown uint64 `json:"totalDown"`
	TotalUp   uint64 `json:"totalUp"`
	Up        uint64 `json:"up"`
}

type ConnectionsReport struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}

type GPUDeviceReport struct {
	MemoryTotal uint64  `json:"memory_total"`
	MemoryUsed  uint64  `json:"memory_used"`
	Name        string  `json:"name"`
	Temperature uint64  `json:"temperature"`
	Utilization float64 `json:"utilization"`
}

type GPUReport struct {
	AverageUsage float64           `json:"-"`
	Count        int               `json:"-"`
	DetailedInfo []GPUDeviceReport `json:"-"`
	Models       []string          `json:"-"`
	Detailed     bool              `json:"-"`
}

func (report GPUReport) MarshalJSON() ([]byte, error) {
	if report.Detailed {
		return json.Marshal(struct {
			AverageUsage float64           `json:"average_usage"`
			Count        int               `json:"count"`
			DetailedInfo []GPUDeviceReport `json:"detailed_info"`
		}{
			AverageUsage: report.AverageUsage,
			Count:        report.Count,
			DetailedInfo: report.DetailedInfo,
		})
	}
	return json.Marshal(struct {
		Models []string `json:"models"`
	}{Models: report.Models})
}

type SampleStatus struct {
	AttemptedAt time.Time     `json:"attempted_at,omitempty"`
	CollectedAt time.Time     `json:"collected_at,omitempty"`
	Error       string        `json:"error,omitempty"`
	StaleAfter  time.Duration `json:"-"`
	Stale       bool          `json:"stale"`
}

type ReportMetadata struct {
	CPU         SampleStatus `json:"cpu"`
	Memory      SampleStatus `json:"memory"`
	Load        SampleStatus `json:"load"`
	Disk        SampleStatus `json:"disk"`
	Network     SampleStatus `json:"network"`
	Connections SampleStatus `json:"connections"`
	Uptime      SampleStatus `json:"uptime"`
	Process     SampleStatus `json:"process"`
	GPU         SampleStatus `json:"gpu"`
}

// ReportSnapshot is immutable after publication. Its field order intentionally
// matches encoding/json's legacy map-key order, preserving JSON v1 golden bytes.
type ReportSnapshot struct {
	Connections ConnectionsReport `json:"connections"`
	CPU         CPUReport         `json:"cpu"`
	Disk        DiskReport        `json:"disk"`
	GPU         *GPUReport        `json:"gpu,omitempty"`
	Load        LoadReport        `json:"load"`
	Message     string            `json:"message"`
	Network     NetworkReport     `json:"network"`
	Process     int               `json:"process"`
	RAM         MemoryReport      `json:"ram"`
	Swap        MemoryReport      `json:"swap"`
	Uptime      uint64            `json:"uptime"`
	Metadata    ReportMetadata    `json:"-"`
}

type reportSample uint8

const (
	reportSampleCPU reportSample = iota
	reportSampleMemory
	reportSampleLoad
	reportSampleDisk
	reportSampleNetwork
	reportSampleConnections
	reportSampleUptime
	reportSampleProcess
	reportSampleGPU
)

type reportSnapshotStore struct {
	mu      sync.Mutex
	now     func() time.Time
	current atomic.Pointer[ReportSnapshot]
}

func newReportSnapshotStore(now func() time.Time) *reportSnapshotStore {
	store := &reportSnapshotStore{now: now}
	store.current.Store(&ReportSnapshot{CPU: CPUReport{Usage: 0.001}})
	return store
}

func (store *reportSnapshotStore) publish(
	sample reportSample,
	staleAfter time.Duration,
	sampleErr error,
	update func(*ReportSnapshot),
) {
	store.mu.Lock()
	defer store.mu.Unlock()

	current := store.current.Load()
	next := *current
	now := store.now()
	status := reportSampleStatus(&next, sample)
	status.AttemptedAt = now
	status.StaleAfter = staleAfter
	if sampleErr != nil {
		status.Error = sampleErr.Error()
	} else {
		status.Error = ""
	}
	if update != nil {
		update(&next)
		if sample == reportSampleGPU {
			next.GPU = cloneGPUReport(next.GPU)
		}
		status.CollectedAt = now
	}
	store.current.Store(&next)
}

func (store *reportSnapshotStore) load() *ReportSnapshot {
	return store.current.Load()
}

func (store *reportSnapshotStore) snapshot() ReportSnapshot {
	result := cloneReportSnapshot(*store.current.Load())
	now := store.now()
	markReportStatusStale(&result.Metadata.CPU, now)
	markReportStatusStale(&result.Metadata.Memory, now)
	markReportStatusStale(&result.Metadata.Load, now)
	markReportStatusStale(&result.Metadata.Disk, now)
	markReportStatusStale(&result.Metadata.Network, now)
	markReportStatusStale(&result.Metadata.Connections, now)
	markReportStatusStale(&result.Metadata.Uptime, now)
	markReportStatusStale(&result.Metadata.Process, now)
	markReportStatusStale(&result.Metadata.GPU, now)
	return result
}

func reportSampleStatus(snapshot *ReportSnapshot, sample reportSample) *SampleStatus {
	switch sample {
	case reportSampleCPU:
		return &snapshot.Metadata.CPU
	case reportSampleMemory:
		return &snapshot.Metadata.Memory
	case reportSampleLoad:
		return &snapshot.Metadata.Load
	case reportSampleDisk:
		return &snapshot.Metadata.Disk
	case reportSampleNetwork:
		return &snapshot.Metadata.Network
	case reportSampleConnections:
		return &snapshot.Metadata.Connections
	case reportSampleUptime:
		return &snapshot.Metadata.Uptime
	case reportSampleProcess:
		return &snapshot.Metadata.Process
	case reportSampleGPU:
		return &snapshot.Metadata.GPU
	default:
		panic("unknown report sample")
	}
}

func markReportStatusStale(status *SampleStatus, now time.Time) {
	status.Stale = status.CollectedAt.IsZero() || status.StaleAfter <= 0 ||
		now.Before(status.CollectedAt) || now.Sub(status.CollectedAt) > status.StaleAfter
}

func cloneReportSnapshot(snapshot ReportSnapshot) ReportSnapshot {
	if snapshot.GPU == nil {
		return snapshot
	}
	gpu := *snapshot.GPU
	gpu.DetailedInfo = append([]GPUDeviceReport(nil), gpu.DetailedInfo...)
	gpu.Models = append([]string(nil), gpu.Models...)
	snapshot.GPU = &gpu
	return snapshot
}

var reportBufferPool = sync.Pool{New: func() any {
	return bytes.NewBuffer(make([]byte, 0, 1024))
}}

func EncodeReportV1(snapshot ReportSnapshot) ([]byte, error) {
	return encodeReportV1(&snapshot)
}

func encodeReportV1(snapshot *ReportSnapshot) ([]byte, error) {
	wire := *snapshot
	wire.Message = reportMessage(snapshot)
	sanitizeReportFloats(&wire)

	buffer := reportBufferPool.Get().(*bytes.Buffer)
	buffer.Reset()
	encoder := json.NewEncoder(buffer)
	if err := encoder.Encode(&wire); err != nil {
		if buffer.Cap() <= maxPooledReportBuffer {
			reportBufferPool.Put(buffer)
		}
		return nil, err
	}
	encoded := buffer.Bytes()
	if len(encoded) > 0 && encoded[len(encoded)-1] == '\n' {
		encoded = encoded[:len(encoded)-1]
	}
	result := append([]byte(nil), encoded...)
	if buffer.Cap() <= maxPooledReportBuffer {
		reportBufferPool.Put(buffer)
	}
	return result, nil
}

func reportMessage(snapshot *ReportSnapshot) string {
	var message strings.Builder
	message.WriteString(snapshot.Message)
	if snapshot.Metadata.Network.Error != "" {
		_, _ = fmt.Fprintf(&message, "failed to get network speed: %s\n", snapshot.Metadata.Network.Error)
	}
	if snapshot.Metadata.Connections.Error != "" {
		_, _ = fmt.Fprintf(&message, "failed to get connections: %s\n", snapshot.Metadata.Connections.Error)
	}
	if snapshot.Metadata.GPU.Error != "" {
		_, _ = fmt.Fprintf(&message, "failed to get detailed GPU info: %s\n", snapshot.Metadata.GPU.Error)
	}
	return message.String()
}

func sanitizeReportFloats(snapshot *ReportSnapshot) {
	snapshot.CPU.Usage = finiteFloat(snapshot.CPU.Usage)
	snapshot.Load.Load1 = finiteFloat(snapshot.Load.Load1)
	snapshot.Load.Load5 = finiteFloat(snapshot.Load.Load5)
	snapshot.Load.Load15 = finiteFloat(snapshot.Load.Load15)
	if snapshot.GPU == nil {
		return
	}
	requiresClone := !isFiniteFloat(snapshot.GPU.AverageUsage)
	for index := range snapshot.GPU.DetailedInfo {
		requiresClone = requiresClone || !isFiniteFloat(snapshot.GPU.DetailedInfo[index].Utilization)
	}
	if requiresClone {
		snapshot.GPU = cloneGPUReport(snapshot.GPU)
		snapshot.GPU.AverageUsage = finiteFloat(snapshot.GPU.AverageUsage)
		for index := range snapshot.GPU.DetailedInfo {
			snapshot.GPU.DetailedInfo[index].Utilization = finiteFloat(snapshot.GPU.DetailedInfo[index].Utilization)
		}
	}
}

func finiteFloat(value float64) float64 {
	if !isFiniteFloat(value) {
		return 0
	}
	return value
}

func isFiniteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func cloneGPUReport(source *GPUReport) *GPUReport {
	if source == nil {
		return nil
	}
	result := *source
	result.DetailedInfo = append([]GPUDeviceReport(nil), source.DetailedInfo...)
	result.Models = append([]string(nil), source.Models...)
	return &result
}
