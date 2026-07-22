package server

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/monitoring"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
	"github.com/komari-monitor/komari-agent/utils"
	"github.com/komari-monitor/komari-agent/ws"
)

const (
	defaultTelemetryHeartbeatInterval = 30 * time.Second
	defaultTelemetryReadWait          = 75 * time.Second
	defaultTelemetryWriteTimeout      = 10 * time.Second
	defaultTelemetryDrainTimeout      = 5 * time.Second
	defaultTelemetryReadLimit         = telemetryv2.MaxFrameSize
	defaultStableConnectionThreshold  = time.Minute
	maximumReconnectBackoff           = time.Minute
)

type telemetrySession interface {
	ReadMessage() (int, []byte, error)
	WriteMessageWithDeadline(time.Time, int, []byte) error
	SetReadDeadline(time.Time) error
	SetReadLimit(int64)
	SetPongHandler(func(string) error)
	Close() error
}

type telemetryConnector func(context.Context, string) (telemetrySession, telemetryProtocol, error)

type telemetryGenerationConfig struct {
	reportInterval    time.Duration
	heartbeatInterval time.Duration
	readWait          time.Duration
	writeTimeout      time.Duration
	drainTimeout      time.Duration
	readLimit         int64
	now               func() time.Time
	buildFrame        func(telemetryProtocol) (int, []byte, error)
	handleMessage     func([]byte)
	queue             *outboundQueue
}

type telemetryRunner struct {
	endpoint         string
	connect          telemetryConnector
	generation       telemetryGenerationConfig
	maxRetries       int
	reconnectBase    time.Duration
	reconnectMaximum time.Duration
	stableThreshold  time.Duration
	fullJitter       func(time.Duration) time.Duration
	wait             func(context.Context, time.Duration) bool
}

func RunTelemetryWebSocket(ctx context.Context) error {
	if ctx == nil {
		return errors.New("telemetry WebSocket requires a parent context")
	}
	if err := monitoring.StartReportSampler(ctx); err != nil {
		return fmt.Errorf("start report sampler: %w", err)
	}
	defer monitoring.StopReportSampler()

	endpoint := buildClientWebSocketEndpoint("/api/clients/report", nil)
	if converted, err := utils.ConvertIDNToASCII(endpoint); err == nil {
		endpoint = converted
	} else {
		log.Printf("Warning: Failed to convert WebSocket IDN to ASCII: %v", err)
	}
	runner := newTelemetryRunner(endpoint)
	return runner.Run(ctx)
}

func newTelemetryRunner(endpoint string) *telemetryRunner {
	reportInterval := time.Duration(flags.Interval * float64(time.Second))
	if reportInterval < time.Second {
		reportInterval = time.Second
	}
	reconnectBase := time.Duration(flags.ReconnectInterval) * time.Second
	if reconnectBase <= 0 {
		reconnectBase = time.Second
	}
	reconnectMaximum := maximumReconnectBackoff
	if reconnectBase > reconnectMaximum {
		reconnectMaximum = reconnectBase
	}
	maxRetries := flags.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	runner := &telemetryRunner{
		endpoint:         endpoint,
		connect:          connectTelemetryWebSocket,
		maxRetries:       maxRetries,
		reconnectBase:    reconnectBase,
		reconnectMaximum: reconnectMaximum,
		fullJitter:       randomFullJitter,
		wait:             waitForContext,
		stableThreshold:  defaultStableConnectionThreshold,
	}
	runner.generation = telemetryGenerationConfig{
		reportInterval:    reportInterval,
		heartbeatInterval: defaultTelemetryHeartbeatInterval,
		readWait:          defaultTelemetryReadWait,
		writeTimeout:      defaultTelemetryWriteTimeout,
		drainTimeout:      defaultTelemetryDrainTimeout,
		readLimit:         defaultTelemetryReadLimit,
		now:               time.Now,
		buildFrame:        buildTelemetryFrame,
	}
	return runner
}

