package monitoring

import (
	"testing"
	"time"
)

var benchmarkEncodedReport []byte

// BenchmarkGenerateReport measures the steady-state v1 report build. Platform
// sources are sampled independently and are deliberately absent from this path.
func BenchmarkGenerateReport(b *testing.B) {
	store := newReportSnapshotStore(time.Now)
	store.current.Store(benchmarkReportSnapshot())
	engine := &ReportEngine{store: store}
	previous := defaultReportEngine.Swap(engine)
	b.Cleanup(func() {
		defaultReportEngine.Store(previous)
	})

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkEncodedReport = GenerateReport()
	}
	b.StopTimer()
	if len(benchmarkEncodedReport) > 0 {
		b.ReportMetric(float64(len(benchmarkEncodedReport)), "bytes/report")
	}
}

func BenchmarkEncodeReportV1(b *testing.B) {
	snapshot := benchmarkReportSnapshot()
	b.ReportAllocs()
	for range b.N {
		benchmarkEncodedReport, _ = encodeReportV1(snapshot)
	}
}

func benchmarkReportSnapshot() *ReportSnapshot {
	return &ReportSnapshot{
		Connections: ConnectionsReport{TCP: 128, UDP: 16},
		CPU:         CPUReport{Usage: 37.5},
		Disk:        DiskReport{Total: 1 << 40, Used: 1 << 39},
		Load:        LoadReport{Load1: 1.25, Load5: 1.5, Load15: 2},
		Network:     NetworkReport{Up: 1024, Down: 2048, TotalUp: 1 << 30, TotalDown: 2 << 30},
		Process:     256,
		RAM:         MemoryReport{Total: 32 << 30, Used: 16 << 30},
		Swap:        MemoryReport{Total: 4 << 30, Used: 1 << 30},
		Uptime:      86_400,
	}
}
