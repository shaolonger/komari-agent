package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/diagnostics"
)

const (
	defaultReliableQueueCapacity = 128
	defaultReliableWriteAttempts = 3
	defaultReliableBurst         = 8
)

type outboundKind uint8

const (
	outboundReliable outboundKind = iota + 1
	outboundHeartbeat
	outboundTelemetry
)

type outboundFrame struct {
	id          uint64
	kind        outboundKind
	messageType int
	payload     []byte
	attempts    int
}

type outboundQueue struct {
	mu               sync.Mutex
	root             context.Context
	reliableCapacity int
	maximumAttempts  int
	maximumBurst     int
	nextID           uint64
	reliableBurst    int
	reliable         []outboundFrame
	heartbeat        *outboundFrame
	telemetry        *outboundFrame
	closed           bool
	itemsAvailable   chan struct{}
	spaceAvailable   chan struct{}
	closedSignal     chan struct{}
}

func newOutboundQueue(root context.Context, reliableCapacity, maximumAttempts int) *outboundQueue {
	if root == nil {
		root = context.Background()
	}
	if reliableCapacity <= 0 {
		reliableCapacity = defaultReliableQueueCapacity
	}
	if maximumAttempts <= 0 {
		maximumAttempts = defaultReliableWriteAttempts
	}
	return &outboundQueue{
		root:             root,
		reliableCapacity: reliableCapacity,
		maximumAttempts:  maximumAttempts,
		maximumBurst:     defaultReliableBurst,
		reliable:         make([]outboundFrame, 0, reliableCapacity),
		itemsAvailable:   make(chan struct{}, 1),
		spaceAvailable:   make(chan struct{}, 1),
		closedSignal:     make(chan struct{}),
	}
}

// WriteJSON makes the queue a pingResultWriter. Control results use the
// bounded reliable FIFO and can survive a connection-generation replacement.
func (queue *outboundQueue) WriteJSON(value interface{}) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return queue.EnqueueReliable(queue.root, websocket.TextMessage, payload)
}

func (queue *outboundQueue) EnqueueReliable(ctx context.Context, messageType int, payload []byte) error {
	if ctx == nil {
		ctx = queue.root
	}
	for {
		queue.mu.Lock()
		if queue.closed {
			queue.mu.Unlock()
			diagnostics.RecordControlQueueDrop()
			return errors.New("outbound queue is closed")
		}
		if len(queue.reliable) < queue.reliableCapacity {
			frame := queue.newFrameLocked(outboundReliable, messageType, payload)
			queue.reliable = append(queue.reliable, frame)
			queue.updateDepthLocked()
			queue.signalItemsLocked()
			queue.mu.Unlock()
			return nil
		}
		queue.mu.Unlock()

		select {
		case <-ctx.Done():
			diagnostics.RecordControlQueueDrop()
			return ctx.Err()
		case <-queue.root.Done():
			diagnostics.RecordControlQueueDrop()
			return queue.root.Err()
		case <-queue.closedSignal:
			diagnostics.RecordControlQueueDrop()
			return errors.New("outbound queue is closed")
		case <-queue.spaceAvailable:
		}
	}
}

func (queue *outboundQueue) EnqueueTelemetry(messageType int, payload []byte) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.closed {
		return errors.New("outbound queue is closed")
	}
	if queue.telemetry != nil {
		diagnostics.RecordTelemetryMerged()
	}
	frame := queue.newFrameLocked(outboundTelemetry, messageType, payload)
	queue.telemetry = &frame
	queue.updateDepthLocked()
	queue.signalItemsLocked()
	return nil
}

func (queue *outboundQueue) EnqueueHeartbeat() error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.closed {
		return errors.New("outbound queue is closed")
	}
	if queue.heartbeat == nil {
		frame := queue.newFrameLocked(outboundHeartbeat, websocket.PingMessage, nil)
		queue.heartbeat = &frame
		queue.updateDepthLocked()
		queue.signalItemsLocked()
	}
	return nil
}

func (queue *outboundQueue) Take(ctx context.Context) (outboundFrame, error) {
	for {
		queue.mu.Lock()
		if frame, ok := queue.nextLocked(); ok {
			queue.mu.Unlock()
			return frame, nil
		}
		if queue.closed {
			queue.mu.Unlock()
			return outboundFrame{}, io.EOF
		}
		queue.mu.Unlock()

		select {
		case <-ctx.Done():
			return outboundFrame{}, ctx.Err()
		case <-queue.closedSignal:
		case <-queue.itemsAvailable:
		}
	}
}

func (queue *outboundQueue) Ack(frame outboundFrame) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	switch frame.kind {
	case outboundReliable:
		if len(queue.reliable) > 0 && queue.reliable[0].id == frame.id {
			queue.reliable[0].payload = nil
			queue.reliable = queue.reliable[1:]
			queue.signalSpaceLocked()
		}
	case outboundHeartbeat:
		if queue.heartbeat != nil && queue.heartbeat.id == frame.id {
			queue.heartbeat = nil
		}
	case outboundTelemetry:
		if queue.telemetry != nil && queue.telemetry.id == frame.id {
			queue.telemetry = nil
		}
	}
	queue.updateDepthLocked()
	if queue.hasItemsLocked() {
		queue.signalItemsLocked()
	}
}

