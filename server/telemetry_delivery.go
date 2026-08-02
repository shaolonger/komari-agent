package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/monitoring"
)

type telemetryDelivery struct {
	mu         sync.Mutex
	spool      *telemetrySpool
	aggregator *monitoring.V3Aggregator
	next       uint64
	queue      *outboundQueue
}

func defaultTelemetrySpoolPath() (string, error) {
	if flags.TelemetrySpoolPath != "" {
		return flags.TelemetrySpoolPath, nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "komari-agent", "telemetry-v3.spool"), nil
}

func newTelemetryDelivery(path string, now func() time.Time) (*telemetryDelivery, error) {
	spool, err := openTelemetrySpool(path, now)
	if err != nil {
		return nil, err
	}
	return &telemetryDelivery{
		spool: spool, aggregator: monitoring.NewV3Aggregator(time.Minute), next: spool.NextSequence(),
	}, nil
}

func (delivery *telemetryDelivery) Sample() error {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	return delivery.aggregator.Add(monitoring.CurrentReportSnapshot())
}

func (delivery *telemetryDelivery) Flush(sampledAt time.Time, forceCheckpoint bool) (spooledFrame, error) {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if delivery.aggregator.PendingSamples() == 0 {
		if err := delivery.aggregator.Add(monitoring.CurrentReportSnapshot()); err != nil {
			return spooledFrame{}, err
		}
	}
	sequence := delivery.next
	prepared, payload, err := monitoring.PrepareReportV3(delivery.aggregator, sequence, sampledAt, forceCheckpoint)
	if err != nil {
		return spooledFrame{}, err
	}
	if err := delivery.spool.Add(sequence, payload, sampledAt); err != nil {
		return spooledFrame{}, fmt.Errorf("persist telemetry frame: %w", err)
	}
	delivery.aggregator.Commit(prepared)
	delivery.next++
	return spooledFrame{Sequence: sequence, CreatedAt: sampledAt, Payload: payload}, nil
}

func (delivery *telemetryDelivery) Pending() []spooledFrame { return delivery.spool.Pending() }

func (delivery *telemetryDelivery) HandleControl(message []byte) bool {
	var control struct {
		Type     string `json:"type"`
		Through  uint64 `json:"through"`
		Expected uint64 `json:"expected"`
	}
	if err := json.Unmarshal(message, &control); err != nil {
		return false
	}
	if control.Type == "telemetry_nack" {
		log.Printf("Server requested telemetry sequence %d; replaying the contiguous durable spool", control.Expected)
		delivery.mu.Lock()
		queue := delivery.queue
		delivery.mu.Unlock()
		if queue != nil {
			if err := delivery.enqueuePending(context.Background(), queue); err != nil {
				log.Printf("Failed to re-enqueue telemetry after sequence NACK: %v", err)
			}
		}
		return true
	}
	if control.Type != "telemetry_ack" {
		return false
	}
	if control.Through == 0 {
		return true
	}
	delivery.mu.Lock()
	if err := delivery.spool.Ack(control.Through); err != nil {
		delivery.mu.Unlock()
		log.Printf("Failed to persist telemetry acknowledgement through %d: %v", control.Through, err)
		return true
	}
	delivery.next = max(delivery.next, control.Through+1)
	queue := delivery.queue
	delivery.mu.Unlock()
	if queue != nil {
		if err := delivery.enqueuePending(context.Background(), queue); err != nil {
			log.Printf("Failed to reconcile telemetry queue after acknowledgement: %v", err)
		}
	}
	return true
}

func (delivery *telemetryDelivery) Close() error {
	if delivery == nil || delivery.spool == nil {
		return nil
	}
	return delivery.spool.Close()
}

func (delivery *telemetryDelivery) enqueuePending(ctx context.Context, queue *outboundQueue) error {
	delivery.mu.Lock()
	delivery.queue = queue
	delivery.mu.Unlock()
	queue.RemoveTelemetryV3Reliable()
	for _, frame := range delivery.Pending() {
		if err := queue.EnqueueReliable(ctx, websocket.BinaryMessage, frame.Payload); err != nil {
			return err
		}
	}
	return nil
}
