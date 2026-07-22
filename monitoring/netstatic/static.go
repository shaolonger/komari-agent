package netstatic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// Netstatic keeps recent per-interface traffic deltas in memory and writes a
// compact snapshot periodically. The on-disk schema is intentionally stable.
var (
	DefaultDataPreserveDay = 31.0
	DefaultDetectInterval  = 2.0
	DefaultSaveInterval    = 60.0 * 10
	SaveFilePath           = "./net_static.json"
)

const maxSnapshotBytes int64 = 64 << 20

// NetStatic is the stable net_static.json representation.
type NetStatic struct {
	Interfaces map[string][]TrafficData `json:"interfaces"`
	Config     NetStaticConfig          `json:"config"`
}

type NetStaticConfig struct {
	DataPreserveDay float64  `json:"data_preserve_day"`
	DetectInterval  float64  `json:"detect_interval"`
	SaveInterval    float64  `json:"save_interval"`
	Nics            []string `json:"nics"`
}

type TrafficData struct {
	Timestamp uint64 `json:"timestamp"`
	Tx        uint64 `json:"tx"`
	Rx        uint64 `json:"rx"`
}

type counters struct {
	Tx uint64
	Rx uint64
}

// A prefixSeries answers an inclusive timestamp range in O(log n). Prefix
// values deliberately use uint64 modular arithmetic, matching the old summing
// behavior even for a malformed snapshot whose total overflows uint64.
type prefixSeries struct {
	timestamps []uint64
	txPrefix   []uint64
	rxPrefix   []uint64
}

type trafficIndex map[string]prefixSeries

type workerGeneration struct {
	id     uint64
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	config NetStaticConfig
}

var (
	mu          sync.RWMutex
	lifecycleMu sync.Mutex

	staticCache   = make(map[string][]TrafficData)
	store         = NetStatic{Interfaces: make(map[string][]TrafficData)}
	config        = configOrDefault(NetStaticConfig{})
	lastCounters  = make(map[string]counters)
	persistedTree = make(trafficIndex)
	pendingTree   = make(trafficIndex)

	running          bool
	activeGeneration *workerGeneration
	nextGenerationID uint64

	readIOCounters = gnet.IOCounters
	clockNow       = time.Now
	writeSnapshot  = persistSnapshot
)

