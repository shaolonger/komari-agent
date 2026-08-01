package monitoring

import (
	"math"
	"testing"
	"time"
)

func TestV3AggregatorPreservesPeaksSumsCounterDeltasAndCheckpoints(t *testing.T) {
	aggregator := NewV3Aggregator(time.Minute)
	base := time.Unix(1_700_000_000, 0).UTC()
	for _, snapshot := range []ReportSnapshot{
		{CPU: CPUReport{Usage: 10}, RAM: MemoryReport{Total: 1000, Used: 400}, Network: NetworkReport{TotalUp: 100, TotalDown: 200}},
		{CPU: CPUReport{Usage: 90}, RAM: MemoryReport{Total: 1000, Used: 700}, Network: NetworkReport{TotalUp: 150, TotalDown: 260}},
		{CPU: CPUReport{Usage: 20}, RAM: MemoryReport{Total: 1000, Used: 500}, Network: NetworkReport{TotalUp: 5, TotalDown: 10}},
	} {
		if err := aggregator.Add(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	frame, err := aggregator.Build(1, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if !frame.Checkpoint || frame.Envelope.Count != 3 || frame.Envelope.CPUMin != 10 || frame.Envelope.CPUMax != 90 || frame.Envelope.CPUSum != 120 {
		t.Fatalf("CPU/checkpoint envelope = %#v", frame.Envelope)
	}
	if frame.Envelope.RAMUsedMin != 400 || frame.Envelope.RAMUsedMax != 700 || frame.Envelope.RAMUsedSum != 1600 {
		t.Fatalf("RAM envelope = %#v", frame.Envelope)
	}
	if frame.Envelope.NetworkUpDelta != 55 || frame.Envelope.NetworkDownDelta != 70 {
		t.Fatalf("counter deltas = %#v", frame.Envelope)
	}
	if aggregator.PendingSamples() != 0 {
		t.Fatal("build did not reset the envelope")
	}

	_ = aggregator.Add(ReportSnapshot{CPU: CPUReport{Usage: 30}, RAM: MemoryReport{Total: 1000, Used: 600}, Network: NetworkReport{TotalUp: 15, TotalDown: 30}})
	frame, err = aggregator.Build(2, base.Add(30*time.Second), false)
	if err != nil || frame.Checkpoint {
		t.Fatalf("unexpected intermediate checkpoint: %#v, %v", frame, err)
	}
	_ = aggregator.Add(ReportSnapshot{CPU: CPUReport{Usage: 40}, RAM: MemoryReport{Total: 1000, Used: 600}, Network: NetworkReport{TotalUp: 20, TotalDown: 40}})
	frame, _ = aggregator.Build(3, base.Add(time.Minute), false)
	if !frame.Checkpoint {
		t.Fatal("periodic checkpoint was not emitted")
	}
}

func TestV3AggregatorIsBoundedAndSaturatesIntegerSums(t *testing.T) {
	aggregator := NewV3Aggregator(time.Minute)
	for index := 0; index < 3600; index++ {
		if err := aggregator.Add(ReportSnapshot{CPU: CPUReport{Usage: 1}, RAM: MemoryReport{Total: math.MaxUint64, Used: math.MaxUint64}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Add(ReportSnapshot{}); err != ErrV3EnvelopeFull {
		t.Fatalf("full error = %v", err)
	}
	frame, err := aggregator.Build(1, time.Unix(1, 0), false)
	if err != nil || frame.Envelope.RAMUsedSum != math.MaxUint64 {
		t.Fatalf("saturated envelope = %#v, %v", frame.Envelope, err)
	}
}

func BenchmarkV3AggregateEnvelope(b *testing.B) {
	snapshot := ReportSnapshot{CPU: CPUReport{Usage: 42}, RAM: MemoryReport{Total: 8 << 30, Used: 3 << 30}, Network: NetworkReport{TotalUp: 1000, TotalDown: 2000}}
	b.ReportAllocs()
	for b.Loop() {
		aggregator := NewV3Aggregator(time.Minute)
		for index := 0; index < 5; index++ {
			_ = aggregator.Add(snapshot)
		}
		_, _ = aggregator.Build(1, time.Unix(1, 0), false)
	}
}
