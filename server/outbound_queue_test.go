package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOutboundQueueCoalescesTelemetryAndPrioritizesReliableFrames(t *testing.T) {
	queue := newOutboundQueue(context.Background(), 8, 3)
	if err := queue.EnqueueTelemetry(websocket.TextMessage, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueTelemetry(websocket.TextMessage, []byte("latest")); err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueHeartbeat(); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"reliable-1", "reliable-2"} {
		if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}

	want := []struct {
		kind    outboundKind
		payload string
	}{
		{outboundReliable, "reliable-1"},
		{outboundReliable, "reliable-2"},
		{outboundHeartbeat, ""},
		{outboundTelemetry, "latest"},
	}
	for index, expected := range want {
		frame, err := queue.Take(context.Background())
		if err != nil {
			t.Fatalf("Take %d: %v", index, err)
		}
		if frame.kind != expected.kind || string(frame.payload) != expected.payload {
			t.Fatalf("frame %d = kind %d payload %q, want kind %d payload %q", index, frame.kind, frame.payload, expected.kind, expected.payload)
		}
		queue.Ack(frame)
	}
	if telemetry, reliable := queue.Depth(); telemetry != 0 || reliable != 0 {
		t.Fatalf("queue depth = %d/%d, want 0/0", telemetry, reliable)
	}
}

func TestOutboundQueueFairnessBoundsReliableBurst(t *testing.T) {
	queue := newOutboundQueue(context.Background(), 16, 3)
	for index := range 10 {
		if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte(fmt.Sprintf("reliable-%d", index))); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.EnqueueTelemetry(websocket.TextMessage, []byte("telemetry")); err != nil {
		t.Fatal(err)
	}
	for index := range defaultReliableBurst {
		frame, err := queue.Take(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if frame.kind != outboundReliable {
			t.Fatalf("frame %d kind = %d, want reliable", index, frame.kind)
		}
		queue.Ack(frame)
	}
	frame, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frame.kind != outboundTelemetry || string(frame.payload) != "telemetry" {
		t.Fatalf("fairness frame = kind %d payload %q", frame.kind, frame.payload)
	}
}

func TestOutboundQueueReliableCapacityBlocksAndHonorsCancellation(t *testing.T) {
	queue := newOutboundQueue(context.Background(), 1, 3)
	if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte("first")); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		finished <- queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte("second"))
	}()
	<-started
	select {
	case err := <-finished:
		t.Fatalf("bounded enqueue returned before capacity was available: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	first, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	queue.Ack(first)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("blocked enqueue failed after capacity became available: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue did not resume after acknowledgement")
	}

	cancelContext, cancel := context.WithCancel(context.Background())
	cancel()
	err = queue.EnqueueReliable(cancelContext, websocket.TextMessage, []byte("third"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled enqueue error = %v", err)
	}
}

func TestOutboundQueueNackRetainsReliableFIFOUntilAttemptLimit(t *testing.T) {
	queue := newOutboundQueue(context.Background(), 2, 2)
	for _, payload := range []string{"first", "second"} {
		if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}

	first, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exhausted := queue.Nack(first); exhausted {
		t.Fatal("first failed attempt unexpectedly exhausted reliable frame")
	}
	retry, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if retry.id != first.id || string(retry.payload) != "first" {
		t.Fatalf("retry = id %d payload %q, want id %d payload first", retry.id, retry.payload, first.id)
	}
	if exhausted := queue.Nack(retry); !exhausted {
		t.Fatal("second failed attempt did not exhaust reliable frame")
	}
	next, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(next.payload) != "second" {
		t.Fatalf("next FIFO payload = %q, want second", next.payload)
	}
}

func TestOutboundQueueCloseDropsEphemeralDrainsReliableAndUnblocksProducers(t *testing.T) {
	queue := newOutboundQueue(context.Background(), 1, 3)
	if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte("drain-me")); err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueTelemetry(websocket.TextMessage, []byte("stale")); err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueHeartbeat(); err != nil {
		t.Fatal(err)
	}

	const blockedProducers = 4
	var producers sync.WaitGroup
	producers.Add(blockedProducers)
	errorsChannel := make(chan error, blockedProducers)
	for index := range blockedProducers {
		go func() {
			defer producers.Done()
			errorsChannel <- queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte(fmt.Sprintf("blocked-%d", index)))
		}()
	}
	queue.Close(true)
	finished := make(chan struct{})
	go func() {
		producers.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock all bounded producers")
	}
	close(errorsChannel)
	for err := range errorsChannel {
		if err == nil || err.Error() != "outbound queue is closed" {
			t.Fatalf("blocked producer error = %v", err)
		}
	}

	if telemetry, reliable := queue.Depth(); telemetry != 0 || reliable != 1 {
		t.Fatalf("closed queue depth = %d/%d, want 0/1", telemetry, reliable)
	}
	frame, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(frame.payload) != "drain-me" {
		t.Fatalf("drained payload = %q", frame.payload)
	}
	queue.Ack(frame)
	if _, err := queue.Take(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("empty closed queue error = %v, want EOF", err)
	}
}

