package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/ws"
)

func TestTelemetryGenerationSendsImmediatelyAndReadFailureCancelsAllWorkers(t *testing.T) {
	session := newFakeTelemetrySession()
	config := testTelemetryGenerationConfig()
	finished := make(chan error, 1)
	go func() {
		finished <- runTelemetryGeneration(context.Background(), session, telemetryProtocolV1, config)
	}()

	write := waitFakeTelemetryWrite(t, session.writes)
	if write.messageType != websocket.TextMessage || string(write.payload) != "fixture-report" {
		t.Fatalf("first write = %+v", write)
	}
	session.reads <- fakeTelemetryRead{err: errors.New("fixture disconnect")}
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "fixture disconnect") {
			t.Fatalf("generation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read failure did not end generation immediately")
	}
	if session.closeCalls.Load() != 1 {
		t.Fatalf("Close calls = %d, want 1", session.closeCalls.Load())
	}
}

func TestTelemetryGenerationHeartbeatAndCancellation(t *testing.T) {
	session := newFakeTelemetrySession()
	config := testTelemetryGenerationConfig()
	config.heartbeatInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- runTelemetryGeneration(ctx, session, telemetryProtocolV1, config)
	}()

	_ = waitFakeTelemetryWrite(t, session.writes) // immediate report
	heartbeat := waitFakeTelemetryWrite(t, session.writes)
	if heartbeat.messageType != websocket.PingMessage || len(heartbeat.payload) != 0 {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("generation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop generation")
	}
}

func TestTelemetryGenerationGracefulShutdownDrainsReliableQueue(t *testing.T) {
	session := newFakeTelemetrySession()
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var startedOnce sync.Once
	session.writeHook = func(deadline time.Time, messageType int, payload []byte) error {
		startedOnce.Do(func() { close(writeStarted) })
		<-releaseWrite
		session.writes <- fakeTelemetryWrite{
			deadline:    deadline,
			messageType: messageType,
			payload:     append([]byte(nil), payload...),
		}
		return nil
	}
	config := testTelemetryGenerationConfig()
	if err := config.queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte("reliable-result")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runTelemetryGeneration(ctx, session, telemetryProtocolV1, config) }()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("reliable drain write did not start")
	}
	cancel()
	select {
	case err := <-finished:
		t.Fatalf("generation returned before in-flight reliable write drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseWrite)
	write := waitFakeTelemetryWrite(t, session.writes)
	if string(write.payload) != "reliable-result" {
		t.Fatalf("drained payload = %q", write.payload)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("generation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("generation did not finish after reliable drain")
	}
	if telemetry, reliable := config.queue.Depth(); telemetry != 0 || reliable != 0 {
		t.Fatalf("drained queue depth = %d/%d, want 0/0", telemetry, reliable)
	}
}

func TestTelemetryGenerationShutdownDrainTimeoutIsBounded(t *testing.T) {
	session := newFakeTelemetrySession()
	writeStarted := make(chan struct{})
	var startedOnce sync.Once
	session.writeHook = func(time.Time, int, []byte) error {
		startedOnce.Do(func() { close(writeStarted) })
		<-session.closed
		return errors.New("fixture writer closed")
	}
	config := testTelemetryGenerationConfig()
	config.drainTimeout = 20 * time.Millisecond
	if err := config.queue.EnqueueReliable(context.Background(), websocket.TextMessage, []byte("bounded-result")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runTelemetryGeneration(ctx, session, telemetryProtocolV1, config) }()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("reliable write did not start")
	}
	started := time.Now()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("generation error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("drain timeout did not bound shutdown")
	}
	if elapsed := time.Since(started); elapsed < config.drainTimeout || elapsed > 200*time.Millisecond {
		t.Fatalf("bounded drain elapsed = %s", elapsed)
	}
	if _, reliable := config.queue.Depth(); reliable != 1 {
		t.Fatalf("failed in-flight reliable frame depth = %d, want retained frame", reliable)
	}
}

func TestTelemetryGenerationRepeatedCancellationJoinsWorkers(t *testing.T) {
	for iteration := range 50 {
		session := newFakeTelemetrySession()
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			finished <- runTelemetryGeneration(ctx, session, telemetryProtocolV1, testTelemetryGenerationConfig())
		}()
		_ = waitFakeTelemetryWrite(t, session.writes)
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration %d error = %v", iteration, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d workers did not join", iteration)
		}
		if session.closeCalls.Load() != 1 {
			t.Fatalf("iteration %d close calls = %d", iteration, session.closeCalls.Load())
		}
	}
}

