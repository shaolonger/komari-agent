package netstatic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func resetNetstaticForTest(t *testing.T) string {
	t.Helper()
	if err := Stop(); err != nil {
		t.Fatalf("stop previous runtime: %v", err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "net_static.json")

	lifecycleMu.Lock()
	mu.Lock()
	staticCache = make(map[string][]TrafficData)
	store = NetStatic{Interfaces: make(map[string][]TrafficData)}
	config = configOrDefault(NetStaticConfig{})
	lastCounters = make(map[string]counters)
	persistedTree = make(trafficIndex)
	pendingTree = make(trafficIndex)
	running = false
	activeGeneration = nil
	nextGenerationID = 0
	SaveFilePath = path
	readIOCounters = gnet.IOCounters
	clockNow = time.Now
	writeSnapshot = persistSnapshot
	mu.Unlock()
	lifecycleMu.Unlock()

	t.Cleanup(func() {
		_ = Stop()
		lifecycleMu.Lock()
		mu.Lock()
		readIOCounters = gnet.IOCounters
		clockNow = time.Now
		writeSnapshot = persistSnapshot
		mu.Unlock()
		lifecycleMu.Unlock()
	})
	return path
}

func TestIndexedRangeMatchesReferenceAcrossMonthBoundary(t *testing.T) {
	june := uint64(time.Date(2026, time.June, 30, 23, 59, 0, 0, time.UTC).Unix())
	july := uint64(time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC).Unix())
	persisted := map[string][]TrafficData{
		"eth0": {
			{Timestamp: june, Tx: 10, Rx: 20},
			{Timestamp: july, Tx: 30, Rx: 40},
			{Timestamp: july + 60, Tx: 50, Rx: 60},
		},
		"eth1": {{Timestamp: july, Tx: 7, Rx: 9}},
	}
	pending := map[string][]TrafficData{
		"eth0": {{Timestamp: july + 120, Tx: 70, Rx: 80}},
	}

	for _, testCase := range []struct {
		name       string
		start, end uint64
	}{
		{name: "all"},
		{name: "new-month", start: july},
		{name: "inclusive-boundary", start: july, end: july},
		{name: "pending-only", start: july + 120},
		{name: "empty", start: july + 1000},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			want := sumTrafficBetween(persisted, pending, testCase.start, testCase.end)
			got := sumTrafficBetweenIndexed(buildTrafficIndex(persisted), buildTrafficIndex(pending), testCase.start, testCase.end)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("indexed result = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCounterResetDoesNotCreateTrafficSpike(t *testing.T) {
	resetNetstaticForTest(t)
	mu.Lock()
	config.Nics = []string{"eth0"}
	applyCountersLocked([]gnet.IOCountersStat{{Name: "eth0", BytesSent: 1000, BytesRecv: 2000}}, 100)
	applyCountersLocked([]gnet.IOCountersStat{{Name: "eth0", BytesSent: 1100, BytesRecv: 2250}}, 101)
	applyCountersLocked([]gnet.IOCountersStat{{Name: "eth0", BytesSent: 25, BytesRecv: 50}}, 102)
	applyCountersLocked([]gnet.IOCountersStat{{Name: "eth0", BytesSent: 35, BytesRecv: 70}}, 103)
	records := append([]TrafficData(nil), staticCache["eth0"]...)
	mu.Unlock()

	want := []TrafficData{
		{Timestamp: 101, Tx: 100, Rx: 250},
		{Timestamp: 103, Tx: 10, Rx: 20},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records after reset = %#v, want %#v", records, want)
	}
}

func TestSnapshotRoundTripAndCorruptRecovery(t *testing.T) {
	path := resetNetstaticForTest(t)
	want := NetStatic{
		Interfaces: map[string][]TrafficData{"eth0": {{Timestamp: 42, Tx: 7, Rx: 11}}},
		Config: NetStaticConfig{
			DataPreserveDay: 31,
			DetectInterval:  2,
			SaveInterval:    600,
			Nics:            []string{"eth0"},
		},
	}
	if err := persistSnapshot(path, want); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	got, loaded, err := loadSnapshot(path)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !loaded || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = (%#v, %v), want (%#v, true)", got, loaded, want)
	}

	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}
	got, loaded, err = loadSnapshot(path)
	if err != nil {
		t.Fatalf("recover corrupt snapshot: %v", err)
	}
	if loaded || len(got.Interfaces) != 0 {
		t.Fatalf("corrupt snapshot unexpectedly loaded: %#v", got)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("corrupt backup missing: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt source should have been moved, stat error = %v", err)
	}
}

func TestPersistFailureCleansTemporaryFileAndPreservesTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	err := persistSnapshot(target, NetStatic{Interfaces: map[string][]TrafficData{}})
	if err == nil {
		t.Fatal("persist to a non-empty directory unexpectedly succeeded")
	}
	if content, readErr := os.ReadFile(filepath.Join(target, "sentinel")); readErr != nil || string(content) != "keep" {
		t.Fatalf("target changed after failed write: content=%q err=%v", content, readErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(directory, ".target.tmp-*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("temporary files after failure = %v, glob error = %v", matches, globErr)
	}
}

func TestStartReloadStopUsesJoinedGenerations(t *testing.T) {
	resetNetstaticForTest(t)
	var samples atomic.Int64
	readIOCounters = func(bool) ([]gnet.IOCountersStat, error) {
		value := uint64(samples.Add(1))
		return []gnet.IOCountersStat{{Name: "eth0", BytesSent: value * 10, BytesRecv: value * 20}}, nil
	}
	mu.Lock()
	config = NetStaticConfig{DataPreserveDay: 31, DetectInterval: 0.002, SaveInterval: 60}
	store.Config = config
	mu.Unlock()

	var startGroup sync.WaitGroup
	for range 8 {
		startGroup.Add(1)
		go func() {
			defer startGroup.Done()
			if err := StartOrContinue(); err != nil {
				t.Errorf("start: %v", err)
			}
		}()
	}
	startGroup.Wait()

	mu.RLock()
	first := activeGeneration
	firstID := first.id
	mu.RUnlock()
	waitFor(t, time.Second, func() bool { return samples.Load() >= 2 })
	if err := SetNewConfig(NetStaticConfig{DetectInterval: 0.003}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-first.done:
	default:
		t.Fatal("old generation was not joined before reload returned")
	}
	mu.RLock()
	second := activeGeneration
	secondID := second.id
	mu.RUnlock()
	if secondID <= firstID {
		t.Fatalf("generation id did not advance: first=%d second=%d", firstID, secondID)
	}

	if err := Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-second.done:
	default:
		t.Fatal("active generation was not joined before stop returned")
	}
	mu.RLock()
	defer mu.RUnlock()
	if running || activeGeneration != nil {
		t.Fatalf("runtime remains active: running=%v generation=%v", running, activeGeneration)
	}
}

func TestPeriodicPersistenceDoesNotHoldDataLock(t *testing.T) {
	resetNetstaticForTest(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	writeSnapshot = func(string, NetStatic) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	}
	readIOCounters = func(bool) ([]gnet.IOCountersStat, error) { return nil, nil }
	mu.Lock()
	config = NetStaticConfig{DataPreserveDay: 31, DetectInterval: 60, SaveInterval: 0.002}
	store.Config = config
	mu.Unlock()
	if err := StartOrContinue(); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("periodic persistence did not start")
	}

	queryDone := make(chan struct{})
	go func() {
		_, _ = GetTotalTrafficBetween(0, 0)
		close(queryDone)
	}()
	select {
	case <-queryDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("query blocked behind disk persistence")
	}
	close(release)
}

