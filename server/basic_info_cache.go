package server

import (
	"sync"
	"sync/atomic"

	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
	"github.com/komari-monitor/komari-agent/update"
)

type staticBasicInfo struct {
	CPUName        string
	CPUCores       int
	Architecture   string
	OSName         string
	KernelVersion  string
	GPUName        string
	Virtualization string
	AgentVersion   string
}

type staticBasicInfoCache struct {
	value  atomic.Pointer[staticBasicInfo]
	mu     sync.Mutex
	loader func() staticBasicInfo
}

var defaultStaticBasicInfoCache = newStaticBasicInfoCache(loadStaticBasicInfo)

func newStaticBasicInfoCache(loader func() staticBasicInfo) *staticBasicInfoCache {
	return &staticBasicInfoCache{loader: loader}
}

func (cache *staticBasicInfoCache) Get() staticBasicInfo {
	if cached := cache.value.Load(); cached != nil {
		return *cached
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cached := cache.value.Load(); cached != nil {
		return *cached
	}
	loaded := cache.loader()
	cache.value.Store(&loaded)
	return loaded
}

func (cache *staticBasicInfoCache) Invalidate() {
	cache.mu.Lock()
	cache.value.Store(nil)
	cache.mu.Unlock()
}

func loadStaticBasicInfo() staticBasicInfo {
	host := monitoring.GetStaticHostInfo()
	return staticBasicInfo{
		CPUName:        host.CPUName,
		CPUCores:       host.CPUCores,
		Architecture:   host.CPUArchitecture,
		OSName:         host.OSName,
		KernelVersion:  host.KernelVersion,
		GPUName:        monitoring.GpuName(),
		Virtualization: host.Virtualization,
		AgentVersion:   update.CurrentVersion,
	}
}

// InvalidateBasicInfoCache is the event hook for a future hardware/config
// reload. Normal reconnects intentionally reuse the same static snapshot.
func InvalidateBasicInfoCache() {
	defaultStaticBasicInfoCache.Invalidate()
}