func TestTelemetryGenerationWriteDeadlineBoundsSlowWriter(t *testing.T) {
	session := newFakeTelemetrySession()
	session.writeHook = func(deadline time.Time, _ int, _ []byte) error {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-timer.C:
			return errors.New("fixture write deadline")
		case <-session.closed:
			return errors.New("fixture closed")
		}
	}
	config := testTelemetryGenerationConfig()
	config.writeTimeout = 20 * time.Millisecond
	started := time.Now()
	err := runTelemetryGeneration(context.Background(), session, telemetryProtocolV1, config)
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("generation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("slow writer was not bounded: %s", elapsed)
	}
}

func TestTelemetryGenerationRejectsBinaryControlFrames(t *testing.T) {
	session := newFakeTelemetrySession()
	session.reads <- fakeTelemetryRead{messageType: websocket.BinaryMessage, payload: []byte("unexpected")}
	err := runTelemetryGeneration(context.Background(), session, telemetryProtocolV1, testTelemetryGenerationConfig())
	if err == nil || !strings.Contains(err.Error(), "unsupported control") {
		t.Fatalf("generation error = %v", err)
	}
}

func TestTelemetryRunnerReconnectsImmediatelyWithGenerationIsolation(t *testing.T) {
	first := newFakeTelemetrySession()
	first.reads <- fakeTelemetryRead{err: errors.New("first generation disconnected")}
	second := newFakeTelemetrySession()
	var connectCalls atomic.Int32
	runner := &telemetryRunner{
		endpoint:         "ws://fixture",
		generation:       testTelemetryGenerationConfig(),
		maxRetries:       0,
		reconnectBase:    time.Second,
		reconnectMaximum: time.Minute,
		stableThreshold:  time.Minute,
		fullJitter:       func(time.Duration) time.Duration { return 0 },
		wait:             waitForContext,
		connect: func(context.Context, string) (telemetrySession, telemetryProtocol, error) {
			switch connectCalls.Add(1) {
			case 1:
				return first, telemetryProtocolV1, nil
			case 2:
				return second, telemetryProtocolV2, nil
			default:
				return nil, telemetryProtocolV1, errors.New("unexpected extra connection")
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(ctx) }()
	waitAtomicInt32(t, &connectCalls, 2)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runner error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	if first.closeCalls.Load() != 1 || second.closeCalls.Load() != 1 {
		t.Fatalf("generation closes = %d/%d, want 1/1", first.closeCalls.Load(), second.closeCalls.Load())
	}
}

func TestTelemetryRunnerFallsBackWhenV3SpoolIsUnavailable(t *testing.T) {
	v3 := newFakeTelemetrySession()
	legacy := newFakeTelemetrySession()
	var fallbackCalls atomic.Int32
	runner := &telemetryRunner{
		endpoint:         "ws://fixture",
		generation:       testTelemetryGenerationConfig(),
		maxRetries:       0,
		reconnectBase:    time.Second,
		reconnectMaximum: time.Minute,
		stableThreshold:  time.Minute,
		fullJitter:       func(time.Duration) time.Duration { return 0 },
		wait:             waitForContext,
		connect: func(context.Context, string) (telemetrySession, telemetryProtocol, error) {
			return v3, telemetryProtocolV3, nil
		},
		connectWithoutV3: func(context.Context, string) (telemetrySession, telemetryProtocol, error) {
			fallbackCalls.Add(1)
			return legacy, telemetryProtocolV1, nil
		},
		deliveryFactory: func() (*telemetryDelivery, error) {
			return nil, errors.New("read-only fixture")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(ctx) }()
	waitAtomicInt32(t, &fallbackCalls, 1)
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("runner error = %v", err)
	}
	if v3.closeCalls.Load() != 1 || legacy.closeCalls.Load() != 1 {
		t.Fatalf("connection closes = v3:%d legacy:%d", v3.closeCalls.Load(), legacy.closeCalls.Load())
	}
}

func TestTelemetryRunnerConnectionBackoffDoublesCapsAndHonorsRetryLimit(t *testing.T) {
	var connectCalls atomic.Int32
	var delays []time.Duration
	runner := &telemetryRunner{
		endpoint:         "ws://fixture",
		generation:       testTelemetryGenerationConfig(),
		maxRetries:       3,
		reconnectBase:    time.Second,
		reconnectMaximum: 3 * time.Second,
		stableThreshold:  time.Minute,
		fullJitter:       func(maximum time.Duration) time.Duration { return maximum },
		wait: func(_ context.Context, delay time.Duration) bool {
			delays = append(delays, delay)
			return true
		},
		connect: func(context.Context, string) (telemetrySession, telemetryProtocol, error) {
			connectCalls.Add(1)
			return nil, telemetryProtocolV1, errors.New("fixture dial failure")
		},
	}
	err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("runner error = %v", err)
	}
	if connectCalls.Load() != 4 {
		t.Fatalf("connect calls = %d, want initial + 3 retries", connectCalls.Load())
	}
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}
	if fmt.Sprint(delays) != fmt.Sprint(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
}

func TestTelemetryRunnerAuthenticationRejectionUsesFixedCooldownWithoutExiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var connectCalls atomic.Int32
	var delay time.Duration
	runner := &telemetryRunner{
		endpoint:         "ws://fixture",
		generation:       testTelemetryGenerationConfig(),
		maxRetries:       0,
		reconnectBase:    time.Second,
		reconnectMaximum: time.Minute,
		stableThreshold:  time.Minute,
		fullJitter:       func(time.Duration) time.Duration { return 0 },
		wait: func(_ context.Context, got time.Duration) bool {
			delay = got
			cancel()
			return false
		},
		connect: func(context.Context, string) (telemetrySession, telemetryProtocol, error) {
			connectCalls.Add(1)
			return nil, telemetryProtocolV1, &clientHTTPStatusError{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"}
		},
	}
	err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runner error = %v, want context canceled", err)
	}
	if connectCalls.Load() != 1 || delay != authenticationRetryInterval {
		t.Fatalf("authentication retry calls=%d delay=%s", connectCalls.Load(), delay)
	}
}

