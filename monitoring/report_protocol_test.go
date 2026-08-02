package monitoring

import (
	"encoding/json"
	"testing"

	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
)

func TestReportV2MatchesJSONV1Fields(t *testing.T) {
	snapshot := benchmarkReportSnapshot()
	snapshot.Metadata.Network.Error = "fixture network error"

	v1, err := encodeReportV1(snapshot)
	if err != nil {
		t.Fatalf("encodeReportV1 failed: %v", err)
	}
	var legacy ReportSnapshot
	if err := json.Unmarshal(v1, &legacy); err != nil {
		t.Fatalf("decode JSON v1: %v", err)
	}
	v2, err := encodeReportV2(snapshot)
	if err != nil {
		t.Fatalf("encodeReportV2 failed: %v", err)
	}
	decoded, err := telemetryv2.Decode(v2)
	if err != nil {
		t.Fatalf("decode telemetry v2: %v", err)
	}

	if decoded.CPUUsage != legacy.CPU.Usage ||
		decoded.RAM != (telemetryv2.Memory{Total: legacy.RAM.Total, Used: legacy.RAM.Used}) ||
		decoded.Swap != (telemetryv2.Memory{Total: legacy.Swap.Total, Used: legacy.Swap.Used}) ||
		decoded.Load != (telemetryv2.Load{Load1: legacy.Load.Load1, Load5: legacy.Load.Load5, Load15: legacy.Load.Load15}) ||
		decoded.Disk != (telemetryv2.Memory{Total: legacy.Disk.Total, Used: legacy.Disk.Used}) ||
		decoded.Network != (telemetryv2.Network{Up: legacy.Network.Up, Down: legacy.Network.Down, TotalUp: legacy.Network.TotalUp, TotalDown: legacy.Network.TotalDown}) ||
		decoded.Connections != (telemetryv2.Connections{TCP: legacy.Connections.TCP, UDP: legacy.Connections.UDP}) ||
		decoded.Uptime != legacy.Uptime || decoded.Process != legacy.Process || decoded.Message != legacy.Message {
		t.Fatalf("v1/v2 field mismatch\nv1: %+v\nv2: %+v", legacy, decoded)
	}
	if len(v2) >= len(v1) {
		t.Fatalf("v2 frame (%d bytes) is not smaller than v1 (%d bytes)", len(v2), len(v1))
	}
}

func TestReportV2DetailedGPUFields(t *testing.T) {
	snapshot := benchmarkReportSnapshot()
	snapshot.GPU = &GPUReport{
		Detailed:     true,
		AverageUsage: 50,
		Count:        2,
		DetailedInfo: []GPUDeviceReport{
			{Name: "GPU 0", MemoryTotal: 100, MemoryUsed: 25, Utilization: 25, Temperature: 60},
			{Name: "GPU 1", MemoryTotal: 200, MemoryUsed: 100, Utilization: 75, Temperature: 70},
		},
	}
	encoded, err := encodeReportV2(snapshot)
	if err != nil {
		t.Fatalf("encodeReportV2 failed: %v", err)
	}
	decoded, err := telemetryv2.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.GPU == nil || !decoded.GPU.Detailed || decoded.GPU.AverageUsage != 50 || len(decoded.GPU.Devices) != 2 {
		t.Fatalf("decoded GPU = %+v", decoded.GPU)
	}
}
