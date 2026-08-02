package monitoring

// Modified from https://github.com/influxdata/telegraf/blob/master/plugins/inputs/amd_rocm_smi/amd_rocm_smi.go
// Original License: MIT

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type ROCmSMI struct {
	BinPath string
	data    []byte
}

// AMDGPUInfo AMD GPU详细信息
type AMDGPUInfo struct {
	Name        string  // GPU型号
	MemoryTotal uint64  // 总显存 (字节)
	MemoryUsed  uint64  // 已用显存 (字节)
	Utilization float64 // GPU使用率 (0-100)
	Temperature uint64  // 温度 (摄氏度)
}

// ROCmSMI JSON响应结构
type ROCmResponse map[string]ROCmGPUInfo

type ROCmGPUInfo struct {
	CardSeries          string `json:"Card series"`
	GPUUsage            string `json:"GPU use (%)"`
	VRAMTotalMemory     string `json:"VRAM Total Memory (B)"`
	VRAMTotalUsedMemory string `json:"VRAM Total Used Memory (B)"`
	TemperatureJunction string `json:"Temperature (Sensor junction) (C)"`
	TemperatureEdge     string `json:"Temperature (Sensor edge) (C)"`
	TemperatureMemory   string `json:"Temperature (Sensor memory) (C)"`
}

type amdGPUProvider struct {
	path   string
	runner gpuCommandRunner
}

func (provider *amdGPUProvider) Static(ctx context.Context) ([]gpuDeviceStatic, error) {
	output, err := provider.runner.Run(ctx, provider.path,
		"--showproductname", "--showmeminfo", "vram", "--json")
	if err != nil {
		return nil, err
	}
	data, keys, err := parseROCmResponse(output)
	if err != nil {
		return nil, err
	}
	result := make([]gpuDeviceStatic, 0, len(keys))
	for _, key := range keys {
		card := data[key]
		memoryTotal, err := parseAMDMemoryBytes(card.VRAMTotalMemory)
		if err != nil {
			return nil, errors.New("invalid AMD total memory")
		}
		result = append(result, gpuDeviceStatic{id: key, name: strings.TrimSpace(card.CardSeries), memoryTotal: memoryTotal})
	}
	return result, validateGPUStatic(result)
}

func (provider *amdGPUProvider) Dynamic(ctx context.Context, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
	output, err := provider.runner.Run(ctx, provider.path,
		"--showuse", "--showmeminfo", "vram", "--showtemp", "--json")
	if err != nil {
		return nil, err
	}
	return parseAMDDynamicResponse(output, metadata)
}

func parseAMDDynamicResponse(output []byte, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
	data, keys, err := parseROCmResponse(output)
	if err != nil {
		return nil, err
	}
	if len(keys) != len(metadata) {
		return nil, errGPUTopologyChanged
	}
	result := make([]DetailedGPUInfo, 0, len(metadata))
	for _, device := range metadata {
		card, exists := data[device.id]
		if !exists {
			return nil, errGPUTopologyChanged
		}
		memoryUsed, err := parseAMDMemoryBytes(card.VRAMTotalUsedMemory)
		if err != nil {
			return nil, errors.New("invalid AMD used memory")
		}
		utilization, err := parseAMDPercentage(card.GPUUsage)
		if err != nil {
			return nil, errors.New("invalid AMD utilization")
		}
		temperature, err := parseAMDTemperature(firstNonEmpty(card.TemperatureJunction, card.TemperatureEdge, card.TemperatureMemory))
		if err != nil {
			return nil, errors.New("invalid AMD temperature")
		}
		result = append(result, DetailedGPUInfo{
			Name:        device.name,
			MemoryTotal: device.memoryTotal,
			MemoryUsed:  memoryUsed,
			Utilization: utilization,
			Temperature: temperature,
		})
	}
	return result, validateDetailedGPUInfo(result)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseROCmResponse(output []byte) (ROCmResponse, []string, error) {
	if err := validateGPUCommandOutput(output); err != nil {
		return nil, nil, err
	}
	var data ROCmResponse
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(data))
	for key := range data {
		if strings.HasPrefix(key, "card") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return nil, nil, errors.New("ROCm response contained no devices")
	}
	if len(keys) > maximumGPUCount {
		return nil, nil, errors.New("ROCm response exceeded the GPU limit")
	}
	return data, keys, nil
}

