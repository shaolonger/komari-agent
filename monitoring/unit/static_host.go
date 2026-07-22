package monitoring

import (
	"runtime"
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/cpu"
)

type StaticHostInfo struct {
	CPUName         string `json:"cpu_name"`
	CPUArchitecture string `json:"cpu_architecture"`
	CPUCores        int    `json:"cpu_cores"`
	OSName          string `json:"os_name"`
	KernelVersion   string `json:"kernel_version"`
	Virtualization  string `json:"virtualization"`
}

type staticHostInfoCache struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex
	loaded    bool
	value     StaticHostInfo
	loader    func() (StaticHostInfo, error)
}

var defaultStaticHostInfoCache = newStaticHostInfoCache(loadStaticHostInfo)

func GetStaticHostInfo() StaticHostInfo {
	return defaultStaticHostInfoCache.Get()
}

func RefreshStaticHostInfo() StaticHostInfo {
	return defaultStaticHostInfoCache.Refresh()
}

func newStaticHostInfoCache(loader func() (StaticHostInfo, error)) *staticHostInfoCache {
	return &staticHostInfoCache{loader: loader}
}

func (cache *staticHostInfoCache) Get() StaticHostInfo {
	cache.mu.RLock()
	value, loaded := cache.value, cache.loaded
	cache.mu.RUnlock()
	if loaded {
		return value
	}
	return cache.load(false)
}

func (cache *staticHostInfoCache) Refresh() StaticHostInfo {
	return cache.load(true)
}

func (cache *staticHostInfoCache) load(force bool) StaticHostInfo {
	cache.refreshMu.Lock()
	defer cache.refreshMu.Unlock()

	cache.mu.RLock()
	current, loaded := cache.value, cache.loaded
	cache.mu.RUnlock()
	if loaded && !force {
		return current
	}

	value, err := cache.loader()
	if err != nil {
		return current
	}
	cache.mu.Lock()
	cache.value = value
	cache.loaded = true
	cache.mu.Unlock()
	return value
}

func loadStaticHostInfo() (StaticHostInfo, error) {
	result := StaticHostInfo{
		CPUName:         "Unknown",
		CPUArchitecture: runtime.GOARCH,
		CPUCores:        1,
		OSName:          OSName(),
		KernelVersion:   KernelVersion(),
		Virtualization:  Virtualized(),
	}

	info, err := cpu.Info()
	if err == nil && len(info) > 0 {
		result.CPUName = strings.TrimSpace(info[0].ModelName)
		if result.CPUName == "" && (info[0].VendorID != "" || info[0].Family != "") {
			result.CPUName = strings.TrimSpace(info[0].VendorID + " " + info[0].Family)
		}
	}
	if result.CPUName == "Unknown" || result.CPUName == "" {
		if name, err := readCPUNameFromProc(); err == nil && name != "" {
			result.CPUName = strings.TrimSpace(name)
		}
	}
	if cores, err := cpu.Counts(true); err == nil && cores > 0 {
		result.CPUCores = cores
	}
	return result, nil
}