func (runner *telemetryRunner) Run(ctx context.Context) error {
	queue := newOutboundQueue(ctx, defaultReliableQueueCapacity, defaultReliableWriteAttempts)
	runner.generation.queue = queue
	defer queue.Close(true)

	failedConnections := 0
	shortGenerations := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		log.Println("Attempting to connect to WebSocket...")
		session, protocol, err := runner.connect(ctx, runner.endpoint)
		if err != nil {
			if failedConnections >= runner.maxRetries {
				return fmt.Errorf("maximum WebSocket retries reached: %w", err)
			}
			delay := exponentialFullJitter(
				runner.reconnectBase,
				runner.reconnectMaximum,
				failedConnections,
				runner.fullJitter,
			)
			failedConnections++
			log.Printf("WebSocket connection failed; retry %d/%d in %s: %v", failedConnections, runner.maxRetries, delay, err)
			if !runner.wait(ctx, delay) {
				return ctx.Err()
			}
			continue
		}

		failedConnections = 0
		connectedAt := runner.generation.now()
		log.Println("WebSocket connected")
		diagnostics.RecordWebSocketConnected()
		queue.ResetEphemeral()
		runner.generation.handleMessage = func(message []byte) {
			handleWebSocketMessage(queue, message)
		}
		err = runTelemetryGeneration(ctx, session, protocol, runner.generation)
		diagnostics.RecordWebSocketDisconnected()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("WebSocket generation ended; reconnecting immediately: %v", err)
		lifetime := runner.generation.now().Sub(connectedAt)
		if lifetime >= runner.stableThreshold {
			shortGenerations = 0
			continue
		}
		shortGenerations++
		// The first read failure reconnects immediately. Repeated accept/close
		// loops then use full-jitter backoff to avoid a tight reconnect storm.
		if shortGenerations > 1 {
			delay := exponentialFullJitter(
				runner.reconnectBase,
				runner.reconnectMaximum,
				shortGenerations-2,
				runner.fullJitter,
			)
			if !runner.wait(ctx, delay) {
				return ctx.Err()
			}
		}
	}
}

func runTelemetryGeneration(
	parent context.Context,
	session telemetrySession,
	protocol telemetryProtocol,
	config telemetryGenerationConfig,
) error {
	if err := validateTelemetryGenerationConfig(config); err != nil {
		_ = session.Close()
		return err
	}
	// The writer intentionally outlives producer cancellation during graceful
	// shutdown so reliable control results can drain within a fixed deadline.
	base := context.WithoutCancel(parent)
	generationCtx, cancelGeneration := context.WithCancel(base)
	writerCtx, cancelWriter := context.WithCancel(base)
	defer cancelGeneration()
	defer cancelWriter()

	if err := session.SetReadDeadline(config.now().Add(config.readWait)); err != nil {
		_ = session.Close()
		return fmt.Errorf("set initial WebSocket read deadline: %w", err)
	}
	session.SetReadLimit(config.readLimit)
	session.SetPongHandler(func(string) error {
		return session.SetReadDeadline(config.now().Add(config.readWait))
	})

	errorsChannel := make(chan error, 4)
	writerDone := make(chan struct{})
	var workers sync.WaitGroup
	startWorker := func(workerContext context.Context, done chan struct{}, run func(context.Context) error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if done != nil {
				defer close(done)
			}
			if err := run(workerContext); err != nil && workerContext.Err() == nil {
				select {
				case errorsChannel <- err:
				default:
				}
			}
		}()
	}
	startWorker(generationCtx, nil, func(ctx context.Context) error {
		return readTelemetryMessages(ctx, session, config.handleMessage)
	})
	startWorker(generationCtx, nil, func(ctx context.Context) error {
		return produceTelemetryReports(ctx, protocol, config)
	})
	startWorker(generationCtx, nil, func(ctx context.Context) error {
		return produceTelemetryHeartbeats(ctx, config)
	})
	startWorker(writerCtx, writerDone, func(ctx context.Context) error {
		return writeOutboundFrames(ctx, session, config)
	})

	select {
	case generationErr := <-errorsChannel:
		cancelGeneration()
		cancelWriter()
		config.queue.ResetEphemeral()
		_ = session.Close()
		workers.Wait()
		return generationErr
	case <-parent.Done():
		cancelGeneration()
		config.queue.Close(true)
		timer := time.NewTimer(config.drainTimeout)
		select {
		case <-writerDone:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			diagnostics.RecordQueueDrainTimeout()
			cancelWriter()
		}
		cancelWriter()
		_ = session.Close()
		workers.Wait()
		return parent.Err()
	}
}

