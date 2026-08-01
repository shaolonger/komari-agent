package monitoring

import (
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv3"
)

func GenerateReportV2() ([]byte, error) {
	started := time.Now()
	snapshot := &emptyReportSnapshot
	if engine := defaultReportEngine.Load(); engine != nil {
		snapshot = engine.store.load()
	}
	encoded, err := encodeReportV2(snapshot)
	diagnostics.ObserveReport(started, len(encoded), err)
	return encoded, err
}

func encodeReportV2(snapshot *ReportSnapshot) ([]byte, error) {
	return telemetryv2.Encode(toTelemetryV2(snapshot))
}

func toTelemetryV2(snapshot *ReportSnapshot) telemetryv2.Report {
	wire := *snapshot
	wire.Message = reportMessage(snapshot)
	sanitizeReportFloats(&wire)

	report := telemetryv2.Report{
		CPUUsage: wire.CPU.Usage,
		RAM: telemetryv2.Memory{
			Total: wire.RAM.Total,
			Used:  wire.RAM.Used,
		},
		Swap: telemetryv2.Memory{
			Total: wire.Swap.Total,
			Used:  wire.Swap.Used,
		},
		Load: telemetryv2.Load{
			Load1:  wire.Load.Load1,
			Load5:  wire.Load.Load5,
			Load15: wire.Load.Load15,
		},
		Disk: telemetryv2.Memory{
			Total: wire.Disk.Total,
			Used:  wire.Disk.Used,
		},
		Network: telemetryv2.Network{
			Up:        wire.Network.Up,
			Down:      wire.Network.Down,
			TotalUp:   wire.Network.TotalUp,
			TotalDown: wire.Network.TotalDown,
		},
		Connections: telemetryv2.Connections{
			TCP: wire.Connections.TCP,
			UDP: wire.Connections.UDP,
		},
		Uptime:  wire.Uptime,
		Process: wire.Process,
		Message: wire.Message,
	}
	if wire.GPU != nil {
		report.GPU = &telemetryv2.GPU{
			Detailed:     wire.GPU.Detailed,
			AverageUsage: wire.GPU.AverageUsage,
			Models:       wire.GPU.Models,
		}
		if wire.GPU.Detailed {
			report.GPU.Devices = make([]telemetryv2.GPUDevice, len(wire.GPU.DetailedInfo))
			for index, device := range wire.GPU.DetailedInfo {
				report.GPU.Devices[index] = telemetryv2.GPUDevice{
					Name:        device.Name,
					MemoryTotal: device.MemoryTotal,
					MemoryUsed:  device.MemoryUsed,
					Utilization: device.Utilization,
					Temperature: device.Temperature,
				}
			}
		}
	}
	return report
}

// GenerateReportV3 adds the latest immutable local snapshot to an aggregate
// envelope and emits it. The caller owns sequence/checkpoint scheduling so the
// same aggregator can span short reconnect generations.
func GenerateReportV3(aggregator *V3Aggregator, sequence uint64, sampledAt time.Time, forceCheckpoint bool) ([]byte, error) {
	if aggregator == nil {
		aggregator = NewV3Aggregator(time.Minute)
	}
	if err := aggregator.Add(CurrentReportSnapshot()); err != nil {
		return nil, err
	}
	return EncodeReportV3(aggregator, sequence, sampledAt, forceCheckpoint)
}

func CurrentReportSnapshot() ReportSnapshot {
	snapshot := &emptyReportSnapshot
	if engine := defaultReportEngine.Load(); engine != nil {
		snapshot = engine.store.load()
	}
	return cloneReportSnapshot(*snapshot)
}

func EncodeReportV3(aggregator *V3Aggregator, sequence uint64, sampledAt time.Time, forceCheckpoint bool) ([]byte, error) {
	frame, err := aggregator.Build(sequence, sampledAt, forceCheckpoint)
	if err != nil {
		return nil, err
	}
	encoded, err := telemetryv3.Encode(frame)
	diagnostics.ObserveReport(sampledAt, len(encoded), err)
	return encoded, err
}
