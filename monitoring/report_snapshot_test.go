package monitoring

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncodeReportV1GoldenWithoutGPU(t *testing.T) {
	snapshot := ReportSnapshot{
		Connections: ConnectionsReport{TCP: 12, UDP: 3},
		CPU:         CPUReport{Usage: 12.5},
		Disk:        DiskReport{Total: 1_000, Used: 500},
		Load:        LoadReport{Load1: 1.1, Load5: 1.2, Load15: 1.3},
		Network:     NetworkReport{Up: 100, Down: 200, TotalUp: 1_000, TotalDown: 2_000},
		Process:     42,
		RAM:         MemoryReport{Total: 8_000, Used: 4_000},
		Swap:        MemoryReport{Total: 2_000, Used: 100},
		Uptime:      999,
	}
	got, err := EncodeReportV1(snapshot)
	if err != nil {
		t.Fatalf("EncodeReportV1 failed: %v", err)
	}
	want := `{"connections":{"tcp":12,"udp":3},"cpu":{"usage":12.5},"disk":{"total":1000,"used":500},"load":{"load1":1.1,"load15":1.3,"load5":1.2},"message":"","network":{"down":200,"totalDown":2000,"totalUp":1000,"up":100},"process":42,"ram":{"total":8000,"used":4000},"swap":{"total":2000,"used":100},"uptime":999}`
	if string(got) != want {
		t.Fatalf("encoded report mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestEncodeReportV1GoldenGPUFallbackAndErrors(t *testing.T) {
	snapshot := ReportSnapshot{
		CPU: CPUReport{Usage: 0.001},
		GPU: &GPUReport{Models: []string{"Fixture GPU"}},
		Metadata: ReportMetadata{
			Network:     SampleStatus{Error: "network fixture"},
			Connections: SampleStatus{Error: "connection fixture"},
			GPU:         SampleStatus{Error: "gpu fixture"},
		},
	}
	got, err := EncodeReportV1(snapshot)
	if err != nil {
		t.Fatalf("EncodeReportV1 failed: %v", err)
	}
	wantMessage := "failed to get network speed: network fixture\\n" +
		"failed to get connections: connection fixture\\n" +
		"failed to get detailed GPU info: gpu fixture\\n"
	want := `{"connections":{"tcp":0,"udp":0},"cpu":{"usage":0.001},"disk":{"total":0,"used":0},"gpu":{"models":["Fixture GPU"]},"load":{"load1":0,"load15":0,"load5":0},"message":"` +
		wantMessage + `","network":{"down":0,"totalDown":0,"totalUp":0,"up":0},"process":0,"ram":{"total":0,"used":0},"swap":{"total":0,"used":0},"uptime":0}`
	if string(got) != want {
		t.Fatalf("encoded report mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestEncodeReportV1DetailedEmptyGPUIsValid(t *testing.T) {
	snapshot := ReportSnapshot{
		CPU: CPUReport{Usage: 0.001},
		GPU: &GPUReport{Detailed: true, DetailedInfo: []GPUDeviceReport{}},
	}
	got, err := EncodeReportV1(snapshot)
	if err != nil {
		t.Fatalf("EncodeReportV1 failed: %v", err)
	}
	if !strings.Contains(string(got), `"gpu":{"average_usage":0,"count":0,"detailed_info":[]}`) {
		t.Fatalf("empty detailed GPU report is not v1-compatible: %s", got)
	}
}

func TestReportSnapshotStoreIsImmutableAndMarksStale(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	store := newReportSnapshotStore(func() time.Time { return current })
	devices := []GPUDeviceReport{{Name: "fixture", Utilization: 50}}
	models := []string{"fixture"}
	store.publish(reportSampleGPU, 3*time.Second, nil, func(snapshot *ReportSnapshot) {
		snapshot.GPU = &GPUReport{Count: 1, DetailedInfo: devices, Models: models}
	})

	devices[0].Name = "mutated-source"
	models[0] = "mutated-source"
	first := store.snapshot()
	if first.GPU.DetailedInfo[0].Name != "fixture" || first.GPU.Models[0] != "fixture" {
		t.Fatalf("published snapshot retained mutable source: %+v", first.GPU)
	}
	first.GPU.DetailedInfo[0].Name = "mutated-reader"
	first.GPU.Models[0] = "mutated-reader"
	second := store.snapshot()
	if second.GPU.DetailedInfo[0].Name != "fixture" || second.GPU.Models[0] != "fixture" {
		t.Fatalf("returned snapshot mutated store: %+v", second.GPU)
	}
	if second.Metadata.GPU.Stale {
		t.Fatal("new GPU sample is stale")
	}

	current = current.Add(3*time.Second + time.Nanosecond)
	if got := store.snapshot(); !got.Metadata.GPU.Stale {
		t.Fatal("expired GPU sample was not marked stale")
	}
	store.publish(reportSampleGPU, 3*time.Second, errors.New("fixture unavailable"), nil)
	failed := store.snapshot()
	if failed.GPU.DetailedInfo[0].Name != "fixture" || failed.Metadata.GPU.Error == "" {
		t.Fatalf("failed sample did not preserve stale value: %+v", failed)
	}
}

func TestEncodeReportV1SanitizesNonFiniteValuesWithoutMutation(t *testing.T) {
	snapshot := ReportSnapshot{
		CPU:  CPUReport{Usage: math.NaN()},
		Load: LoadReport{Load1: math.Inf(1), Load5: math.Inf(-1), Load15: math.NaN()},
		GPU: &GPUReport{
			AverageUsage: math.NaN(),
			Count:        1,
			DetailedInfo: []GPUDeviceReport{{Name: "fixture", Utilization: math.Inf(1)}},
			Detailed:     true,
		},
	}
	got, err := EncodeReportV1(snapshot)
	if err != nil {
		t.Fatalf("EncodeReportV1 failed: %v", err)
	}
	if strings.Contains(string(got), "NaN") || strings.Contains(string(got), "Inf") {
		t.Fatalf("non-finite value escaped sanitization: %s", got)
	}
	if !math.IsNaN(snapshot.CPU.Usage) || !math.IsNaN(snapshot.GPU.AverageUsage) {
		t.Fatal("encoder mutated caller snapshot")
	}
}

func TestReportSnapshotStoreConcurrentPublishAndEncode(t *testing.T) {
	store := newReportSnapshotStore(time.Now)
	const iterations = 1_000
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		for index := range iterations {
			store.publish(reportSampleCPU, time.Minute, nil, func(snapshot *ReportSnapshot) {
				snapshot.CPU.Usage = float64(index)
			})
		}
	}()
	go func() {
		defer wait.Done()
		for range iterations {
			store.publish(reportSampleGPU, time.Minute, nil, func(snapshot *ReportSnapshot) {
				snapshot.GPU = &GPUReport{Count: 1, Models: []string{"fixture"}}
			})
		}
	}()
	go func() {
		defer wait.Done()
		for range iterations {
			if _, err := encodeReportV1(store.load()); err != nil {
				t.Errorf("encodeReportV1 failed: %v", err)
			}
		}
	}()
	wait.Wait()
}
