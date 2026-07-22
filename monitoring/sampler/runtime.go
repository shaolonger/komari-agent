package sampler

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// Clock keeps scheduling deterministic in tests while the production runtime
// uses monotonic Go timers. Wait returns false when the context is cancelled.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) bool
}

type SampleFunc func(context.Context) (any, error)

// Spec describes one independently scheduled source. Sample implementations
// must honor context cancellation and publish immutable values.
type Spec struct {
	Name            string
	Interval        time.Duration
	Timeout         time.Duration
	StaleAfter      time.Duration
	Jitter          time.Duration
	ErrorBackoffMin time.Duration
	ErrorBackoffMax time.Duration
	Sample          SampleFunc
}

type Result struct {
	Value             any       `json:"value,omitempty"`
	CollectedAt       time.Time `json:"collected_at,omitempty"`
	AttemptedAt       time.Time `json:"attempted_at"`
	Error             string    `json:"error,omitempty"`
	ConsecutiveErrors uint64    `json:"consecutive_errors"`
	Stale             bool      `json:"stale"`
	Generation        uint64    `json:"generation"`
}

type Snapshot struct {
	Generation uint64            `json:"generation"`
	CapturedAt time.Time         `json:"captured_at"`
	Results    map[string]Result `json:"results"`
}

type resultState struct {
	result     Result
	staleAfter time.Duration
}

type workerStart struct {
	ctx        context.Context
	generation uint64
	spec       Spec
}

type Runtime struct {
	mu         sync.RWMutex
	clock      Clock
	parent     context.Context
	cancel     context.CancelFunc
	running    bool
	generation uint64
	specs      []Spec
	results    map[string]resultState
	wg         sync.WaitGroup
}

type Option func(*Runtime)

func WithClock(clock Clock) Option {
	return func(runtime *Runtime) {
		if clock != nil {
			runtime.clock = clock
		}
	}
}

func New(specs []Spec, options ...Option) (*Runtime, error) {
	normalized, err := normalizeSpecs(specs)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		clock:   realClock{},
		specs:   normalized,
		results: make(map[string]resultState, len(normalized)),
	}
	for _, option := range options {
		option(runtime)
	}
	return runtime, nil
}

func (runtime *Runtime) Start(parent context.Context) error {
	if parent == nil {
		return errors.New("sampler runtime requires a parent context")
	}

	runtime.mu.Lock()
	if runtime.running {
		runtime.mu.Unlock()
		return errors.New("sampler runtime is already running")
	}
	runtime.parent = parent
	runtime.running = true
	starts := runtime.startGenerationLocked()
	runtime.mu.Unlock()
	runtime.launch(starts)
	return nil
}

func (runtime *Runtime) Reload(specs []Spec) error {
	normalized, err := normalizeSpecs(specs)
	if err != nil {
		return err
	}

	runtime.mu.Lock()
	runtime.specs = normalized
	if !runtime.running {
		runtime.results = make(map[string]resultState, len(normalized))
		runtime.mu.Unlock()
		return nil
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	starts := runtime.startGenerationLocked()
	runtime.mu.Unlock()
	runtime.launch(starts)
	return nil
}

func (runtime *Runtime) Stop() {
	runtime.mu.Lock()
	if !runtime.running {
		runtime.mu.Unlock()
		return
	}
	runtime.running = false
	if runtime.cancel != nil {
		runtime.cancel()
	}
	runtime.cancel = nil
	runtime.parent = nil
	runtime.mu.Unlock()
	runtime.wg.Wait()
}

func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mu.RLock()
	generation := runtime.generation
	now := runtime.clock.Now()
	results := make(map[string]Result, len(runtime.results))
	for name, state := range runtime.results {
		result := state.result
		result.Stale = result.CollectedAt.IsZero() || now.Sub(result.CollectedAt) > state.staleAfter
		results[name] = result
	}
	runtime.mu.RUnlock()
	return Snapshot{Generation: generation, CapturedAt: now, Results: results}
}

func (runtime *Runtime) Running() bool {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.running
}