func TestConcurrentQueriesAndMutations(t *testing.T) {
	resetNetstaticForTest(t)
	fixedNow := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	clockNow = func() time.Time { return fixedNow }
	base := uint64(fixedNow.Add(-time.Hour).Unix())
	if err := ForceReplaceRecord(map[string][]TrafficData{
		"eth0": {{Timestamp: base, Tx: 1, Rx: 2}},
	}); err != nil {
		t.Fatalf("seed records: %v", err)
	}

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				switch worker % 4 {
				case 0:
					_, _ = GetTotalTrafficBetween(base, 0)
				case 1:
					_, _ = GetNetStaticBetween(base, 0)
				case 2:
					mu.Lock()
					applyCountersLocked([]gnet.IOCountersStat{{Name: "eth0", BytesSent: uint64(iteration + 1), BytesRecv: uint64(iteration + 2)}}, base+uint64(iteration))
					mu.Unlock()
				case 3:
					_, _ = GetNetStatic()
				}
			}
		}(worker)
	}
	group.Wait()
}

func TestSetNewConfigRejectsInvalidPersistenceParameters(t *testing.T) {
	resetNetstaticForTest(t)
	for _, config := range []NetStaticConfig{
		{DataPreserveDay: -1},
		{DetectInterval: -1},
		{DetectInterval: 0.0001},
		{SaveInterval: -1},
		{SaveInterval: 8 * 24 * 60 * 60},
		{Nics: []string{""}},
	} {
		if err := SetNewConfig(config); err == nil {
			t.Fatalf("SetNewConfig(%+v) unexpectedly succeeded", config)
		}
	}
}

func TestStopContextIsBoundedWhenPersistenceStalls(t *testing.T) {
	resetNetstaticForTest(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	writeSnapshot = func(string, NetStatic) error {
		close(entered)
		<-release
		return nil
	}
	mu.Lock()
	config = NetStaticConfig{DataPreserveDay: 31, DetectInterval: 60, SaveInterval: 60}
	store.Config = config
	mu.Unlock()
	if err := StartOrContinue(); err != nil {
		t.Fatalf("start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := StopContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopContext() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("StopContext exceeded bound: %s", elapsed)
	}
	select {
	case <-entered:
	default:
		t.Fatal("final persistence did not start")
	}
	close(release)
	waitFor(t, time.Second, func() bool {
		if !lifecycleMu.TryLock() {
			return false
		}
		lifecycleMu.Unlock()
		return true
	})
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
