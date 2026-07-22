package monitoring

// Modified from https://github.com/influxdata/telegraf/blob/master/plugins/inputs/nvidia_smi/nvidia_smi.go
// Original License: MIT

import (
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type NvidiaSMI struct {
	BinPath string
	data    []byte
}

// NVIDIAGPUInfo 包含详细的NVIDIA GPU信息
type NVIDIAGPUInfo struct {
	Name        string  // GPU型号
	MemoryTotal uint64  // 总显存 (字节)
	MemoryUsed  uint64  // 已用显存 (字节)
	Utilization float64 // GPU使用率 (0-100)
	Temperature uint64  // 温度 (摄氏度)
}

type nvidiaGPUProvider struct {
	path   string
	runner gpuCommandRunner
}

func (provider *nvidiaGPUProvider) Static(ctx context.Context) ([]gpuDeviceStatic, error) {
	output, err := provider.runner.Run(ctx, provider.path,
		"--query-gpu=uuid,name,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, err
	}
	return parseNvidiaStaticCSV(output)
}

func (provider *nvidiaGPUProvider) Dynamic(ctx context.Context, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
	output, err := provider.runner.Run(ctx, provider.path,
		"--query-gpu=uuid,memory.used,utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, err
	}
	return parseNvidiaDynamicCSV(output, metadata)
}

func parseNvidiaStaticCSV(output []byte) ([]gpuDeviceStatic, error) {
	if err := validateGPUCommandOutput(output); err != nil {
		return nil, err
	}
	records, err := readGPUCSV(output, 3)
	if err != nil {
		return nil, err
	}
	result := make([]gpuDeviceStatic, 0, len(records))
	for _, record := range records {
		memoryMiB, err := strconv.ParseUint(strings.TrimSpace(record[2]), 10, 64)
		if err != nil || memoryMiB > ^uint64(0)/(1024*1024) {
			return nil, errors.New("invalid NVIDIA total memory")
		}
		result = append(result, gpuDeviceStatic{
			id:          strings.TrimSpace(record[0]),
			name:        strings.TrimSpace(record[1]),
			memoryTotal: memoryMiB * 1024 * 1024,
		})
	}
	return result, validateGPUStatic(result)
}

func parseNvidiaDynamicCSV(output []byte, metadata []gpuDeviceStatic) ([]DetailedGPUInfo, error) {
	if err := validateGPUCommandOutput(output); err != nil {
		return nil, err
	}
	records, err := readSimpleGPUCSV(output, 4)
	if err != nil {
		return nil, err
	}
	dynamic := make(map[string][3]string, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record[0])
		if _, exists := dynamic[id]; exists {
			return nil, errors.New("duplicate NVIDIA device ID")
		}
		dynamic[id] = [3]string{record[1], record[2], record[3]}
	}
	if len(dynamic) != len(metadata) {
		return nil, errGPUTopologyChanged
	}
	result := make([]DetailedGPUInfo, 0, len(metadata))
	for _, device := range metadata {
		values, exists := dynamic[device.id]
		if !exists {
			return nil, errGPUTopologyChanged
		}
		memoryMiB, err := parseNvidiaOptionalUint(values[0])
		if err != nil || memoryMiB > ^uint64(0)/(1024*1024) {
			return nil, errors.New("invalid NVIDIA used memory")
		}
		utilization, err := parseNvidiaOptionalFloat(values[1])
		if err != nil {
			return nil, errors.New("invalid NVIDIA utilization")
		}
		temperature, err := parseNvidiaOptionalUint(values[2])
		if err != nil {
			return nil, errors.New("invalid NVIDIA temperature")
		}
		result = append(result, DetailedGPUInfo{
			Name:        device.name,
			MemoryTotal: device.memoryTotal,
			MemoryUsed:  memoryMiB * 1024 * 1024,
			Utilization: utilization,
			Temperature: temperature,
		})
	}
	return result, validateDetailedGPUInfo(result)
}

func readSimpleGPUCSV(output []byte, fields int) ([][]string, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil, errors.New("GPU CSV contained no devices")
	}
	if len(lines) > maximumGPUCount {
		return nil, fmt.Errorf("GPU provider returned more than %d devices", maximumGPUCount)
	}
	result := make([][]string, 0, len(lines))
	for _, line := range lines {
		record := strings.Split(line, ",")
		if len(record) != fields {
			return nil, fmt.Errorf("GPU CSV record has %d fields, want %d", len(record), fields)
		}
		result = append(result, record)
	}
	return result, nil
}

func parseNvidiaOptionalFloat(value string) (float64, error) {
	if isUnavailableNvidiaValue(value) {
		return 0, nil
	}
	return strconv.ParseFloat(strings.TrimSpace(value), 64)
}

func parseNvidiaOptionalUint(value string) (uint64, error) {
	if isUnavailableNvidiaValue(value) {
		return 0, nil
	}
	return strconv.ParseUint(strings.TrimSpace(value), 10, 64)
}

func isUnavailableNvidiaValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "n/a", "[n/a]", "not supported", "-":
		return true
	default:
		return false
	}
}

func readGPUCSV(output []byte, fields int) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(string(output)))
	reader.FieldsPerRecord = fields
	reader.TrimLeadingSpace = true
	result := make([][]string, 0, 4)
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse GPU CSV: %w", err)
		}
		if len(result) >= maximumGPUCount {
			return nil, fmt.Errorf("GPU provider returned more than %d devices", maximumGPUCount)
		}
		result = append(result, record)
	}
	if len(result) == 0 {
		return nil, errors.New("GPU CSV contained no devices")
	}
	return result, nil
}

