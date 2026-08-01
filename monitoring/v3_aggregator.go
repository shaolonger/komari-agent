package monitoring

import (
	"errors"
	"math"
	"time"

	"github.com/komari-monitor/komari-agent/protocol/telemetryv3"
)

var ErrV3EnvelopeFull = errors.New("telemetry v3 aggregate envelope is full")

type V3Aggregator struct {
	checkpointInterval time.Duration
	lastCheckpoint     time.Time
	latest             ReportSnapshot
	count              uint32
	cpuMin             float64
	cpuMax             float64
	cpuSum             float64
	ramMin             uint64
	ramMax             uint64
	ramSum             uint64
	previousUp         uint64
	previousDown       uint64
	haveCounters       bool
	upDelta            uint64
	downDelta          uint64
}

func NewV3Aggregator(checkpointInterval time.Duration) *V3Aggregator {
	if checkpointInterval <= 0 {
		checkpointInterval = time.Minute
	}
	return &V3Aggregator{checkpointInterval: checkpointInterval}
}

func (aggregator *V3Aggregator) Add(snapshot ReportSnapshot) error {
	if aggregator.count >= telemetryv3.MaxSampleCount {
		return ErrV3EnvelopeFull
	}
	snapshot = cloneReportSnapshot(snapshot)
	cpu := finiteFloat(snapshot.CPU.Usage)
	ram := snapshot.RAM.Used
	if aggregator.count == 0 {
		aggregator.cpuMin, aggregator.cpuMax = cpu, cpu
		aggregator.ramMin, aggregator.ramMax = ram, ram
	} else {
		aggregator.cpuMin = min(aggregator.cpuMin, cpu)
		aggregator.cpuMax = max(aggregator.cpuMax, cpu)
		aggregator.ramMin = min(aggregator.ramMin, ram)
		aggregator.ramMax = max(aggregator.ramMax, ram)
	}
	aggregator.cpuSum += cpu
	aggregator.ramSum = saturatingAdd(aggregator.ramSum, ram)
	if aggregator.haveCounters {
		aggregator.upDelta = saturatingAdd(aggregator.upDelta, counterDelta(aggregator.previousUp, snapshot.Network.TotalUp))
		aggregator.downDelta = saturatingAdd(aggregator.downDelta, counterDelta(aggregator.previousDown, snapshot.Network.TotalDown))
	}
	aggregator.previousUp = snapshot.Network.TotalUp
	aggregator.previousDown = snapshot.Network.TotalDown
	aggregator.haveCounters = true
	aggregator.count++
	aggregator.latest = snapshot
	return nil
}

func (aggregator *V3Aggregator) Build(sequence uint64, sampledAt time.Time, forceCheckpoint bool) (telemetryv3.Frame, error) {
	if aggregator.count == 0 {
		return telemetryv3.Frame{}, errors.New("telemetry v3 aggregate envelope is empty")
	}
	checkpoint := forceCheckpoint || aggregator.lastCheckpoint.IsZero() || sampledAt.Sub(aggregator.lastCheckpoint) >= aggregator.checkpointInterval || sampledAt.Before(aggregator.lastCheckpoint)
	frame := telemetryv3.Frame{
		Sequence: sequence, SampledAt: sampledAt, Checkpoint: checkpoint,
		Envelope: telemetryv3.Envelope{
			Count: aggregator.count, CPUMin: aggregator.cpuMin, CPUMax: aggregator.cpuMax, CPUSum: aggregator.cpuSum,
			RAMUsedMin: aggregator.ramMin, RAMUsedMax: aggregator.ramMax, RAMUsedSum: aggregator.ramSum,
			NetworkUpDelta: aggregator.upDelta, NetworkDownDelta: aggregator.downDelta,
		},
		Latest: toTelemetryV2(&aggregator.latest),
	}
	if checkpoint {
		aggregator.lastCheckpoint = sampledAt
	}
	aggregator.resetEnvelope()
	return frame, nil
}

func (aggregator *V3Aggregator) PendingSamples() uint32 { return aggregator.count }

func (aggregator *V3Aggregator) resetEnvelope() {
	aggregator.count = 0
	aggregator.cpuMin, aggregator.cpuMax, aggregator.cpuSum = 0, 0, 0
	aggregator.ramMin, aggregator.ramMax, aggregator.ramSum = 0, 0, 0
	aggregator.upDelta, aggregator.downDelta = 0, 0
	// Keep the previous counters across envelopes so every increment belongs to
	// exactly one emitted delta.
}

func counterDelta(previous, current uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}
