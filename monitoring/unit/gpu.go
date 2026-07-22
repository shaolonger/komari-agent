package monitoring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"time"
	"unicode/utf8"
)

const (
	maximumGPUCount          = 64
	maximumGPUNameBytes      = 256
	maximumGPUCommandOutput  = 1 << 20
	defaultGPUCommandTimeout = 2 * time.Second
	defaultGPUBackoffMin     = 3 * time.Second
	defaultGPUBackoffMax     = time.Minute
)

var (
	errGPUOutputLimit     = errors.New("GPU command output exceeds limit")
	errNoGPUProvider      = errors.New("no supported GPU provider found")
	errGPUTopologyChanged = errors.New("GPU device topology changed")
)

// DetailedGPUInfo is the bounded, platform-independent GPU snapshot used by
// the report engine.
type DetailedGPUInfo struct {
	Name        string  `json:"name"`
	MemoryTotal uint64  `json:"memory_total"`
	MemoryUsed  uint64  `json:"memory_used"`
	Utilization float64 `json:"utilization"`
	Temperature uint64  `json:"temperature"`
}

type gpuDeviceStatic struct {
	id          string
	name        string
	memoryTotal uint64
}

type gpuProvider interface {
	Static(context.Context) ([]gpuDeviceStatic, error)
	Dynamic(context.Context, []gpuDeviceStatic) ([]DetailedGPUInfo, error)
}

type gpuCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type gpuCommandRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (function gpuCommandRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return function(ctx, name, args...)
}

type boundedGPUBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (buffer *boundedGPUBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := maximumGPUCommandOutput - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return originalLength, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.buffer.Write(data)
	return originalLength, nil
}

type execGPUCommandRunner struct{}

func (execGPUCommandRunner) Run(parent context.Context, name string, args ...string) ([]byte, error) {
	if parent == nil {
		return nil, errors.New("GPU command requires a context")
	}
	ctx, cancel := context.WithTimeout(parent, defaultGPUCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = 250 * time.Millisecond
	var stdout, stderr boundedGPUBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, errGPUOutputLimit
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("GPU command failed: %w", err)
	}
	return bytes.Clone(stdout.buffer.Bytes()), nil
}

type gpuCollector struct {
	gate               chan struct{}
	providers          func() []gpuProvider
	now                func() time.Time
	provider           gpuProvider
	metadata           []gpuDeviceStatic
	nextAttempt        time.Time
	backoff            time.Duration
	lastErr            error
	dynamicNextAttempt time.Time
	dynamicBackoff     time.Duration
	dynamicLastErr     error
}

var defaultDetailedGPUCollector = newGPUCollector(newPlatformGPUProviders, time.Now)

func newGPUCollector(providers func() []gpuProvider, now func() time.Time) *gpuCollector {
	if providers == nil {
		providers = func() []gpuProvider { return nil }
	}
	if now == nil {
		now = time.Now
	}
	return &gpuCollector{
		gate:           make(chan struct{}, 1),
		providers:      providers,
		now:            now,
		backoff:        defaultGPUBackoffMin,
		dynamicBackoff: defaultGPUBackoffMin,
	}
}

func (collector *gpuCollector) acquire(ctx context.Context) error {
	select {
	case collector.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (collector *gpuCollector) release() {
	<-collector.gate
}

func (collector *gpuCollector) Models(ctx context.Context) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("GPU model lookup requires a context")
	}
	if err := collector.acquire(ctx); err != nil {
		return nil, err
	}
	defer collector.release()
	if err := collector.ensureProvider(ctx); err != nil {
		return nil, err
	}
	models := make([]string, len(collector.metadata))
	for index := range collector.metadata {
		models[index] = collector.metadata[index].name
	}
	return models, nil
}

func (collector *gpuCollector) Sample(ctx context.Context) ([]DetailedGPUInfo, error) {
	if ctx == nil {
		return nil, errors.New("GPU sampling requires a context")
	}
	if err := collector.acquire(ctx); err != nil {
		return nil, err
	}
	defer collector.release()
	if err := collector.ensureProvider(ctx); err != nil {
		return nil, err
	}
	now := collector.now()
	if now.Before(collector.dynamicNextAttempt) {
		return nil, fmt.Errorf("GPU sampler is in backoff: %w", collector.dynamicLastErr)
	}
	values, err := collector.provider.Dynamic(ctx, collector.metadata)
	if err == nil {
		err = validateDetailedGPUInfo(values)
	}
	if err != nil {
		collector.dynamicLastErr = err
		collector.dynamicNextAttempt = now.Add(collector.dynamicBackoff)
		collector.dynamicBackoff = min(collector.dynamicBackoff*2, defaultGPUBackoffMax)
		if errors.Is(err, errGPUTopologyChanged) {
			collector.provider = nil
			collector.metadata = nil
			collector.nextAttempt = collector.dynamicNextAttempt
			collector.lastErr = err
		}
		return nil, err
	}
	collector.dynamicLastErr = nil
	collector.dynamicNextAttempt = time.Time{}
	collector.dynamicBackoff = defaultGPUBackoffMin
	return append([]DetailedGPUInfo(nil), values...), nil
}