func TestTelemetryConnectorClassifiesHTTPAuthenticationRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "sensitive rejection detail", http.StatusUnauthorized)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	_, _, err := connectTelemetryWebSocketWithDialer(t.Context(), endpoint, websocket.DefaultDialer)
	if !isAuthenticationRejection(err) {
		t.Fatalf("connector error = %v, want authentication rejection", err)
	}
	if strings.Contains(err.Error(), "sensitive rejection detail") {
		t.Fatalf("connector leaked response body: %v", err)
	}
}

func TestTelemetryRunnerBacksOffRepeatedShortGenerations(t *testing.T) {
	var connectCalls atomic.Int32
	var delays []time.Duration
	blocking := newFakeTelemetrySession()
	runner := &telemetryRunner{
		endpoint:         "ws://fixture",
		generation:       testTelemetryGenerationConfig(),
		maxRetries:       0,
		reconnectBase:    time.Second,
		reconnectMaximum: 10 * time.Second,
		stableThreshold:  time.Minute,
		fullJitter:       func(maximum time.Duration) time.Duration { return maximum },
		wait: func(_ context.Context, delay time.Duration) bool {
			delays = append(delays, delay)
			return true
		},
		connect: func(context.Context, string) (telemetrySession, telemetryProtocol, error) {
			call := connectCalls.Add(1)
			if call <= 3 {
				session := newFakeTelemetrySession()
				session.reads <- fakeTelemetryRead{err: errors.New("short generation")}
				return session, telemetryProtocolV1, nil
			}
			return blocking, telemetryProtocolV1, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(ctx) }()
	waitAtomicInt32(t, &connectCalls, 4)
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("runner error = %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second}
	if fmt.Sprint(delays) != fmt.Sprint(want) {
		t.Fatalf("short-generation delays = %v, want %v", delays, want)
	}
}

func TestExponentialFullJitterBounds(t *testing.T) {
	for attempt, wantCeiling := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second} {
		got := exponentialFullJitter(time.Second, 5*time.Second, attempt, func(maximum time.Duration) time.Duration {
			if maximum != wantCeiling {
				t.Fatalf("attempt %d ceiling = %s, want %s", attempt, maximum, wantCeiling)
			}
			return maximum / 2
		})
		if got != wantCeiling/2 {
			t.Fatalf("attempt %d jitter = %s", attempt, got)
		}
	}
	for range 100 {
		if got := randomFullJitter(time.Millisecond); got < 0 || got > time.Millisecond {
			t.Fatalf("random jitter out of range: %s", got)
		}
	}
}

func TestTelemetryGenerationReadLimitRejectsOversizedMessage(t *testing.T) {
	session, closeServer := newLocalTelemetrySession(t, func(connection *websocket.Conn) {
		_ = connection.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 2_048)))
	})
	defer closeServer()
	config := testTelemetryGenerationConfig()
	config.readLimit = 1_024
	err := runTelemetryGeneration(context.Background(), session, telemetryProtocolV1, config)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "read limit") {
		t.Fatalf("oversized message error = %v", err)
	}
}