func validateTelemetryGenerationConfig(config telemetryGenerationConfig) error {
	if config.reportInterval <= 0 || config.heartbeatInterval <= 0 || config.readWait <= 0 || config.writeTimeout <= 0 || config.drainTimeout <= 0 {
		return errors.New("telemetry generation intervals and deadlines must be positive")
	}
	if config.readLimit <= 0 || config.now == nil || config.buildFrame == nil || config.queue == nil {
		return errors.New("telemetry generation requires read limit, clock, frame builder and outbound queue")
	}
	return nil
}

func readTelemetryMessages(ctx context.Context, session telemetrySession, handle func([]byte)) error {
	if handle == nil {
		handle = func([]byte) {}
	}
	for {
		messageType, message, err := session.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read WebSocket message: %w", err)
		}
		if messageType != websocket.TextMessage {
			return fmt.Errorf("unsupported control WebSocket message type %d", messageType)
		}
		diagnostics.RecordWebSocketMessageRead()
		handle(message)
	}
}

func produceTelemetryReports(
	ctx context.Context,
	protocol telemetryProtocol,
	config telemetryGenerationConfig,
) error {
	publish := func() error {
		messageType, payload, encodeErr := config.buildFrame(protocol)
		if encodeErr != nil {
			log.Printf("Telemetry v2 encoding failed; sent JSON v1 fallback: %v", encodeErr)
		}
		return config.queue.EnqueueTelemetry(messageType, payload)
	}
	if err := publish(); err != nil {
		return err
	}
	ticker := time.NewTicker(config.reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := publish(); err != nil {
				return err
			}
		}
	}
}

func produceTelemetryHeartbeats(ctx context.Context, config telemetryGenerationConfig) error {
	ticker := time.NewTicker(config.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := config.queue.EnqueueHeartbeat(); err != nil {
				return err
			}
		}
	}
}

func writeOutboundFrames(ctx context.Context, session telemetrySession, config telemetryGenerationConfig) error {
	for {
		frame, err := config.queue.Take(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := session.WriteMessageWithDeadline(
			config.now().Add(config.writeTimeout),
			frame.messageType,
			frame.payload,
		); err != nil {
			config.queue.Nack(frame)
			return fmt.Errorf("write outbound WebSocket frame: %w", err)
		}
		config.queue.Ack(frame)
		diagnostics.RecordWebSocketMessageSent()
	}
}

func connectTelemetryWebSocket(ctx context.Context, endpoint string) (telemetrySession, telemetryProtocol, error) {
	dialer := newTelemetryWSDialer()
	conn, response, err := dialer.DialContext(ctx, endpoint, newWSHeaders())
	if err != nil {
		if response != nil {
			status := response.Status
			if response.Body != nil {
				_ = response.Body.Close()
			}
			if response.StatusCode != 101 {
				return nil, telemetryProtocolV1, errors.New(status)
			}
		}
		return nil, telemetryProtocolV1, err
	}
	protocol, err := negotiatedTelemetryProtocol(conn.Subprotocol())
	if err != nil {
		_ = conn.Close()
		return nil, telemetryProtocolV1, err
	}
	return ws.NewSafeConn(conn), protocol, nil
}

func exponentialFullJitter(
	base time.Duration,
	maximum time.Duration,
	attempt int,
	jitter func(time.Duration) time.Duration,
) time.Duration {
	ceiling := base
	for index := 0; index < attempt && ceiling < maximum; index++ {
		if ceiling >= maximum/2 {
			ceiling = maximum
			break
		}
		ceiling *= 2
	}
	if ceiling > maximum {
		ceiling = maximum
	}
	return jitter(ceiling)
}

func randomFullJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	var entropy [8]byte
	if _, err := cryptorand.Read(entropy[:]); err != nil {
		return maximum / 2
	}
	return time.Duration(binary.LittleEndian.Uint64(entropy[:]) % (uint64(maximum) + 1))
}

func waitForContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