func (rsmi *ROCmSMI) GatherModel() ([]string, error) {
	return rsmi.gatherModel()
}

func (rsmi *ROCmSMI) GatherUsage() ([]float64, error) {
	return rsmi.gatherUsage()
}

// GatherDetailedInfo 获取详细GPU信息
func (rsmi *ROCmSMI) GatherDetailedInfo() ([]AMDGPUInfo, error) {
	return rsmi.gatherDetailedInfo()
}

func (rsmi *ROCmSMI) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGPUCommandTimeout)
	defer cancel()
	return rsmi.StartContext(ctx)
}

func (rsmi *ROCmSMI) StartContext(ctx context.Context) error {
	if _, err := os.Stat(rsmi.BinPath); os.IsNotExist(err) {
		binPath, err := exec.LookPath("rocm-smi")
		if err != nil {
			return errors.New("rocm-smi tool not found")
		}
		rsmi.BinPath = binPath
	}

	output, err := (execGPUCommandRunner{}).Run(ctx, rsmi.BinPath, "--showallinfo", "--json")
	if err != nil {
		return err
	}
	rsmi.data = output
	return nil
}

func (rsmi *ROCmSMI) gatherModel() ([]string, error) {
	data, keys, err := parseROCmResponse(rsmi.data)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(keys))
	for _, key := range keys {
		if name := strings.TrimSpace(data[key].CardSeries); name != "" {
			models = append(models, name)
		}
	}
	return models, nil
}

func (rsmi *ROCmSMI) gatherUsage() ([]float64, error) {
	data, keys, err := parseROCmResponse(rsmi.data)
	if err != nil {
		return nil, err
	}
	usageList := make([]float64, 0, len(keys))
	for _, key := range keys {
		usage, err := parseAMDPercentage(data[key].GPUUsage)
		if err != nil {
			usage = 0
		}
		usageList = append(usageList, usage)
	}
	return usageList, nil
}

func (rsmi *ROCmSMI) gatherDetailedInfo() ([]AMDGPUInfo, error) {
	if rsmi.data == nil {
		return nil, errors.New("no data available")
	}

	data, keys, err := parseROCmResponse(rsmi.data)
	if err != nil {
		return nil, err
	}
	gpuInfos := make([]AMDGPUInfo, 0, len(keys))
	for _, key := range keys {
		card := data[key]
		usage, _ := parseAMDPercentage(card.GPUUsage)
		memoryUsed, _ := parseAMDMemoryBytes(card.VRAMTotalUsedMemory)
		memoryTotal, _ := parseAMDMemoryBytes(card.VRAMTotalMemory)
		temperature, _ := parseAMDTemperature(firstNonEmpty(card.TemperatureJunction, card.TemperatureEdge, card.TemperatureMemory))
		gpuInfos = append(gpuInfos, AMDGPUInfo{
			Name: card.CardSeries, MemoryTotal: memoryTotal, MemoryUsed: memoryUsed,
			Utilization: usage, Temperature: temperature,
		})
	}
	return gpuInfos, nil
}

// 解析AMD百分比值 (例如 "25" -> 25.0)
func parseAMDPercentage(value string) (float64, error) {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.TrimSuffix(cleaned, "%")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return 0.0, nil
	}

	result, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0.0, err
	}

	return result, nil
}

// 解析AMD显存字节 (例如 "1073741824" -> 1073741824字节)
func parseAMDMemoryBytes(value string) (uint64, error) {
	cleaned := strings.TrimSpace(value)

	if cleaned == "" {
		return 0, nil
	}

	bytes, err := strconv.ParseUint(cleaned, 10, 64)
	if err != nil {
		return 0, err
	}

	// 直接返回字节数
	return bytes, nil
}

// 解析AMD温度值 (例如 "65" -> 65)
func parseAMDTemperature(value string) (uint64, error) {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.TrimSuffix(cleaned, "C")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return 0, nil
	}

	result, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(result) || math.IsInf(result, 0) || result < 0 || result > 1000 {
		return 0, errors.New("invalid AMD temperature")
	}

	return uint64(math.Round(result)), nil
}
