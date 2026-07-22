package diagnostics

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDisabledDiagnosticsDoNotRecord(t *testing.T) {
	resetForTest()
	SetEnabled(false)
	ObserveReport(time.Now().Add(-time.Second), 128, nil)
	if snapshot := CurrentSnapshot(); snapshot.Report.Count != 0 || snapshot.ReportBytes != 0 {
		t.Fatalf("disabled diagnostics recorded data: %+v", snapshot)
	}
}

func TestDiagnosticsSnapshotContainsOnlyAggregateData(t *testing.T) {
	resetForTest()
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	ObserveSampler(SamplerCPU, time.Now().Add(-time.Millisecond), nil)
	ObserveReport(time.Now().Add(-time.Millisecond), 256, nil)
	RecordPingRejected()
	SetQueueDepth(3, 2)
	RecordTelemetryMerged()
	RecordControlQueueRetry()
	RecordControlQueueDrop()
	RecordQueueDrainTimeout()
	RecordWebSocketConnected()

	snapshot := CurrentSnapshot()
	if snapshot.Samplers["cpu"].Count != 1 || snapshot.Report.Count != 1 || snapshot.ReportBytes != 256 {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.Queue.TelemetryDepth != 3 || snapshot.Queue.ControlDepth != 2 ||
		snapshot.Queue.TelemetryMerged != 1 || snapshot.Queue.ControlRetries != 1 ||
		snapshot.Queue.ControlDrops != 1 || snapshot.Queue.DrainTimeouts != 1 {
		t.Fatalf("unexpected queue diagnostics: %+v", snapshot.Queue)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	for _, sensitive := range []string{"token", "authorization", "command", "endpoint", "target", "ip_address"} {
		if strings.Contains(strings.ToLower(string(encoded)), sensitive) {
			t.Fatalf("diagnostics schema contains sensitive field %q: %s", sensitive, encoded)
		}
	}
}

func TestDiagnosticsAreSafeForConcurrentWriters(t *testing.T) {
	resetForTest()
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })

	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 1_000 {
				ObserveSampler(SamplerNetwork, time.Now(), nil)
				RecordWebSocketMessageSent()
				_ = CurrentSnapshot()
			}
		}()
	}
	workers.Wait()
	if count := CurrentSnapshot().Samplers["network"].Count; count != 32_000 {
		t.Fatalf("unexpected concurrent sample count %d", count)
	}
}

func TestTimeoutIsCounted(t *testing.T) {
	resetForTest()
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	ObserveHTTP(time.Now(), context.DeadlineExceeded)
	if snapshot := CurrentSnapshot().HTTP; snapshot.Errors != 1 || snapshot.Timeouts != 1 {
		t.Fatalf("unexpected timeout snapshot: %+v", snapshot)
	}
}
