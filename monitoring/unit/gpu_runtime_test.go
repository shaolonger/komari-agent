package monitoring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGPUProvider struct {
	staticCalls  atomic.Int32
	dynamicCalls atomic.Int32
	static       func(context.Context) ([]gpuDeviceStatic, error)
	dynamic      func(context.Context, []gpuDeviceStatic) ([]DetailedGPUInfo, error)
}

func (provider *fakeGPUProvider) Static(ctx context.Context) ([]gpuDeviceStatic, error) {
	provider.staticCalls.Add(1)
	return provider.static(ctx)
}

func (provider *fakeGPUProvider) Dynamic(ctx context.Context, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
	provider.dynamicCalls.Add(1)
	return provider.dynamic(ctx, metadata)
}

func validFakeGPUProvider() *fakeGPUProvider {
	return &fakeGPUProvider{
		static: func(context.Context) ([]gpuDeviceStatic, error) {
			return []gpuDeviceStatic{{id: "0", name: "Fixture GPU", memoryTotal: 1024}}, nil
		},
		dynamic: func(_ context.Context, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
			return []DetailedGPUInfo{{Name: metadata[0].name, MemoryTotal: metadata[0].memoryTotal, MemoryUsed: 512, Utilization: 50, Temperature: 60}}, nil
		},
	}
}

func TestGPUCollectorIsLazyAndCachesStaticMetadata(t *testing.T) {
	provider := validFakeGPUProvider()
	var discoveries atomic.Int32
	collector := newGPUCollector(func() []gpuProvider {
		discoveries.Add(1)
		return []gpuProvider{provider}
	}, time.Now)
	if discoveries.Load() != 0 || provider.staticCalls.Load() != 0 || provider.dynamicCalls.Load() != 0 {
		t.Fatal("constructing a GPU collector performed discovery or sampling")
	}
	models, err := collector.Models(context.Background())
	if err != nil || len(models) != 1 || models[0] != "Fixture GPU" {
		t.Fatalf("models = %v, err = %v", models, err)
	}
	models[0] = "mutated"
	for range 2 {
		values, err := collector.Sample(context.Background())
		if err != nil || len(values) != 1 || values[0].Name != "Fixture GPU" {
			t.Fatalf("sample = %+v, err = %v", values, err)
		}
	}
	if discoveries.Load() != 1 || provider.staticCalls.Load() != 1 || provider.dynamicCalls.Load() != 2 {
		t.Fatalf("calls discovery/static/dynamic = %d/%d/%d", discoveries.Load(), provider.staticCalls.Load(), provider.dynamicCalls.Load())
	}
	collector.Invalidate()
	if _, err := collector.Models(context.Background()); err != nil || discoveries.Load() != 2 || provider.staticCalls.Load() != 2 {
		t.Fatalf("explicit invalidation did not reload static metadata: err=%v discovery/static=%d/%d", err, discoveries.Load(), provider.staticCalls.Load())
	}
}

func TestGPUCollectorCollapsesConcurrentColdModelReads(t *testing.T) {
	provider := validFakeGPUProvider()
	started := make(chan struct{})
	release := make(chan struct{})
	provider.static = func(context.Context) ([]gpuDeviceStatic, error) {
		close(started)
		<-release
		return []gpuDeviceStatic{{id: "0", name: "Fixture GPU", memoryTotal: 1024}}, nil
	}
	collector := newGPUCollector(func() []gpuProvider { return []gpuProvider{provider} }, time.Now)
	const workers = 64
	var waiters sync.WaitGroup
	errorsFound := make(chan error, workers)
	for range workers {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			models, err := collector.Models(context.Background())
			if err != nil || len(models) != 1 || models[0] != "Fixture GPU" {
				errorsFound <- fmt.Errorf("models=%v err=%v", models, err)
			}
		}()
	}
	<-started
	close(release)
	waiters.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if provider.staticCalls.Load() != 1 {
		t.Fatalf("concurrent static calls = %d", provider.staticCalls.Load())
	}
}

