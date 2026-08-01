package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	payload, err := monitoring.EncodeReportV3(delivery.aggregator, sequence, sampledAt, forceCheckpoint)
	if err != nil {
		return spooledFrame{}, err
	}
	if err := delivery.spool.Add(sequence, payload, sampledAt); err != nil {
		return spooledFrame{}, fmt.Errorf("persist telemetry frame: %w", err)
	}
	delivery.next++
	return spooledFrame{Sequence: sequence, CreatedAt: sampledAt, Payload: payload}, nil
}

func (delivery *telemetryDelivery) Pending() []spooledFrame { return delivery.spool.Pending() }

func (delivery *telemetryDelivery) HandleControl(message []byte) bool {
	var ack struct {
		Type    string `json:"type"`
		Through uint64 `json:"through"`
	}
	if err := json.Unmarshal(message, &ack); err != nil || ack.Type != "telemetry_ack" {
		return false
	}
	if ack.Through == 0 {
		return true
	}
	_ = delivery.spool.Ack(ack.Through)
	return true
}

func (delivery *telemetryDelivery) Close() error {
	if delivery == nil || delivery.spool == nil {
		return nil
	}
	return delivery.spool.Close()
}

func (delivery *telemetryDelivery) enqueuePending(ctx context.Context, queue *outboundQueue) error {
	queue.RemoveTelemetryV3Reliable()
	for _, frame := range delivery.Pending() {
		if err := queue.EnqueueReliable(ctx, websocket.BinaryMessage, frame.Payload); err != nil {
			return err
		}
	}
	return nil
}
