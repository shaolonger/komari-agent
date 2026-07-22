package monitoring

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari-agent/monitoring/netstatic"
	"github.com/komari-monitor/komari-agent/utils"
	"github.com/shirou/gopsutil/v4/net"
)

type networkIOSource func(bool) ([]net.IOCountersStat, error)

type networkCounter struct {
	up   uint64
	down uint64
}

type networkSampler struct {
	mu         sync.Mutex
	source     networkIOSource
	now        func() time.Time
	previous   map[string]networkCounter
	previousAt time.Time
}

var defaultNetworkSampler = newNetworkSampler(net.IOCounters, time.Now)

var (
	// 预定义常见的回环和虚拟接口名称
	loopbackNames = map[string]struct{}{
		"br":      {},
		"cni":     {},
		"docker":  {},
		"podman":  {},
		"flannel": {},
		"lo":      {},
		"veth":    {}, // Docker
		"virbr":   {}, // KVM
		"vmbr":    {}, // Proxmox
		"tap":     {},
		"fwbr":    {},
		"fwpr":    {},
	}
)

// VnstatInterface represents a network interface in vnstat output
type VnstatInterface struct {
	Name    string        `json:"name"`
	Alias   string        `json:"alias"`
	Created VnstatDate    `json:"created"`
	Updated VnstatUpdated `json:"updated"`
	Traffic VnstatTraffic `json:"traffic"`
}

// VnstatDate represents date information
type VnstatDate struct {
	Date      VnstatDateInfo `json:"date"`
	Timestamp int64          `json:"timestamp"`
}

// VnstatUpdated represents updated information
type VnstatUpdated struct {
	Date      VnstatDateInfo `json:"date"`
	Time      VnstatTimeInfo `json:"time"`
	Timestamp int64          `json:"timestamp"`
}

// VnstatDateInfo represents date components
type VnstatDateInfo struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

// VnstatTimeInfo represents time components
type VnstatTimeInfo struct {
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}

// VnstatTraffic represents traffic data from vnstat
type VnstatTraffic struct {
	Total      VnstatTotal        `json:"total"`
	FiveMinute []VnstatTimeEntry  `json:"fiveminute"`
	Hour       []VnstatTimeEntry  `json:"hour"`
	Day        []VnstatTimeEntry  `json:"day"`
	Month      []VnstatMonthEntry `json:"month"`
	Year       []VnstatYearEntry  `json:"year"`
	Top        []VnstatTimeEntry  `json:"top"`
}

// VnstatTotal represents total traffic data
type VnstatTotal struct {
	Rx uint64 `json:"rx"`
	Tx uint64 `json:"tx"`
}