func TestTelemetryGenerationHalfOpenTimesOutWithoutPong(t *testing.T) {
	serverDone := make(chan struct{})
	session, closeServer := newLocalTelemetrySession(t, func(*websocket.Conn) {
		<-serverDone // Deliberately never read Ping frames, so no Pong is emitted.
	})
	defer func() {
		close(serverDone)
		closeServer()
	}()
	config := testTelemetryGenerationConfig()
	config.heartbeatInterval = 10 * time.Millisecond
	config.readWait = 40 * time.Millisecond
	started := time.Now()
	err := runTelemetryGeneration(context.Background(), session, telemetryProtocolV1, config)
	if err == nil {
		t.Fatal("half-open connection did not time out")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("half-open detection took %s", elapsed)
	}
}

func TestTelemetryGenerationPongExtendsReadDeadline(t *testing.T) {
	serverDone := make(chan struct{})
	session, closeServer := newLocalTelemetrySession(t, func(connection *websocket.Conn) {
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				close(serverDone)
				return
			}
		}
	})
	defer closeServer()
	config := testTelemetryGenerationConfig()
	config.heartbeatInterval = 10 * time.Millisecond
	config.readWait = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runTelemetryGeneration(ctx, session, telemetryProtocolV1, config) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Pong did not keep connection alive: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("generation did not stop after cancellation")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server connection did not close")
	}
}

type fakeTelemetryRead struct {
	messageType int
	payload     []byte
	err         error
}

type fakeTelemetryWrite struct {
	deadline    time.Time
	messageType int
	payload     []byte
}

type fakeTelemetrySession struct {
	reads      chan fakeTelemetryRead
	writes     chan fakeTelemetryWrite
	closed     chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
	writeHook  func(time.Time, int, []byte) error

	mu           sync.Mutex
	readLimit    int64
	readDeadline time.Time
	pongHandler  func(string) error
}

func newFakeTelemetrySession() *fakeTelemetrySession {
	return &fakeTelemetrySession{
		reads:  make(chan fakeTelemetryRead, 8),
		writes: make(chan fakeTelemetryWrite, 32),
		closed: make(chan struct{}),
	}
}

func (session *fakeTelemetrySession) ReadMessage() (int, []byte, error) {
	select {
	case read := <-session.reads:
		return read.messageType, read.payload, read.err
	case <-session.closed:
		return 0, nil, errors.New("fixture session closed")
	}
}

func (session *fakeTelemetrySession) WriteMessageWithDeadline(deadline time.Time, messageType int, payload []byte) error {
	if session.writeHook != nil {
		return session.writeHook(deadline, messageType, payload)
	}
	write := fakeTelemetryWrite{deadline: deadline, messageType: messageType, payload: append([]byte(nil), payload...)}
	select {
	case session.writes <- write:
		return nil
	case <-session.closed:
		return errors.New("fixture session closed")
	}
}

func (session *fakeTelemetrySession) SetReadDeadline(deadline time.Time) error {
	session.mu.Lock()
	session.readDeadline = deadline
	session.mu.Unlock()
	return nil
}

func (session *fakeTelemetrySession) SetReadLimit(limit int64) {
	session.mu.Lock()
	session.readLimit = limit
	session.mu.Unlock()
}

func (session *fakeTelemetrySession) SetPongHandler(handler func(string) error) {
	session.mu.Lock()
	session.pongHandler = handler
	session.mu.Unlock()
}

func (session *fakeTelemetrySession) Close() error {
	session.closeCalls.Add(1)
	session.closeOnce.Do(func() { close(session.closed) })
	return nil
}

func testTelemetryGenerationConfig() telemetryGenerationConfig {
	return telemetryGenerationConfig{
		reportInterval:    time.Hour,
		heartbeatInterval: time.Hour,
		readWait:          time.Hour,
		writeTimeout:      100 * time.Millisecond,
		drainTimeout:      100 * time.Millisecond,
		readLimit:         64 * 1024,
		now:               time.Now,
		buildFrame: func(telemetryProtocol) (int, []byte, error) {
			return websocket.TextMessage, []byte("fixture-report"), nil
		},
		handleMessage: func(context.Context, []byte) {},
		queue:         newOutboundQueue(context.Background(), 8, 3),
	}
}

func waitFakeTelemetryWrite(t *testing.T, writes <-chan fakeTelemetryWrite) fakeTelemetryWrite {
	t.Helper()
	select {
	case write := <-writes:
		return write
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telemetry write")
		return fakeTelemetryWrite{}
	}
}

func waitAtomicInt32(t *testing.T, value *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if value.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("value = %d, want at least %d", value.Load(), want)
}

func newLocalTelemetrySession(t *testing.T, serve func(*websocket.Conn)) (telemetrySession, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Errorf("Upgrade failed: %v", err)
			return
		}
		defer connection.Close()
		serve(connection)
	}))
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	connection, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		server.Close()
		t.Fatalf("Dial failed: %v", err)
	}
	return ws.NewSafeConn(connection), func() {
		_ = connection.Close()
		server.Close()
	}
}
