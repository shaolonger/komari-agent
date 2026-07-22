package monitoring

import (
	"bufio"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/shirou/gopsutil/v4/cpu"
)

var flags = pkg_flags.GlobalConfig

type CpuInfo struct {
	CPUName         string  `json:"cpu_name"`
	CPUArchitecture string  `json:"cpu_architecture"`
	CPUCores        int     `json:"cpu_cores"`
	CPUUsage        float64 `json:"cpu_usage"`
}

func Cpu() CpuInfo {
	cpuinfo := CpuInfo{
		CPUName:         "Unknown",
		CPUArchitecture: runtime.GOARCH,
		CPUCores:        1,
		CPUUsage:        0.0,
	}

	// 优先使用 gopsutil 获取 CPU 信息，避免触发 lscpu 在部分内核上的 lockdown 日志刷屏。
	info, err := cpu.Info()
	if err == nil && len(info) > 0 {
		cpuinfo.CPUName = strings.TrimSpace(info[0].ModelName)
		if cpuinfo.CPUName == "" {
			if info[0].VendorID != "" || info[0].Family != "" {
				cpuinfo.CPUName = strings.TrimSpace(info[0].VendorID + " " + info[0].Family)
			}
		}
	}

	if cpuinfo.CPUName == "Unknown" {
		name, err := readCPUNameFromProc()
		if err == nil && name != "" {
			cpuinfo.CPUName = strings.TrimSpace(name)
		}
	}

	cores, err := cpu.Counts(true)
	if err == nil {
		cpuinfo.CPUCores = cores
	}

	percentages, err := cpu.Percent(1*time.Second, false)
	if err == nil && len(percentages) > 0 {
		cpuinfo.CPUUsage = percentages[0]
	}

	return cpuinfo
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