// Nack retains reliable frames for the next connection generation until their
// explicit attempt limit is exhausted. Ephemeral telemetry/heartbeat is
// discarded; a producer will publish a fresh replacement.
func (queue *outboundQueue) Nack(frame outboundFrame) (exhausted bool) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	switch frame.kind {
	case outboundReliable:
		if len(queue.reliable) > 0 && queue.reliable[0].id == frame.id {
			queue.reliable[0].attempts++
			diagnostics.RecordControlQueueRetry()
			if queue.reliable[0].attempts >= queue.maximumAttempts {
				queue.reliable[0].payload = nil
				queue.reliable = queue.reliable[1:]
				queue.signalSpaceLocked()
				diagnostics.RecordControlQueueDrop()
				exhausted = true
			}
		}
	case outboundHeartbeat:
		if queue.heartbeat != nil && queue.heartbeat.id == frame.id {
			queue.heartbeat = nil
		}
	case outboundTelemetry:
		if queue.telemetry != nil && queue.telemetry.id == frame.id {
			queue.telemetry = nil
		}
	}
	queue.updateDepthLocked()
	if queue.hasItemsLocked() {
		queue.signalItemsLocked()
	}
	return exhausted
}

func (queue *outboundQueue) ResetEphemeral() {
	queue.mu.Lock()
	queue.heartbeat = nil
	queue.telemetry = nil
	queue.reliableBurst = 0
	queue.updateDepthLocked()
	if len(queue.reliable) > 0 {
		queue.signalItemsLocked()
	}
	queue.mu.Unlock()
}

// RemoveTelemetryV3Reliable drops only replayable v3 frames before a new
// connection generation reloads the authoritative durable spool. JSON control
// and Ping results remain in their original reliable FIFO order.
func (queue *outboundQueue) RemoveTelemetryV3Reliable() {
	queue.mu.Lock()
	kept := queue.reliable[:0]
	for index := range queue.reliable {
		frame := queue.reliable[index]
		isV3 := frame.messageType == websocket.BinaryMessage && len(frame.payload) >= 4 && string(frame.payload[:4]) == "KMR3"
		if isV3 {
			frame.payload = nil
			continue
		}
		kept = append(kept, frame)
	}
	queue.reliable = kept
	queue.reliableBurst = 0
	queue.updateDepthLocked()
	queue.signalSpaceLocked()
	if queue.hasItemsLocked() {
		queue.signalItemsLocked()
	}
	queue.mu.Unlock()
}

func (queue *outboundQueue) Close(dropEphemeral bool) {
	queue.mu.Lock()
	if queue.closed {
		queue.mu.Unlock()
		return
	}
	if dropEphemeral {
		queue.heartbeat = nil
		queue.telemetry = nil
	}
	queue.closed = true
	close(queue.closedSignal)
	queue.updateDepthLocked()
	queue.mu.Unlock()
}

func (queue *outboundQueue) Depth() (telemetry, reliable int) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.telemetry != nil {
		telemetry++
	}
	if queue.heartbeat != nil {
		telemetry++
	}
	return telemetry, len(queue.reliable)
}

func (queue *outboundQueue) newFrameLocked(kind outboundKind, messageType int, payload []byte) outboundFrame {
	queue.nextID++
	return outboundFrame{
		id:          queue.nextID,
		kind:        kind,
		messageType: messageType,
		payload:     append([]byte(nil), payload...),
	}
}

func (queue *outboundQueue) nextLocked() (outboundFrame, bool) {
	hasEphemeral := queue.heartbeat != nil || queue.telemetry != nil
	if len(queue.reliable) > 0 && (queue.reliableBurst < queue.maximumBurst || !hasEphemeral) {
		queue.reliableBurst++
		return queue.reliable[0], true
	}
	if queue.heartbeat != nil {
		queue.reliableBurst = 0
		return *queue.heartbeat, true
	}
	if queue.telemetry != nil {
		queue.reliableBurst = 0
		return *queue.telemetry, true
	}
	if len(queue.reliable) > 0 {
		queue.reliableBurst++
		return queue.reliable[0], true
	}
	return outboundFrame{}, false
}

func (queue *outboundQueue) hasItemsLocked() bool {
	return len(queue.reliable) > 0 || queue.heartbeat != nil || queue.telemetry != nil
}

func (queue *outboundQueue) updateDepthLocked() {
	telemetryDepth := 0
	if queue.telemetry != nil {
		telemetryDepth++
	}
	if queue.heartbeat != nil {
		telemetryDepth++
	}
	diagnostics.SetQueueDepth(telemetryDepth, len(queue.reliable))
}

func (queue *outboundQueue) signalItemsLocked() {
	select {
	case queue.itemsAvailable <- struct{}{}:
	default:
	}
}

func (queue *outboundQueue) signalSpaceLocked() {
	select {
	case queue.spaceAvailable <- struct{}{}:
	default:
	}
}