func TestOutboundQueueOwnsPayloadAndQueuesPingResultsReliably(t *testing.T) {
	useServerFlagsSnapshot(t)
	flags.DisableWebSsh = true
	flags.EnableRemoteControl = false
	flags.EnablePing = false

	queue := newOutboundQueue(context.Background(), 2, 3)
	payload := []byte("owned")
	if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, payload); err != nil {
		t.Fatal(err)
	}
	payload[0] = 'X'
	frame, err := queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(frame.payload) != "owned" {
		t.Fatalf("queue retained caller-owned mutation: %q", frame.payload)
	}
	queue.Ack(frame)

	NewPingTask(queue, 42, "tcp", "127.0.0.1:80")
	frame, err = queue.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frame.kind != outboundReliable || frame.messageType != websocket.TextMessage {
		t.Fatalf("ping result frame = kind %d message type %d", frame.kind, frame.messageType)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(frame.payload, &result); err != nil {
		t.Fatalf("decode ping result: %v", err)
	}
	if result["type"] != "ping_result" || result["task_id"] != float64(42) || result["value"] != float64(-1) {
		t.Fatalf("ping result payload = %#v", result)
	}
}

func TestOutboundQueueConcurrentProducersAndConsumer(t *testing.T) {
	const producerCount = 8
	const framesPerProducer = 100
	wantReliable := int64(producerCount * framesPerProducer)
	queue := newOutboundQueue(context.Background(), 32, 3)

	consumerDone := make(chan error, 1)
	var consumed atomic.Int64
	go func() {
		for {
			frame, err := queue.Take(context.Background())
			if errors.Is(err, io.EOF) {
				consumerDone <- nil
				return
			}
			if err != nil {
				consumerDone <- err
				return
			}
			if frame.kind == outboundReliable {
				consumed.Add(1)
			}
			queue.Ack(frame)
		}
	}()

	var producers sync.WaitGroup
	producers.Add(producerCount + 1)
	for producer := range producerCount {
		go func() {
			defer producers.Done()
			for frame := range framesPerProducer {
				payload := []byte(fmt.Sprintf("%d/%d", producer, frame))
				if err := queue.EnqueueReliable(context.Background(), websocket.TextMessage, payload); err != nil {
					t.Errorf("EnqueueReliable: %v", err)
					return
				}
			}
		}()
	}
	go func() {
		defer producers.Done()
		for frame := range 10_000 {
			if err := queue.EnqueueTelemetry(websocket.TextMessage, []byte(fmt.Sprintf("telemetry-%d", frame))); err != nil {
				t.Errorf("EnqueueTelemetry: %v", err)
				return
			}
			_ = queue.EnqueueHeartbeat()
		}
	}()
	producers.Wait()
	deadline := time.Now().Add(time.Second)
	for consumed.Load() != wantReliable && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := consumed.Load(); got != wantReliable {
		t.Fatalf("consumed reliable frames = %d, want %d", got, wantReliable)
	}
	queue.Close(true)
	select {
	case err := <-consumerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not exit after queue close")
	}
}
