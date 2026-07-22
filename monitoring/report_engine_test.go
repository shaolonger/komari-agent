package monitoring

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	unit "github.com/komari-monitor/komari-agent/monitoring/unit"
)

func TestReportEngineEncodingNeverCallsPlatformSources(t *testing.T) {
	var calls atomic.Int64
	called := func() { calls.Add(1) }
	sources := reportSources{
		cpu: func() unit.CpuInfo {
			called()
			return unit.CpuInfo{CPUUsage: 25}
		},
		memory: func() unit.MemoryInfo {
			called()
			return unit.MemoryInfo{RAM: unit.RamInfo{Total: 100, Used: 50}, Swap: unit.RamInfo{Total: 20, Used: 10}}
		},
		load: func() unit.LoadInfo {
			called()
			return unit.LoadInfo{Load1: 1, Load5: 2, Load15: 3}
		},
		disk: func() unit.DiskInfo {
			called()
			return unit.DiskInfo{Total: 1_000, Used: 500}
		},
		network: func() (uint64, uint64, uint64, uint64, error) {
			called()
			return 1_000, 2_000, 100, 200, nil
		},
		connections: func() (int, int, error) {
			called()
			return 12, 3, nil
		},
		uptime: func() (uint64, error) {
			called()
			return 999, nil
		},
		process: func() int {
			called()
			return 42
		},
		gpu:       func() ([]unit.DetailedGPUInfo, error) { return nil, nil },
		gpuModels: func() ([]string, error) { return nil, nil },
	}
	config := reportEngineConfig{
		fastInterval:       time.Hour,
		uptimeInterval:     time.Hour,
		connectionInterval: time.Hour,
		processInterval:    time.Hour,
		diskInterval:       time.Hour,
		gpuInterval:        time.Hour,
	}
	engine, err := newReportEngine(sources, config, time.Now)
	if err != nil {
		t.Fatalf("newReportEngine failed: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(engine.Stop)

	waitForReportSnapshot(t, engine, func(snapshot ReportSnapshot) bool {
		return snapshot.Metadata.CPU.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Memory.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Load.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Disk.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Network.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Connections.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Uptime.CollectedAt.IsZero() == false &&
			snapshot.Metadata.Process.CollectedAt.IsZero() == false
	})
	before := calls.Load()
	if before != 8 {
		t.Fatalf("platform source calls = %d, want 8", before)
	}
	for range 1_000 {
		encoded, err := engine.EncodeV1()
		if err != nil || len(encoded) == 0 {
			t.Fatalf("EncodeV1 failed: len=%d err=%v", len(encoded), err)
		}
	}
	if after := calls.Load(); after != before {
		t.Fatalf("encoding called platform sources: before=%d after=%d", before, after)
	}
}

func TestDetailedGPUReportHandlesEmptyAndCopiesValues(t *testing.T) {
	empty := detailedGPUReport(nil)
	if empty == nil || !empty.Detailed || empty.Count != 0 || empty.AverageUsage != 0 || empty.DetailedInfo == nil {
		t.Fatalf("empty GPU report = %+v", empty)
	}
	values := []unit.DetailedGPUInfo{
		{Name: "one", Utilization: 25, MemoryTotal: 100, MemoryUsed: 50, Temperature: 60},
		{Name: "two", Utilization: 75, MemoryTotal: 200, MemoryUsed: 100, Temperature: 70},
	}
	got := detailedGPUReport(values)
	if got.Count != 2 || got.AverageUsage != 50 || len(got.DetailedInfo) != 2 {
		t.Fatalf("GPU report = %+v", got)
	}
}

func waitForReportSnapshot(t *testing.T, engine *ReportEngine, ready func(ReportSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready(engine.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for report snapshot: %+v", engine.Snapshot().Metadata)
}