func (collector *gpuCollector) ensureProvider(ctx context.Context) error {
	if collector.provider != nil {
		return nil
	}
	now := collector.now()
	if now.Before(collector.nextAttempt) {
		return fmt.Errorf("GPU provider discovery is in backoff: %w", collector.lastErr)
	}
	var providerErrors []error
	for _, provider := range collector.providers() {
		if provider == nil {
			providerErrors = append(providerErrors, errors.New("nil GPU provider"))
			continue
		}
		metadata, err := provider.Static(ctx)
		if err == nil {
			err = validateGPUStatic(metadata)
		}
		if err == nil {
			collector.provider = provider
			collector.metadata = append([]gpuDeviceStatic(nil), metadata...)
			collector.lastErr = nil
			collector.nextAttempt = time.Time{}
			collector.backoff = defaultGPUBackoffMin
			return nil
		}
		providerErrors = append(providerErrors, err)
		if ctx.Err() != nil {
			break
		}
	}
	err := errors.Join(providerErrors...)
	if err == nil {
		err = errNoGPUProvider
	}
	collector.lastErr = err
	collector.nextAttempt = now.Add(collector.backoff)
	collector.backoff = min(collector.backoff*2, defaultGPUBackoffMax)
	return err
}

func (collector *gpuCollector) Invalidate() {
	collector.gate <- struct{}{}
	collector.provider = nil
	collector.metadata = nil
	collector.nextAttempt = time.Time{}
	collector.backoff = defaultGPUBackoffMin
	collector.lastErr = nil
	collector.dynamicNextAttempt = time.Time{}
	collector.dynamicBackoff = defaultGPUBackoffMin
	collector.dynamicLastErr = nil
	collector.release()
}

func validateGPUStatic(values []gpuDeviceStatic) error {
	if len(values) == 0 {
		return errors.New("GPU provider returned no devices")
	}
	if len(values) > maximumGPUCount {
		return fmt.Errorf("GPU provider returned more than %d devices", maximumGPUCount)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.id == "" || value.name == "" || !utf8.ValidString(value.name) || len(value.name) > maximumGPUNameBytes {
			return errors.New("GPU provider returned invalid static metadata")
		}
		if _, exists := seen[value.id]; exists {
			return errors.New("GPU provider returned a duplicate device")
		}
		seen[value.id] = struct{}{}
	}
	return nil
}

func validateDetailedGPUInfo(values []DetailedGPUInfo) error {
	if len(values) == 0 {
		return errors.New("GPU provider returned no dynamic devices")
	}
	if len(values) > maximumGPUCount {
		return fmt.Errorf("GPU provider returned more than %d dynamic devices", maximumGPUCount)
	}
	for _, value := range values {
		if value.Name == "" || !utf8.ValidString(value.Name) || len(value.Name) > maximumGPUNameBytes ||
			value.MemoryUsed > value.MemoryTotal || math.IsNaN(value.Utilization) || math.IsInf(value.Utilization, 0) ||
			value.Utilization < 0 || value.Utilization > 100 {
			return errors.New("GPU provider returned invalid dynamic metadata")
		}
	}
	return nil
}

func validateGPUCommandOutput(output []byte) error {
	if len(output) == 0 {
		return errors.New("GPU command returned no output")
	}
	if len(output) > maximumGPUCommandOutput {
		return errGPUOutputLimit
	}
	return nil
}

func GetDetailedGPUHostContext(ctx context.Context) ([]string, error) {
	return defaultDetailedGPUCollector.Models(ctx)
}

func GetDetailedGPUInfoContext(ctx context.Context) ([]DetailedGPUInfo, error) {
	return defaultDetailedGPUCollector.Sample(ctx)
}

func GetDetailedGPUHost() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGPUCommandTimeout)
	defer cancel()
	return GetDetailedGPUHostContext(ctx)
}

func GetDetailedGPUState() ([]float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGPUCommandTimeout)
	defer cancel()
	values, err := GetDetailedGPUInfoContext(ctx)
	if err != nil {
		return nil, err
	}
	usage := make([]float64, len(values))
	for index := range values {
		usage[index] = values[index].Utilization
	}
	return usage, nil
}

func GetDetailedGPUInfo() ([]DetailedGPUInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGPUCommandTimeout)
	defer cancel()
	return GetDetailedGPUInfoContext(ctx)
}

func InvalidateDetailedGPUCache() {
	defaultDetailedGPUCollector.Invalidate()
}