func TestGPUCollectorProviderFailoverAndErrorBackoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	broken := validFakeGPUProvider()
	broken.static = func(context.Context) ([]gpuDeviceStatic, error) { return nil, errors.New("NVIDIA unavailable") }
	working := validFakeGPUProvider()
	var dynamicFailures atomic.Int32
	working.dynamic = func(_ context.Context, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
		if dynamicFailures.Add(1) <= 2 {
			return nil, errors.New("temporary GPU failure")
		}
		return []DetailedGPUInfo{{Name: metadata[0].name, MemoryTotal: 1024, MemoryUsed: 1, Utilization: 1}}, nil
	}
	collector := newGPUCollector(func() []gpuProvider { return []gpuProvider{broken, working} }, func() time.Time { return now })
	if _, err := collector.Sample(context.Background()); err == nil {
		t.Fatal("first dynamic failure was accepted")
	}
	if _, err := collector.Sample(context.Background()); err == nil || working.dynamicCalls.Load() != 1 {
		t.Fatalf("backoff sample err = %v, dynamic calls = %d", err, working.dynamicCalls.Load())
	}
	now = now.Add(4 * time.Second)
	_, _ = collector.Sample(context.Background())
	if working.dynamicCalls.Load() != 2 {
		t.Fatalf("first backoff expiry calls = %d", working.dynamicCalls.Load())
	}
	now = now.Add(7 * time.Second)
	values, err := collector.Sample(context.Background())
	if err != nil || len(values) != 1 || broken.staticCalls.Load() != 1 || working.staticCalls.Load() != 1 || working.dynamicCalls.Load() != 3 {
		t.Fatalf("recovered values = %+v, err = %v, calls = %d/%d/%d", values, err, broken.staticCalls.Load(), working.staticCalls.Load(), working.dynamicCalls.Load())
	}
}

func TestGPUCollectorNoProviderDiscoveryBackoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var discoveries atomic.Int32
	collector := newGPUCollector(func() []gpuProvider {
		discoveries.Add(1)
		return nil
	}, func() time.Time { return now })
	for range 2 {
		if _, err := collector.Models(context.Background()); !errors.Is(err, errNoGPUProvider) {
			t.Fatalf("no-provider error = %v", err)
		}
	}
	if discoveries.Load() != 1 {
		t.Fatalf("discovery calls during backoff = %d", discoveries.Load())
	}
	now = now.Add(4 * time.Second)
	_, _ = collector.Models(context.Background())
	if discoveries.Load() != 2 {
		t.Fatalf("discovery calls after backoff = %d", discoveries.Load())
	}
}

func TestGPUCollectorRefreshesStaticMetadataAfterTopologyChange(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	provider := validFakeGPUProvider()
	provider.dynamic = func(_ context.Context, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
		if provider.dynamicCalls.Load() == 1 {
			return nil, errGPUTopologyChanged
		}
		return []DetailedGPUInfo{{Name: metadata[0].name, MemoryTotal: 1024, MemoryUsed: 1, Utilization: 1}}, nil
	}
	collector := newGPUCollector(func() []gpuProvider { return []gpuProvider{provider} }, func() time.Time { return now })
	if _, err := collector.Sample(context.Background()); !errors.Is(err, errGPUTopologyChanged) {
		t.Fatalf("topology error = %v", err)
	}
	now = now.Add(4 * time.Second)
	values, err := collector.Sample(context.Background())
	if err != nil || len(values) != 1 || provider.staticCalls.Load() != 2 || provider.dynamicCalls.Load() != 2 {
		t.Fatalf("topology refresh values=%+v err=%v calls=%d/%d", values, err, provider.staticCalls.Load(), provider.dynamicCalls.Load())
	}
}

func TestGPUCollectorWaiterHonorsContext(t *testing.T) {
	provider := validFakeGPUProvider()
	started := make(chan struct{})
	release := make(chan struct{})
	provider.static = func(context.Context) ([]gpuDeviceStatic, error) {
		close(started)
		<-release
		return []gpuDeviceStatic{{id: "0", name: "Fixture GPU", memoryTotal: 1024}}, nil
	}
	collector := newGPUCollector(func() []gpuProvider { return []gpuProvider{provider} }, time.Now)
	done := make(chan struct{})
	go func() {
		_, _ = collector.Models(context.Background())
		close(done)
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := collector.Models(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting context error = %v", err)
	}
	close(release)
	<-done
}

func TestNvidiaTargetedProviderParsesMultiGPUAndRejectsTopologyChange(t *testing.T) {
	runner := gpuCommandRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(args[0], "name") {
			return []byte("0, NVIDIA Fixture 0, 24576\n1, NVIDIA Fixture 1, 49152\n"), nil
		}
		return []byte("1, 8192, 75, 70\n0, 4096, 25, 60\n"), nil
	})
	provider := &nvidiaGPUProvider{path: "nvidia-smi", runner: runner}
	metadata, err := provider.Static(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	values, err := provider.Dynamic(context.Background(), metadata)
	if err != nil || len(values) != 2 || values[0].Name != "NVIDIA Fixture 0" || values[0].Utilization != 25 || values[1].MemoryUsed != 8192*1024*1024 {
		t.Fatalf("NVIDIA values = %+v, err = %v", values, err)
	}
	if _, err := parseNvidiaDynamicCSV([]byte("0, 1, 2, 3\n"), metadata); err == nil {
		t.Fatal("NVIDIA topology change was accepted")
	}
}

