package monitoring

import (
	"bufio"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/shirou/gopsutil/v4/cpu"
)

var flags = pkg_flags.GlobalConfig

type cpuTimesSource func(bool) ([]cpu.TimesStat, error)

type cpuUsageSampler struct {
	mu       sync.Mutex
	source   cpuTimesSource
	previous *cpu.TimesStat
}

var defaultCPUUsageSampler = newCPUUsageSampler(cpu.Times)

type CpuInfo struct {
	CPUName         string  `json:"cpu_name"`
	CPUArchitecture string  `json:"cpu_architecture"`
	CPUCores        int     `json:"cpu_cores"`
	CPUUsage        float64 `json:"cpu_usage"`
}

func Cpu() CpuInfo {
	static := GetStaticHostInfo()
	cpuinfo := CpuInfo{
		CPUName:         static.CPUName,
		CPUArchitecture: static.CPUArchitecture,
		CPUCores:        static.CPUCores,
		CPUUsage:        0.0,
	}

	usage, err := defaultCPUUsageSampler.Sample()
	if err == nil {
		cpuinfo.CPUUsage = usage
	}

	return cpuinfo
}

func newCPUUsageSampler(source cpuTimesSource) *cpuUsageSampler {
	return &cpuUsageSampler{source: source}
}

func (sampler *cpuUsageSampler) Sample() (float64, error) {
	times, err := sampler.source(false)
	if err != nil {
		return 0, err
	}
	if len(times) == 0 {
		return 0, nil
	}
	current := times[0]

	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	if sampler.previous == nil {
		sampler.previous = &current
		return 0, nil
	}
	previous := *sampler.previous
	sampler.previous = &current
	return calculateCPUUsage(previous, current), nil
}

func calculateCPUUsage(previous, current cpu.TimesStat) float64 {
	previousTotal, previousBusy := cpuTotals(previous)
	currentTotal, currentBusy := cpuTotals(current)
	totalDelta := currentTotal - previousTotal
	busyDelta := currentBusy - previousBusy
	if totalDelta <= 0 || busyDelta <= 0 {
		return 0
	}
	return math.Min(100, math.Max(0, busyDelta/totalDelta*100))
}

func cpuTotals(times cpu.TimesStat) (float64, float64) {
	total := times.User + times.System + times.Idle + times.Nice + times.Iowait + times.Irq +
		times.Softirq + times.Steal + times.Guest + times.GuestNice
	if runtime.GOOS == "linux" {
		total -= times.Guest
		total -= times.GuestNice
	}
	return total, total - times.Idle - times.Iowait
}

// readCPUNameFromProc 从 /proc/cpuinfo 读取 CPU 名称
func readCPUNameFromProc() (string, error) {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", err
	}
	defer file.Close()

	return readCPUName(file)
}

func readCPUName(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(reader)
	processorName := ""
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch key {
		case "model", "model name", "hardware":
			if value != "" {
				return value, nil
			}
		case "processor":
			if value != "" {
				if _, err := strconv.ParseUint(value, 10, 64); err != nil && processorName == "" {
					processorName = value
				}
			}
		}
	}

	return processorName, scanner.Err()
}