func (runtime *Runtime) startGenerationLocked() []workerStart {
	runtime.generation++
	generation := runtime.generation
	ctx, cancel := context.WithCancel(runtime.parent)
	runtime.cancel = cancel
	runtime.results = make(map[string]resultState, len(runtime.specs))
	starts := make([]workerStart, 0, len(runtime.specs))
	for _, spec := range runtime.specs {
		runtime.wg.Add(1)
		starts = append(starts, workerStart{ctx: ctx, generation: generation, spec: spec})
	}
	return starts
}

func (runtime *Runtime) launch(starts []workerStart) {
	for _, start := range starts {
		go runtime.run(start)
	}
}

func (runtime *Runtime) run(start workerStart) {
	defer runtime.wg.Done()
	backoff := start.spec.ErrorBackoffMin
	var attempt uint64
	for {
		if start.ctx.Err() != nil {
			return
		}
		attempt++
		attemptedAt := runtime.clock.Now()
		sampleCtx, cancel := context.WithTimeout(start.ctx, start.spec.Timeout)
		value, sampleErr := start.spec.Sample(sampleCtx)
		if sampleErr == nil && sampleCtx.Err() != nil {
			sampleErr = sampleCtx.Err()
		}
		cancel()
		runtime.publish(start.generation, start.spec, attemptedAt, value, sampleErr)

		delay := start.spec.Interval
		if sampleErr != nil {
			delay = backoff
			if backoff >= start.spec.ErrorBackoffMax/2 {
				backoff = start.spec.ErrorBackoffMax
			} else {
				backoff *= 2
			}
		} else {
			backoff = start.spec.ErrorBackoffMin
		}
		delay += deterministicJitter(start.spec.Name, start.generation, attempt, start.spec.Jitter)
		if !runtime.clock.Wait(start.ctx, delay) {
			return
		}
	}
}

func (runtime *Runtime) publish(generation uint64, spec Spec, attemptedAt time.Time, value any, sampleErr error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.running || runtime.generation != generation {
		return
	}
	state := runtime.results[spec.Name]
	state.staleAfter = spec.StaleAfter
	state.result.AttemptedAt = attemptedAt
	state.result.Generation = generation
	if sampleErr != nil {
		state.result.Error = sampleErr.Error()
		state.result.ConsecutiveErrors++
	} else {
		state.result.Value = value
		state.result.CollectedAt = attemptedAt
		state.result.Error = ""
		state.result.ConsecutiveErrors = 0
	}
	runtime.results[spec.Name] = state
}

func normalizeSpecs(specs []Spec) ([]Spec, error) {
	normalized := make([]Spec, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for index, spec := range specs {
		if spec.Name == "" {
			return nil, fmt.Errorf("sampler at index %d has an empty name", index)
		}
		if _, exists := seen[spec.Name]; exists {
			return nil, fmt.Errorf("duplicate sampler name %q", spec.Name)
		}
		seen[spec.Name] = struct{}{}
		if spec.Sample == nil {
			return nil, fmt.Errorf("sampler %q has no sample function", spec.Name)
		}
		if spec.Interval <= 0 {
			return nil, fmt.Errorf("sampler %q has an invalid interval", spec.Name)
		}
		if spec.Timeout <= 0 {
			spec.Timeout = min(spec.Interval, 5*time.Second)
		}
		if spec.StaleAfter <= 0 {
			spec.StaleAfter = spec.Interval * 3
		}
		if spec.Jitter < 0 {
			return nil, fmt.Errorf("sampler %q has a negative jitter", spec.Name)
		}
		if spec.ErrorBackoffMin <= 0 {
			spec.ErrorBackoffMin = spec.Interval
		}
		if spec.ErrorBackoffMax < spec.ErrorBackoffMin {
			spec.ErrorBackoffMax = max(spec.ErrorBackoffMin, spec.Interval*8)
		}
		normalized[index] = spec
	}
	return normalized, nil
}

func deterministicJitter(name string, generation, attempt uint64, maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	hasher := fnv.New64a()
	_, _ = fmt.Fprintf(hasher, "%s:%d:%d", name, generation, attempt)
	return time.Duration(hasher.Sum64() % (uint64(maximum) + 1))
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) Wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