// VnstatTimeEntry represents a time-based traffic entry
type VnstatTimeEntry struct {
	ID        int            `json:"id"`
	Date      VnstatDateInfo `json:"date"`
	Time      VnstatTimeInfo `json:"time,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Rx        uint64         `json:"rx"`
	Tx        uint64         `json:"tx"`
}

// VnstatMonthEntry represents a monthly traffic entry
type VnstatMonthEntry struct {
	ID        int            `json:"id"`
	Date      VnstatDateInfo `json:"date"`
	Timestamp int64          `json:"timestamp"`
	Rx        uint64         `json:"rx"`
	Tx        uint64         `json:"tx"`
}

// VnstatYearEntry represents a yearly traffic entry
type VnstatYearEntry struct {
	ID        int            `json:"id"`
	Date      VnstatDateInfo `json:"date"`
	Timestamp int64          `json:"timestamp"`
	Rx        uint64         `json:"rx"`
	Tx        uint64         `json:"tx"`
}

// VnstatOutput represents the complete vnstat JSON output
type VnstatOutput struct {
	VnstatVersion string            `json:"vnstatversion"`
	JsonVersion   string            `json:"jsonversion"`
	Interfaces    []VnstatInterface `json:"interfaces"`
}

func NetworkSpeed() (totalUp, totalDown, upSpeed, downSpeed uint64, err error) {
	includeNics := parseNics(flags.IncludeNics)
	excludeNics := parseNics(flags.ExcludeNics)
	totalUp, totalDown, upSpeed, downSpeed, err = defaultNetworkSampler.Sample(includeNics, excludeNics)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	// 如果设置了月重置（非0），统计totalUp、totalDown
	if flags.MonthRotate != 0 {
		netstatic.StartOrContinue() // 确保netstatic在运行
		now := uint64(time.Now().Unix())
		resetDay := uint64(utils.GetLastResetDate(flags.MonthRotate, time.Now()).Unix())
		nicStatics, err := netstatic.GetTotalTrafficBetween(resetDay, now)
		if err != nil {
			return totalUp, totalDown, upSpeed, downSpeed, fmt.Errorf("failed to call GetTotalTrafficBetween: %w", err)
		}

		monthlyUp, monthlyDown := uint64(0), uint64(0)
		for interfaceName, stats := range nicStatics {
			if shouldInclude(interfaceName, includeNics, excludeNics) {
				monthlyUp += stats.Tx
				monthlyDown += stats.Rx
			}
		}
		return monthlyUp, monthlyDown, upSpeed, downSpeed, nil
	}

	return totalUp, totalDown, upSpeed, downSpeed, nil
}

func getNetworkSpeedFallback(includeNics, excludeNics map[string]struct{}) (totalUp, totalDown, upSpeed, downSpeed uint64, err error) {
	return defaultNetworkSampler.Sample(includeNics, excludeNics)
}

func newNetworkSampler(source networkIOSource, now func() time.Time) *networkSampler {
	return &networkSampler{
		source:   source,
		now:      now,
		previous: make(map[string]networkCounter),
	}
}

func (sampler *networkSampler) Sample(includeNics, excludeNics map[string]struct{}) (totalUp, totalDown, upSpeed, downSpeed uint64, err error) {
	counters, err := sampler.source(true)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("failed to get network IO counters: %w", err)
	}
	if len(counters) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("no network interfaces found")
	}
	now := sampler.now()
	current := make(map[string]networkCounter, len(counters))
	for _, counter := range counters {
		if !shouldInclude(counter.Name, includeNics, excludeNics) {
			continue
		}
		current[counter.Name] = networkCounter{up: counter.BytesSent, down: counter.BytesRecv}
		totalUp += counter.BytesSent
		totalDown += counter.BytesRecv
	}

	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	if sampler.previousAt.IsZero() || !now.After(sampler.previousAt) {
		sampler.previous = current
		sampler.previousAt = now
		return totalUp, totalDown, 0, 0, nil
	}
	elapsedSeconds := now.Sub(sampler.previousAt).Seconds()
	for name, value := range current {
		previous, exists := sampler.previous[name]
		if !exists {
			continue
		}
		if value.up >= previous.up {
			upSpeed += uint64(float64(value.up-previous.up) / elapsedSeconds)
		}
		if value.down >= previous.down {
			downSpeed += uint64(float64(value.down-previous.down) / elapsedSeconds)
		}
	}
	sampler.previous = current
	sampler.previousAt = now
	return totalUp, totalDown, upSpeed, downSpeed, nil
}

func parseNics(nics string) map[string]struct{} {
	if nics == "" {
		return nil
	}
	nicSet := make(map[string]struct{})
	for _, nic := range strings.Split(nics, ",") {
		nicSet[strings.TrimSpace(nic)] = struct{}{}
	}
	return nicSet
}

func shouldInclude(nicName string, includeNics, excludeNics map[string]struct{}) bool {
	// 默认排除回环接口
	for loopbackName := range loopbackNames {
		if strings.HasPrefix(nicName, loopbackName) {
			return false
		}
	}

	// 如果定义了白名单，则只包括白名单中的接口
	if len(includeNics) > 0 {
		_, ok := includeNics[nicName]
		return ok
	}

	// 如果定义了黑名单，则排除黑名单中的接口
	if len(excludeNics) > 0 {
		if _, ok := excludeNics[nicName]; ok {
			return false
		}
	}

	return true
}

func InterfaceList() ([]string, error) {
	includeNics := parseNics(flags.IncludeNics)
	excludeNics := parseNics(flags.ExcludeNics)
	interfaces := []string{}

	ioCounters, err := net.IOCounters(true)
	if err != nil {
		return nil, err
	}
	for _, interfaceStats := range ioCounters {
		if shouldInclude(interfaceStats.Name, includeNics, excludeNics) {
			interfaces = append(interfaces, interfaceStats.Name)
		}
	}
	return interfaces, nil
}
