package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	maximumPingResultsPerBatch = 32
	pingResultFlushInterval    = 500 * time.Millisecond
)

type pingBatchResult struct {
	TaskID     uint      `json:"task_id"`
	Value      int       `json:"value"`
	PingType   string    `json:"ping_type"`
	FinishedAt time.Time `json:"finished_at"`
}

type pingResultBatchMessage struct {
	Type     string            `json:"type"`
	Sequence uint64            `json:"sequence"`
	Results  []pingBatchResult `json:"results"`
}

// pingResultBatcher turns concurrent one-result callbacks into bounded,
// durable batches. A batch is journaled before it enters the WebSocket queue.
type pingResultBatcher struct {
	mu      sync.Mutex
	writer  pingResultWriter
	spool   *telemetrySpool
	now     func() time.Time
	next    uint64
	results []pingBatchResult
	stop    chan struct{}
	done    chan struct{}
	closed  bool
}

func newPingResultBatcher(writer pingResultWriter, path string, now func() time.Time) (*pingResultBatcher, error) {
	if writer == nil {
		return nil, errors.New("ping batch writer is nil")
	}
	if now == nil {
		now = time.Now
	}
	spool, err := openTelemetrySpool(path, now)
	if err != nil {
		return nil, err
	}
	batcher := &pingResultBatcher{
		writer: writer,
		spool:  spool,
		now:    now,
		next:   spool.NextSequence(),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if err := batcher.enqueuePending(); err != nil {
		_ = spool.Close()
		return nil, err
	}
	go batcher.run()
	return batcher, nil
}

func newDefaultPingResultBatcher(writer pingResultWriter) (*pingResultBatcher, error) {
	path, err := defaultTelemetrySpoolPath()
	if err != nil {
		return nil, err
	}
	return newPingResultBatcher(writer, path+".ping", time.Now)
}

func (batcher *pingResultBatcher) WriteJSON(value interface{}) error {
	result, err := decodePingBatchResult(value)
	if err != nil {
		return err
	}
	batcher.mu.Lock()
	defer batcher.mu.Unlock()
	if batcher.closed {
		return errors.New("ping result batcher is closed")
	}
	batcher.results = append(batcher.results, result)
	if len(batcher.results) >= maximumPingResultsPerBatch {
		return batcher.flushLocked()
	}
	return nil
}

func decodePingBatchResult(value interface{}) (pingBatchResult, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return pingBatchResult{}, err
	}
	var envelope struct {
		Type string `json:"type"`
		pingBatchResult
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type != "ping_result" || envelope.TaskID == 0 || envelope.FinishedAt.IsZero() {
		return pingBatchResult{}, errors.New("invalid ping result")
	}
	return envelope.pingBatchResult, nil
}

func (batcher *pingResultBatcher) run() {
	ticker := time.NewTicker(pingResultFlushInterval)
	defer ticker.Stop()
	defer close(batcher.done)
	for {
		select {
		case <-ticker.C:
			batcher.mu.Lock()
			if !batcher.closed {
				_ = batcher.flushLocked()
			}
			batcher.mu.Unlock()
		case <-batcher.stop:
			return
		}
	}
}

func (batcher *pingResultBatcher) flushLocked() error {
	if len(batcher.results) == 0 {
		return nil
	}
	count := min(len(batcher.results), maximumPingResultsPerBatch)
	results := append([]pingBatchResult(nil), batcher.results[:count]...)
	message := pingResultBatchMessage{Type: "ping_result_batch", Sequence: batcher.next, Results: results}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	createdAt := batcher.now()
	if err := batcher.spool.Add(batcher.next, payload, createdAt); err != nil {
		return fmt.Errorf("persist ping result batch: %w", err)
	}
	batcher.next++
	copy(batcher.results, batcher.results[count:])
	batcher.results = batcher.results[:len(batcher.results)-count]
	if err := batcher.writer.WriteJSON(json.RawMessage(payload)); err != nil {
		return fmt.Errorf("enqueue ping result batch: %w", err)
	}
	if len(batcher.results) > 0 {
		return batcher.flushLocked()
	}
	return nil
}

func (batcher *pingResultBatcher) enqueuePending() error {
	if queue, ok := batcher.writer.(*outboundQueue); ok {
		queue.RemovePingBatchReliable()
	}
	for _, frame := range batcher.spool.Pending() {
		if err := batcher.writer.WriteJSON(json.RawMessage(frame.Payload)); err != nil {
			return err
		}
	}
	return nil
}

func (batcher *pingResultBatcher) HandleControl(message []byte) bool {
	var control struct {
		Type     string `json:"type"`
		Through  uint64 `json:"through"`
		Expected uint64 `json:"expected"`
	}
	if err := json.Unmarshal(message, &control); err != nil {
		return false
	}
	switch control.Type {
	case "ping_result_ack":
		if control.Through > 0 {
			acknowledged := false
			batcher.mu.Lock()
			if err := batcher.spool.Ack(control.Through); err != nil {
				log.Printf("Failed to persist Ping acknowledgement through %d: %v", control.Through, err)
			} else {
				batcher.next = max(batcher.next, control.Through+1)
				acknowledged = true
			}
			batcher.mu.Unlock()
			if acknowledged {
				if err := batcher.enqueuePending(); err != nil {
					log.Printf("Failed to reconcile Ping queue after acknowledgement: %v", err)
				}
			}
		}
		return true
	case "ping_result_nack":
		_ = batcher.enqueuePending()
		return true
	default:
		return false
	}
}

func (batcher *pingResultBatcher) Close() error {
	batcher.mu.Lock()
	if batcher.closed {
		batcher.mu.Unlock()
		return nil
	}
	flushErr := batcher.flushLocked()
	batcher.closed = true
	close(batcher.stop)
	batcher.mu.Unlock()
	<-batcher.done
	return errors.Join(flushErr, batcher.spool.Close())
}