func configOrDefault(c NetStaticConfig) NetStaticConfig {
	if !positiveFinite(c.DataPreserveDay) {
		c.DataPreserveDay = DefaultDataPreserveDay
	}
	if !validInterval(c.DetectInterval) {
		c.DetectInterval = DefaultDetectInterval
	}
	if !validInterval(c.SaveInterval) {
		c.SaveInterval = DefaultSaveInterval
	}
	c.Nics = cloneStrings(c.Nics)
	return c
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validInterval(seconds float64) bool {
	return positiveFinite(seconds) && time.Duration(seconds*float64(time.Second)) > 0
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func ensureInitLocked() {
	if store.Interfaces == nil {
		store.Interfaces = make(map[string][]TrafficData)
	}
	if staticCache == nil {
		staticCache = make(map[string][]TrafficData)
	}
	if lastCounters == nil {
		lastCounters = make(map[string]counters)
	}
	if persistedTree == nil {
		persistedTree = make(trafficIndex)
	}
	if pendingTree == nil {
		pendingTree = make(trafficIndex)
	}
	config = configOrDefault(config)
}

func cloneNetStatic(source NetStatic) NetStatic {
	result := NetStatic{
		Interfaces: make(map[string][]TrafficData, len(source.Interfaces)),
		Config:     configOrDefault(source.Config),
	}
	for name, series := range source.Interfaces {
		result.Interfaces[name] = append([]TrafficData(nil), series...)
	}
	return result
}

func normalizedRecords(source map[string][]TrafficData) map[string][]TrafficData {
	result := make(map[string][]TrafficData, len(source))
	for name, records := range source {
		if name == "" || len(records) == 0 {
			continue
		}
		series := append([]TrafficData(nil), records...)
		sort.SliceStable(series, func(i, j int) bool {
			return series[i].Timestamp < series[j].Timestamp
		})
		result[name] = series
	}
	return result
}

func buildTrafficIndex(source map[string][]TrafficData) trafficIndex {
	index := make(trafficIndex, len(source))
	for name, records := range source {
		if len(records) == 0 {
			continue
		}
		series := prefixSeries{
			timestamps: make([]uint64, len(records)),
			txPrefix:   make([]uint64, len(records)+1),
			rxPrefix:   make([]uint64, len(records)+1),
		}
		for i, record := range records {
			series.timestamps[i] = record.Timestamp
			series.txPrefix[i+1] = series.txPrefix[i] + record.Tx
			series.rxPrefix[i+1] = series.rxPrefix[i] + record.Rx
		}
		index[name] = series
	}
	return index
}

func appendIndex(index trafficIndex, name string, record TrafficData, source []TrafficData) {
	series, ok := index[name]
	if !ok || len(series.timestamps) == 0 {
		index[name] = prefixSeries{
			timestamps: []uint64{record.Timestamp},
			txPrefix:   []uint64{0, record.Tx},
			rxPrefix:   []uint64{0, record.Rx},
		}
		return
	}
	if record.Timestamp < series.timestamps[len(series.timestamps)-1] {
		index[name] = buildTrafficIndex(map[string][]TrafficData{name: source})[name]
		return
	}
	series.timestamps = append(series.timestamps, record.Timestamp)
	series.txPrefix = append(series.txPrefix, series.txPrefix[len(series.txPrefix)-1]+record.Tx)
	series.rxPrefix = append(series.rxPrefix, series.rxPrefix[len(series.rxPrefix)-1]+record.Rx)
	index[name] = series
}

func (series prefixSeries) sumBetween(start, end uint64) (uint64, uint64, bool) {
	left := 0
	if start != 0 {
		left = sort.Search(len(series.timestamps), func(i int) bool {
			return series.timestamps[i] >= start
		})
	}
	right := len(series.timestamps)
	if end != 0 {
		right = sort.Search(len(series.timestamps), func(i int) bool {
			return series.timestamps[i] > end
		})
	}
	if left >= right {
		return 0, 0, false
	}
	return series.txPrefix[right] - series.txPrefix[left], series.rxPrefix[right] - series.rxPrefix[left], true
}

func sumTrafficBetweenIndexed(persisted, pending trafficIndex, start, end uint64) map[string]TrafficData {
	result := make(map[string]TrafficData, len(persisted)+len(pending))
	addIndex := func(index trafficIndex) {
		for name, series := range index {
			tx, rx, found := series.sumBetween(start, end)
			if !found || (tx == 0 && rx == 0) {
				continue
			}
			current := result[name]
			current.Tx += tx
			current.Rx += rx
			result[name] = current
		}
	}
	addIndex(persisted)
	addIndex(pending)
	return result
}

// sumTrafficBetween is retained as the reference implementation for
// correctness tests and before/after benchmarks.
func sumTrafficBetween(persisted, pending map[string][]TrafficData, start, end uint64) map[string]TrafficData {
	result := make(map[string]TrafficData)
	inRange := func(timestamp uint64) bool {
		return (start == 0 || timestamp >= start) && (end == 0 || timestamp <= end)
	}
	addSource := func(source map[string][]TrafficData) {
		for name, records := range source {
			var tx, rx uint64
			for _, record := range records {
				if inRange(record.Timestamp) {
					tx += record.Tx
					rx += record.Rx
				}
			}
			if tx == 0 && rx == 0 {
				continue
			}
			current := result[name]
			current.Tx += tx
			current.Rx += rx
			result[name] = current
		}
	}
	addSource(persisted)
	addSource(pending)
	return result
}

func safeDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return 0
}

func nicAllowed(name string, nics []string) bool {
	if len(nics) == 0 {
		return true
	}
	for _, allowed := range nics {
		if name == allowed {
			return true
		}
	}
	return false
}

func applyCountersLocked(samples []gnet.IOCountersStat, timestamp uint64) {
	ensureInitLocked()
	for _, sample := range samples {
		if !nicAllowed(sample.Name, config.Nics) {
			continue
		}
		current := counters{Tx: sample.BytesSent, Rx: sample.BytesRecv}
		previous, exists := lastCounters[sample.Name]
		if exists {
			record := TrafficData{
				Timestamp: timestamp,
				Tx:        safeDelta(current.Tx, previous.Tx),
				Rx:        safeDelta(current.Rx, previous.Rx),
			}
			if record.Tx > 0 || record.Rx > 0 {
				staticCache[sample.Name] = append(staticCache[sample.Name], record)
				appendIndex(pendingTree, sample.Name, record, staticCache[sample.Name])
			}
		}
		lastCounters[sample.Name] = current
	}
}

func flushCacheLocked(timestamp uint64) {
	ensureInitLocked()
	for name, records := range staticCache {
		var tx, rx uint64
		for _, record := range records {
			tx += record.Tx
			rx += record.Rx
		}
		if tx == 0 && rx == 0 {
			continue
		}
		record := TrafficData{Timestamp: timestamp, Tx: tx, Rx: rx}
		store.Interfaces[name] = append(store.Interfaces[name], record)
		appendIndex(persistedTree, name, record, store.Interfaces[name])
	}
	staticCache = make(map[string][]TrafficData)
	pendingTree = make(trafficIndex)
}

func retentionDuration(days float64) time.Duration {
	hours := days * 24
	maxHours := float64(math.MaxInt64) / float64(time.Hour)
	if hours >= maxHours {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(hours * float64(time.Hour))
}

func purgeExpiredLocked(now time.Time) {
	ensureInitLocked()
	cutoffTime := now.Add(-retentionDuration(config.DataPreserveDay)).Unix()
	var cutoff uint64
	if cutoffTime > 0 {
		cutoff = uint64(cutoffTime)
	}
	for name, records := range store.Interfaces {
		first := sort.Search(len(records), func(i int) bool {
			return records[i].Timestamp >= cutoff
		})
		if first == len(records) {
			delete(store.Interfaces, name)
			continue
		}
		if first > 0 {
			store.Interfaces[name] = append([]TrafficData(nil), records[first:]...)
		}
	}
	persistedTree = buildTrafficIndex(store.Interfaces)
}

func snapshotLocked() NetStatic {
	ensureInitLocked()
	store.Config = configOrDefault(config)
	return cloneNetStatic(store)
}

func loadSnapshot(path string) (NetStatic, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NetStatic{}, false, nil
		}
		return NetStatic{}, false, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return NetStatic{}, false, err
	}
	if info.Size() > maxSnapshotBytes {
		return NetStatic{}, false, fmt.Errorf("netstatic snapshot exceeds %d bytes", maxSnapshotBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil {
		return NetStatic{}, false, err
	}
	if int64(len(data)) > maxSnapshotBytes {
		return NetStatic{}, false, fmt.Errorf("netstatic snapshot exceeds %d bytes", maxSnapshotBytes)
	}
	if len(data) == 0 {
		return NetStatic{}, false, nil
	}

	var snapshot NetStatic
	if err := json.Unmarshal(data, &snapshot); err != nil {
		if backupErr := backupCorruptSnapshot(path); backupErr != nil {
			return NetStatic{}, false, errors.Join(err, backupErr)
		}
		return NetStatic{}, false, nil
	}
	snapshot.Interfaces = normalizedRecords(snapshot.Interfaces)
	snapshot.Config = configOrDefault(snapshot.Config)
	return snapshot, true, nil
}

func backupCorruptSnapshot(path string) error {
	backup := path + ".bak"
	if _, err := os.Lstat(backup); err == nil {
		backup = fmt.Sprintf("%s.bak.%d", path, time.Now().UnixNano())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(path, backup)
}

func persistSnapshot(path string, snapshot NetStatic) (resultErr error) {
	started := time.Now()
	defer func() {
		diagnostics.ObserveNetstaticSave(started, resultErr)
	}()

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func installSnapshotLocked(snapshot NetStatic, loaded bool) {
	ensureInitLocked()
	if loaded {
		store = cloneNetStatic(snapshot)
		config = configOrDefault(snapshot.Config)
	} else {
		config = configOrDefault(config)
		store.Config = config
	}
	store.Interfaces = normalizedRecords(store.Interfaces)
	staticCache = make(map[string][]TrafficData)
	lastCounters = make(map[string]counters)
	pendingTree = make(trafficIndex)
	persistedTree = buildTrafficIndex(store.Interfaces)
	purgeExpiredLocked(clockNow())
}

func startGenerationLocked() {
	nextGenerationID++
	ctx, cancel := context.WithCancel(context.Background())
	generation := &workerGeneration{
		id:     nextGenerationID,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		config: configOrDefault(config),
	}
	activeGeneration = generation
	running = true
	go runGeneration(generation)
}

func generationIsActiveLocked(generation *workerGeneration) bool {
	return running && activeGeneration == generation
}

func runGeneration(generation *workerGeneration) {
	detectTicker := time.NewTicker(time.Duration(generation.config.DetectInterval * float64(time.Second)))
	saveTicker := time.NewTicker(time.Duration(generation.config.SaveInterval * float64(time.Second)))
	defer func() {
		detectTicker.Stop()
		saveTicker.Stop()
		close(generation.done)
	}()

	for {
		select {
		case <-generation.ctx.Done():
			return
		case <-detectTicker.C:
			samples, err := readIOCounters(true)
			if err != nil {
				continue
			}
			timestamp := uint64(clockNow().Unix())
			mu.Lock()
			if generationIsActiveLocked(generation) {
				applyCountersLocked(samples, timestamp)
			}
			mu.Unlock()
		case tick := <-saveTicker.C:
			mu.Lock()
			if !generationIsActiveLocked(generation) {
				mu.Unlock()
				continue
			}
			flushCacheLocked(uint64(tick.Unix()))
			purgeExpiredLocked(clockNow())
			snapshot := snapshotLocked()
			path := SaveFilePath
			mu.Unlock()
			_ = writeSnapshot(path, snapshot)
		}
	}
}

// GetNetStatic returns an immutable copy of persisted and pending traffic.
func GetNetStatic() (*NetStatic, error) {
	mu.RLock()
	defer mu.RUnlock()
	result := cloneNetStatic(NetStatic{Interfaces: store.Interfaces, Config: config})
	for name, records := range staticCache {
		result.Interfaces[name] = append(result.Interfaces[name], records...)
	}
	return &result, nil
}

// StartOrContinue loads the snapshot and starts exactly one worker generation.
func StartOrContinue() error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	mu.RLock()
	alreadyRunning := running
	path := SaveFilePath
	mu.RUnlock()
	if alreadyRunning {
		return nil
	}

	snapshot, loaded, err := loadSnapshot(path)
	if err != nil {
		return err
	}
	mu.Lock()
	installSnapshotLocked(snapshot, loaded)
	startGenerationLocked()
	mu.Unlock()
	return nil
}

// Clear removes in-memory traffic. The next periodic or final save persists it.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()
	ensureInitLocked()
	store.Interfaces = make(map[string][]TrafficData)
	staticCache = make(map[string][]TrafficData)
	lastCounters = make(map[string]counters)
	persistedTree = make(trafficIndex)
	pendingTree = make(trafficIndex)
	return nil
}

// Stop joins the active generation before producing the final durable snapshot.
func Stop() error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	mu.Lock()
	if !running {
		mu.Unlock()
		return nil
	}
	generation := activeGeneration
	activeGeneration = nil
	running = false
	mu.Unlock()

	if generation != nil {
		generation.cancel()
		<-generation.done
	}

	mu.Lock()
	flushCacheLocked(uint64(clockNow().Unix()))
	purgeExpiredLocked(clockNow())
	snapshot := snapshotLocked()
	path := SaveFilePath
	mu.Unlock()
	return writeSnapshot(path, snapshot)
}

// GetNetStaticBetween returns the records in the inclusive time range.
func GetNetStaticBetween(start, end uint64) (*NetStatic, error) {
	mu.RLock()
	defer mu.RUnlock()
	result := NetStatic{Interfaces: make(map[string][]TrafficData), Config: configOrDefault(config)}
	inRange := func(timestamp uint64) bool {
		return (start == 0 || timestamp >= start) && (end == 0 || timestamp <= end)
	}
	addSource := func(source map[string][]TrafficData) {
		for name, records := range source {
			for _, record := range records {
				if inRange(record.Timestamp) {
					result.Interfaces[name] = append(result.Interfaces[name], record)
				}
			}
		}
	}
	addSource(store.Interfaces)
	addSource(staticCache)
	return &result, nil
}

// GetTotalTraffic returns totals for all retained traffic.
func GetTotalTraffic() (map[string]TrafficData, error) {
	return GetTotalTrafficBetween(0, 0)
}

// GetTotalTrafficBetween answers an inclusive range from prefix indexes. Its
// cost is O(number of interfaces * log(records per interface)), independent of
// the number of retained buckets scanned by the old implementation.
func GetTotalTrafficBetween(start, end uint64) (map[string]TrafficData, error) {
	started := time.Now()
	mu.RLock()
	result := sumTrafficBetweenIndexed(persistedTree, pendingTree, start, end)
	mu.RUnlock()
	diagnostics.ObserveNetstaticQuery(started, nil)
	return result, nil
}

// SetNewConfig atomically replaces the worker generation when running. A
// detached generation is always joined before a new generation can start.
func SetNewConfig(newConfig NetStaticConfig) error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	mu.Lock()
	ensureInitLocked()
	wasRunning := running
	oldGeneration := activeGeneration
	if oldGeneration != nil {
		activeGeneration = nil
	}

	// Preserve pending traffic before changing NIC policy.
	flushCacheLocked(uint64(clockNow().Unix()))
	if newConfig.DataPreserveDay != 0 {
		config.DataPreserveDay = newConfig.DataPreserveDay
	}
	if newConfig.DetectInterval != 0 {
		config.DetectInterval = newConfig.DetectInterval
	}
	if newConfig.SaveInterval != 0 {
		config.SaveInterval = newConfig.SaveInterval
	}
	if newConfig.Nics != nil {
		config.Nics = cloneStrings(newConfig.Nics)
	}
	config = configOrDefault(config)
	store.Config = config
	purgeExpiredLocked(clockNow())

	if len(config.Nics) > 0 {
		for name := range lastCounters {
			if !nicAllowed(name, config.Nics) {
				delete(lastCounters, name)
			}
		}
	}
	snapshot := snapshotLocked()
	path := SaveFilePath
	mu.Unlock()

	if oldGeneration != nil {
		oldGeneration.cancel()
		<-oldGeneration.done
	}
	persistErr := writeSnapshot(path, snapshot)

	if wasRunning {
		mu.Lock()
		startGenerationLocked()
		mu.Unlock()
	}
	return persistErr
}

// ForceReplaceRecord replaces retained history using a defensive, sorted copy.
func ForceReplaceRecord(records map[string][]TrafficData) error {
	mu.Lock()
	defer mu.Unlock()
	ensureInitLocked()
	store.Interfaces = normalizedRecords(records)
	purgeExpiredLocked(clockNow())
	return nil
}