func TestNvidiaTargetedProviderTreatsDocumentedUnavailableMetricsAsZero(t *testing.T) {
	metadata := []gpuDeviceStatic{{id: "GPU-fixture", name: "Fixture", memoryTotal: 1024}}
	values, err := parseNvidiaDynamicCSV([]byte("GPU-fixture, [N/A], N/A, Not Supported\n"), metadata)
	if err != nil || len(values) != 1 || values[0].MemoryUsed != 0 || values[0].Utilization != 0 || values[0].Temperature != 0 {
		t.Fatalf("unavailable NVIDIA values = %+v, err = %v", values, err)
	}
}

func TestAMDTargetedProviderParsesMultiGPUDeterministically(t *testing.T) {
	static := []byte(`{"card1":{"Card series":"AMD Fixture 1","VRAM Total Memory (B)":"200"},"card0":{"Card series":"AMD Fixture 0","VRAM Total Memory (B)":"100"}}`)
	dynamic := []byte(`{"card1":{"GPU use (%)":"75","VRAM Total Used Memory (B)":"100","Temperature (Sensor junction) (C)":"70"},"card0":{"GPU use (%)":"25","VRAM Total Used Memory (B)":"50","Temperature (Sensor junction) (C)":"60"}}`)
	runner := gpuCommandRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "--showproductname" {
			return static, nil
		}
		return dynamic, nil
	})
	provider := &amdGPUProvider{path: "rocm-smi", runner: runner}
	metadata, err := provider.Static(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	values, err := provider.Dynamic(context.Background(), metadata)
	if err != nil || len(values) != 2 || values[0].Name != "AMD Fixture 0" || values[0].MemoryUsed != 50 || values[1].Utilization != 75 {
		t.Fatalf("AMD values = %+v, err = %v", values, err)
	}
}

func TestGPUProvidersRejectOversizedAndInvalidValues(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maximumGPUCommandOutput+1)
	if _, err := parseNvidiaStaticCSV(oversized); !errors.Is(err, errGPUOutputLimit) {
		t.Fatalf("oversized NVIDIA error = %v", err)
	}
	if _, _, err := parseROCmResponse(oversized); !errors.Is(err, errGPUOutputLimit) {
		t.Fatalf("oversized AMD error = %v", err)
	}
	metadata := []gpuDeviceStatic{{id: "0", name: "Fixture", memoryTotal: 100}}
	for name, output := range map[string]string{
		"NaN":      "0, 1, NaN, 60\n",
		"overused": "0, 101, 50, 60\n",
		"too hot":  "0, 1, 50, invalid\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseNvidiaDynamicCSV([]byte(output), metadata); err == nil {
				t.Fatalf("invalid NVIDIA output %q was accepted", output)
			}
		})
	}
}

func TestExecGPUCommandRunnerHonorsContextAndOutputLimit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := execGPUCommandRunner{}
	t.Setenv("GO_GPU_HELPER_MODE", "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := runner.Run(ctx, executable, "-test.run=^TestGPUCommandHelperProcess$"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("command timeout error = %v", err)
	}
	t.Setenv("GO_GPU_HELPER_MODE", "oversized")
	if _, err := runner.Run(context.Background(), executable, "-test.run=^TestGPUCommandHelperProcess$"); !errors.Is(err, errGPUOutputLimit) {
		t.Fatalf("command output limit error = %v", err)
	}
}

func TestGPUCommandHelperProcess(t *testing.T) {
	switch os.Getenv("GO_GPU_HELPER_MODE") {
	case "sleep":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "oversized":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), maximumGPUCommandOutput+1))
		os.Exit(0)
	}
}
