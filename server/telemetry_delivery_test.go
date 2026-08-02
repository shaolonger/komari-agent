package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv3"
)

func TestTelemetryDeliveryPersistsBeforeSendAndHandlesAck(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	delivery, err := newTelemetryDelivery(filepath.Join(t.TempDir(), "delivery.spool"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	if err := delivery.Sample(); err != nil {
		t.Fatal(err)
	}
	frame, err := delivery.Flush(now, true)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := telemetryv3.Decode(frame.Payload)
	if err != nil || decoded.Sequence != 1 || !decoded.Checkpoint {
		t.Fatalf("decoded frame = %#v, err=%v", decoded, err)
	}
	if len(delivery.Pending()) != 1 {
		t.Fatal("frame was not durable before acknowledgement")
	}
	ack, _ := json.Marshal(map[string]any{"type": "telemetry_ack", "through": 1})
	if !delivery.HandleControl(ack) || len(delivery.Pending()) != 0 {
		t.Fatal("ack did not remove the durable frame")
	}
	if delivery.HandleControl([]byte(`{"message":"ping"}`)) {
		t.Fatal("non-ack control message was consumed")
	}
}

func TestTelemetryDeliveryNackRequeuesDurableFrames(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	delivery, err := newTelemetryDelivery(filepath.Join(t.TempDir(), "delivery.spool"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	if err := delivery.Sample(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := delivery.Flush(now, true); err != nil {
			t.Fatal(err)
		}
	}
	queue := newOutboundQueue(t.Context(), 16, 3)
	defer queue.Close(false)
	if err := delivery.enqueuePending(t.Context(), queue); err != nil {
		t.Fatal(err)
	}
	if !delivery.HandleControl([]byte(`{"type":"telemetry_nack","expected":2}`)) {
		t.Fatal("telemetry NACK was not handled")
	}
	frame, err := queue.Take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := telemetryv3.Decode(frame.payload)
	if err != nil || decoded.Sequence != 2 {
		t.Fatalf("NACK replay started at sequence %#v, err=%v", decoded, err)
	}
	_, reliable := queue.Depth()
	if reliable != 2 {
		t.Fatalf("NACK reliable depth = %d, want sequences 2 and 3", reliable)
	}
}

func TestTelemetryDeliveryPartialAndDuplicateAckNeverRequeuesPendingFrames(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	path := filepath.Join(t.TempDir(), "delivery.spool")
	delivery, err := newTelemetryDelivery(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	for range 2 {
		if _, err := delivery.Flush(now, true); err != nil {
			t.Fatal(err)
		}
	}
	queue := newOutboundQueue(t.Context(), 16, 3)
	defer queue.Close(false)
	if err := delivery.enqueuePending(t.Context(), queue); err != nil {
		t.Fatal(err)
	}
	// Both frames have left the local writer queue, while the server has only
	// made sequence 1 durable. A normal partial ACK must not put sequence 2 back
	// into the queue; only a NACK is a replay instruction.
	for range 2 {
		frame, err := queue.Take(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		queue.Ack(frame)
	}
	ack := []byte(`{"type":"telemetry_ack","through":1,"accepted_through":2}`)
	if !delivery.HandleControl(ack) || !delivery.HandleControl(ack) {
		t.Fatal("telemetry ACK was not handled")
	}
	if _, reliable := queue.Depth(); reliable != 0 {
		t.Fatalf("partial ACK requeued %d reliable frames", reliable)
	}
	pending := delivery.Pending()
	if len(pending) != 1 || pending[0].Sequence != 2 {
		t.Fatalf("partial ACK pending = %#v", pending)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.HandleControl(ack) {
		t.Fatal("duplicate telemetry ACK was not handled")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() {
		t.Fatalf("duplicate ACK grew spool from %d to %d bytes", info.Size(), after.Size())
	}
}

func TestTelemetryDeliveryNackReplayStopsWithConnectionGeneration(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	delivery, err := newTelemetryDelivery(filepath.Join(t.TempDir(), "delivery.spool"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	if _, err := delivery.Flush(now, true); err != nil {
		t.Fatal(err)
	}
	queue := newOutboundQueue(t.Context(), 1, 3)
	defer queue.Close(false)
	if err := queue.EnqueueReliable(t.Context(), websocket.TextMessage, []byte("control-result")); err != nil {
		t.Fatal(err)
	}
	delivery.mu.Lock()
	delivery.queue = queue
	delivery.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if !delivery.HandleControlContext(ctx, []byte(`{"type":"telemetry_nack","expected":1}`)) {
		t.Fatal("telemetry NACK was not handled")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled NACK replay blocked for %s", elapsed)
	}
}

func TestTelemetryDeliveryRetainsAggregateWhenSpoolWriteFails(t *testing.T) {
	now := time.Unix(1_710_000_000, 0).UTC()
	delivery, err := newTelemetryDelivery(filepath.Join(t.TempDir(), "delivery-full.spool"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	if err := delivery.Sample(); err != nil {
		t.Fatal(err)
	}
	delivery.spool.mu.Lock()
	delivery.spool.payloadBytes = spoolMaximumBytes
	delivery.spool.mu.Unlock()
	if _, err := delivery.Flush(now, true); !errors.Is(err, ErrTelemetrySpoolFull) {
		t.Fatalf("flush error = %v, want spool full", err)
	}
	if got := delivery.aggregator.PendingSamples(); got != 1 {
		t.Fatalf("pending samples after failed durable write = %d, want 1", got)
	}
	if delivery.next != 1 {
		t.Fatalf("next sequence after failed durable write = %d, want 1", delivery.next)
	}
}

func TestTelemetryV3ProducerReducesNetworkCadenceButRetainsSamples(t *testing.T) {
	delivery, err := newTelemetryDelivery(filepath.Join(t.TempDir(), "adaptive.spool"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 38*time.Millisecond)
	defer cancel()
	queue := newOutboundQueue(ctx, 16, 3)
	config := telemetryGenerationConfig{
		reportInterval: 5 * time.Millisecond,
		v3SendInterval: 15 * time.Millisecond,
		now:            time.Now,
		queue:          queue,
		delivery:       delivery,
	}
	if err := produceTelemetryV3(ctx, config); err == nil {
		t.Fatal("producer did not stop with its context")
	}
	pending := delivery.Pending()
	if len(pending) < 2 || len(pending) > 4 {
		t.Fatalf("network frames = %d for ~7 local samples", len(pending))
	}
	totalSamples := uint32(0)
	for _, item := range pending {
		frame, err := telemetryv3.Decode(item.Payload)
		if err != nil {
			t.Fatal(err)
		}
		totalSamples += frame.Envelope.Count
	}
	if totalSamples < uint32(len(pending))*2-1 {
		t.Fatalf("aggregate sample count = %d across %d frames", totalSamples, len(pending))
	}
}

func TestOutboundQueueV3ReplayReplacementPreservesControlResults(t *testing.T) {
	queue := newOutboundQueue(t.Context(), 8, 3)
	defer queue.Close(true)
	if err := queue.EnqueueReliable(t.Context(), 1, []byte("control")); err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueReliable(t.Context(), 2, []byte("KMR3-old")); err != nil {
		t.Fatal(err)
	}
	queue.RemoveTelemetryV3Reliable()
	_, reliable := queue.Depth()
	if reliable != 1 {
		t.Fatalf("reliable depth = %d", reliable)
	}
	frame, err := queue.Take(t.Context())
	if err != nil || string(frame.payload) != "control" {
		t.Fatalf("remaining frame = %#v, err=%v", frame, err)
	}
}