func (smi *NvidiaSMI) GatherModel() ([]string, error) {
	return smi.gatherModel()
}

func (smi *NvidiaSMI) GatherUsage() ([]float64, error) {
	return smi.gatherUsage()
}

// GatherDetailedInfo 获取详细GPU信息
func (smi *NvidiaSMI) GatherDetailedInfo() ([]NVIDIAGPUInfo, error) {
	return smi.gatherDetailedInfo()
}

func (smi *NvidiaSMI) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGPUCommandTimeout)
	defer cancel()
	return smi.StartContext(ctx)
}

func (smi *NvidiaSMI) StartContext(ctx context.Context) error {
	if _, err := os.Stat(smi.BinPath); os.IsNotExist(err) {
		binPath, err := exec.LookPath("nvidia-smi")
		if err != nil {
			return errors.New("nvidia-smi tool not found")
		}
		smi.BinPath = binPath
	}
	output, err := (execGPUCommandRunner{}).Run(ctx, smi.BinPath, "-q", "-x")
	if err != nil {
		return err
	}
	smi.data = output
	return nil
}

func (smi *NvidiaSMI) gatherModel() ([]string, error) {
	if err := validateGPUCommandOutput(smi.data); err != nil {
		return nil, err
	}
	var stats nvidiaSMIXMLResult
	var models []string

	if err := xml.Unmarshal(smi.data, &stats); err != nil {
		return nil, err
	}
	if len(stats.GPUs) > maximumGPUCount {
		return nil, errors.New("NVIDIA XML exceeded the GPU limit")
	}

	for _, gpu := range stats.GPUs {
		if gpu.ProductName != "" {
			models = append(models, gpu.ProductName)
		}
	}

	return models, nil
}

func (smi *NvidiaSMI) gatherUsage() ([]float64, error) {
	if err := validateGPUCommandOutput(smi.data); err != nil {
		return nil, err
	}
	var stats nvidiaSMIXMLResult
	var usageList []float64

	if err := xml.Unmarshal(smi.data, &stats); err != nil {
		return nil, err
	}
	if len(stats.GPUs) > maximumGPUCount {
		return nil, errors.New("NVIDIA XML exceeded the GPU limit")
	}

	for _, gpu := range stats.GPUs {
		usage, err := parsePercentageValue(gpu.Utilization.GPUUtil)
		if err != nil {
			usage = 0.0 // 默认为0，不中断处理
		}
		usageList = append(usageList, usage)
	}

	return usageList, nil
}

func (smi *NvidiaSMI) gatherDetailedInfo() ([]NVIDIAGPUInfo, error) {
	if err := validateGPUCommandOutput(smi.data); err != nil {
		return nil, err
	}
	var stats nvidiaSMIXMLResult
	var gpuInfos []NVIDIAGPUInfo

	if err := xml.Unmarshal(smi.data, &stats); err != nil {
		return nil, err
	}
	if len(stats.GPUs) > maximumGPUCount {
		return nil, errors.New("NVIDIA XML exceeded the GPU limit")
	}

	for _, gpu := range stats.GPUs {
		utilization, _ := parsePercentageValue(gpu.Utilization.GPUUtil)
		memTotal, _ := parseMemoryValue(gpu.FrameBufferMemoryUsage.Total)
		memUsed, _ := parseMemoryValue(gpu.FrameBufferMemoryUsage.Used)
		temp, _ := parseTemperatureValue(gpu.Temperature.GPUTemp)

		gpuInfo := NVIDIAGPUInfo{
			Name:        gpu.ProductName,
			MemoryTotal: memTotal,
			MemoryUsed:  memUsed,
			Utilization: utilization,
			Temperature: temp,
		}

		gpuInfos = append(gpuInfos, gpuInfo)
	}

	return gpuInfos, nil
}

// 解析百分比值 (例如 "25 %" -> 25.0)
func parsePercentageValue(value string) (float64, error) {
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

// 解析内存值 (例如 "1024 MiB" -> 1073741824字节)
func parseMemoryValue(value string) (uint64, error) {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.TrimSuffix(cleaned, "MiB")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return 0, nil
	}

	result, err := strconv.ParseUint(cleaned, 10, 64)
	if err != nil {
		return 0, err
	}

	// 转换MiB为字节 (1 MiB = 1024*1024 bytes)
	return result * 1024 * 1024, nil
}

// 解析温度值 (例如 "65 C" -> 65)
func parseTemperatureValue(value string) (uint64, error) {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.TrimSuffix(cleaned, "C")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return 0, nil
	}

	result, err := strconv.ParseUint(cleaned, 10, 64)
	if err != nil {
		return 0, err
	}

	return result, nil
}

// NVIDIA-SMI XML结构定义
type nvidiaSMIXMLResult struct {
	GPUs []nvidiaSMIGPU `xml:"gpu"`
}

type nvidiaSMIGPU struct {
	ProductName string `xml:"product_name"`
	Utilization struct {
		GPUUtil string `xml:"gpu_util"`
	} `xml:"utilization"`
	FrameBufferMemoryUsage struct {
		Total string `xml:"total"`
		Used  string `xml:"used"`
		Free  string `xml:"free"`
	} `xml:"fb_memory_usage"`
	Temperature struct {
		GPUTemp string `xml:"gpu_temp"`
	} `xml:"temperature"`
}
