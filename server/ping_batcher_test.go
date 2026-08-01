package server

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type pingBatchCapture struct {
	mu       sync.Mutex
	messages [][]byte
}

func (capture *pingBatchCapture) WriteJSON(value interface{}) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	capture.mu.Lock()
	capture.messages = append(capture.messages, payload)
	capture.mu.Unlock()
	return nil
}

func (capture *pingBatchCapture) count() int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return len(capture.messages)
}

func TestPingResultBatcherPersistsBatchesBeforeDeliveryAndAcknowledges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ping.spool")
	capture := &pingBatchCapture{}
	batcher, err := newPingResultBatcher(capture, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for taskID := uint(1); taskID <= maximumPingResultsPerBatch; taskID++ {
		if err := batcher.WriteJSON(map[string]interface{}{
			"type": "ping_result", "task_id": taskID, "value": int(taskID), "ping_type": "tcp", "finished_at": time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if capture.count() != 1 {
		t.Fatalf("delivered batches = %d, want 1", capture.count())
	}
	pending := batcher.spool.Pending()
	if len(pending) != 1 || pending[0].Sequence != 1 {
		t.Fatalf("pending = %#v", pending)
	}
	var message pingResultBatchMessage
	if err := json.Unmarshal(pending[0].Payload, &message); err != nil {
		t.Fatal(err)
	}
	if len(message.Results) != maximumPingResultsPerBatch || message.Sequence != 1 {
		t.Fatalf("batch = %#v", message)
	}
	if !batcher.HandleControl([]byte(`{"type":"ping_result_ack","through":1}`)) {
		t.Fatal("ACK was not consumed")
	}
	if len(batcher.spool.Pending()) != 0 {
		t.Fatal("ACK did not remove durable batch")
	}
	if err := batcher.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPingResultBatcherReplaysUnacknowledgedBatchAfterReconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ping.spool")
	first := &pingBatchCapture{}
	batcher, err := newPingResultBatcher(first, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := batcher.WriteJSON(map[string]interface{}{
		"type": "ping_result", "task_id": uint(7), "value": 11, "ping_type": "tcp", "finished_at": time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := batcher.Close(); err != nil {
		t.Fatal(err)
	}
	if first.count() != 1 {
		t.Fatalf("initial writes = %d", first.count())
	}

	second := &pingBatchCapture{}
	reopened, err := newPingResultBatcher(second, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if second.count() != 1 {
		t.Fatalf("replayed writes = %d, want 1", second.count())
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPingResultBatcherRejectsMalformedResults(t *testing.T) {
	batcher, err := newPingResultBatcher(&pingBatchCapture{}, filepath.Join(t.TempDir(), "ping.spool"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer batcher.Close()
	if err := batcher.WriteJSON(map[string]interface{}{"type": "other"}); err == nil {
		t.Fatal("malformed result was accepted")
	}
}
