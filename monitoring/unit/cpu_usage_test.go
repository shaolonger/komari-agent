package monitoring

import (
	"errors"
	"math"
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestCPUUsageSamplerUsesNonBlockingCounterDeltas(t *testing.T) {
	samples := [][]cpu.TimesStat{
		{{User: 10, System: 10, Idle: 80}},
		{{User: 30, System: 20, Idle: 150}},
	}
	index := 0
	sampler := newCPUUsageSampler(func(bool) ([]cpu.TimesStat, error) {
		result := samples[index]
		index++
		return result, nil
	})

	first, err := sampler.Sample()
	if err != nil || first != 0 {
		t.Fatalf("unexpected first CPU sample: usage=%f err=%v", first, err)
	}
	second, err := sampler.Sample()
	if err != nil {
		t.Fatalf("second CPU sample failed: %v", err)
	}
	if math.Abs(second-30) > 0.001 {
		t.Fatalf("expected 30%% CPU usage, got %f", second)
	}
}

func TestCalculateCPUUsageHandlesResetAndBounds(t *testing.T) {
	previous := cpu.TimesStat{User: 50, System: 20, Idle: 100}
	reset := cpu.TimesStat{User: 1, System: 1, Idle: 1}
	if usage := calculateCPUUsage(previous, reset); usage != 0 {
		t.Fatalf("counter reset produced usage %f", usage)
	}
	if usage := calculateCPUUsage(cpu.TimesStat{}, cpu.TimesStat{User: 200}); usage != 100 {
		t.Fatalf("usage was not bounded at 100: %f", usage)
	}
}

func TestCPUUsageSamplerPropagatesSourceError(t *testing.T) {
	expected := errors.New("cpu source failed")
	sampler := newCPUUsageSampler(func(bool) ([]cpu.TimesStat, error) {
		return nil, expected
	})
	if _, err := sampler.Sample(); !errors.Is(err, expected) {
		t.Fatalf("expected source error, got %v", err)
	}
}
