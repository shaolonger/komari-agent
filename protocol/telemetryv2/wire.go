package telemetryv2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

const (
	Subprotocol       = "komari.telemetry.v2"
	LegacySubprotocol = "komari.telemetry.v1"
	Version           = uint8(2)
	HeaderSize        = 16
	MaxFrameSize      = 64 * 1024
	MaxMessageSize    = 4 * 1024
	MaxGPUCount       = 64
	MaxGPUNameSize    = 256
	SchemaID          = uint32(0x32525054) // little-endian bytes spell "TPR2"
)

var frameMagic = [4]byte{'K', 'M', 'R', '2'}

const (
	flagGPU         = uint8(1 << 0)
	flagGPUDetailed = uint8(1 << 1)
	knownFlags      = flagGPU | flagGPUDetailed
)

type Memory struct {
	Total uint64
	Used  uint64
}

type Load struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

type Network struct {
	Up        uint64
	Down      uint64
	TotalUp   uint64
	TotalDown uint64
}

type Connections struct {
	TCP int
	UDP int
}

type GPUDevice struct {
	Name        string
	MemoryTotal uint64
	MemoryUsed  uint64
	Utilization float64
	Temperature uint64
}

type GPU struct {
	Detailed     bool
	AverageUsage float64
	Devices      []GPUDevice
	Models       []string
}

type Report struct {
	CPUUsage    float64
	RAM         Memory
	Swap        Memory
	Load        Load
	Disk        Memory
	Network     Network
	Connections Connections
	Uptime      uint64
	Process     int
	Message     string
	GPU         *GPU
}

func Encode(report Report) ([]byte, error) {
	if err := validateReport(report); err != nil {
		return nil, err
	}

	frame := make([]byte, HeaderSize, 256)
	frame = appendFloat64(frame, report.CPUUsage)
	frame = appendMemory(frame, report.RAM)
	frame = appendMemory(frame, report.Swap)
	frame = appendFloat64(frame, report.Load.Load1)
	frame = appendFloat64(frame, report.Load.Load5)
	frame = appendFloat64(frame, report.Load.Load15)
	frame = appendMemory(frame, report.Disk)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.Up)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.Down)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.TotalUp)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.TotalDown)
	frame = binary.LittleEndian.AppendUint32(frame, uint32(report.Connections.TCP))
	frame = binary.LittleEndian.AppendUint32(frame, uint32(report.Connections.UDP))
	frame = binary.LittleEndian.AppendUint64(frame, report.Uptime)
	frame = binary.LittleEndian.AppendUint32(frame, uint32(report.Process))
	frame = appendString(frame, report.Message)

	flags := uint8(0)
	if report.GPU != nil {
		flags |= flagGPU
		if report.GPU.Detailed {
			flags |= flagGPUDetailed
			frame = binary.LittleEndian.AppendUint16(frame, uint16(len(report.GPU.Devices)))
			frame = appendFloat64(frame, report.GPU.AverageUsage)
			for _, device := range report.GPU.Devices {
				frame = appendString(frame, device.Name)
				frame = binary.LittleEndian.AppendUint64(frame, device.MemoryTotal)
				frame = binary.LittleEndian.AppendUint64(frame, device.MemoryUsed)
				frame = appendFloat64(frame, device.Utilization)
				frame = binary.LittleEndian.AppendUint64(frame, device.Temperature)
			}
		} else {
			frame = binary.LittleEndian.AppendUint16(frame, uint16(len(report.GPU.Models)))
			for _, model := range report.GPU.Models {
				frame = appendString(frame, model)
			}
		}
	}
	if len(frame) > MaxFrameSize {
		return nil, fmt.Errorf("telemetry v2 frame is %d bytes, maximum is %d", len(frame), MaxFrameSize)
	}

	copy(frame[0:4], frameMagic[:])
	frame[4] = Version
	frame[5] = flags
	binary.LittleEndian.PutUint16(frame[6:8], HeaderSize)
	binary.LittleEndian.PutUint32(frame[8:12], uint32(len(frame)-HeaderSize))
	binary.LittleEndian.PutUint32(frame[12:16], SchemaID)
	return frame, nil
}

func Decode(frame []byte) (Report, error) {
	if len(frame) < HeaderSize {
		return Report{}, errors.New("telemetry v2 frame is shorter than its header")
	}
	if len(frame) > MaxFrameSize {
		return Report{}, fmt.Errorf("telemetry v2 frame is %d bytes, maximum is %d", len(frame), MaxFrameSize)
	}
	if string(frame[0:4]) != string(frameMagic[:]) {
		return Report{}, errors.New("invalid telemetry v2 magic")
	}
	if frame[4] != Version {
		return Report{}, fmt.Errorf("unsupported telemetry version %d", frame[4])
	}
	flags := frame[5]
	if flags&^knownFlags != 0 || flags&flagGPUDetailed != 0 && flags&flagGPU == 0 {
		return Report{}, fmt.Errorf("invalid telemetry v2 flags 0x%02x", flags)
	}
	if headerSize := binary.LittleEndian.Uint16(frame[6:8]); headerSize != HeaderSize {
		return Report{}, fmt.Errorf("invalid telemetry v2 header size %d", headerSize)
	}
	if schema := binary.LittleEndian.Uint32(frame[12:16]); schema != SchemaID {
		return Report{}, fmt.Errorf("unsupported telemetry schema 0x%08x", schema)
	}
	payloadSize := binary.LittleEndian.Uint32(frame[8:12])
	if uint64(payloadSize)+HeaderSize != uint64(len(frame)) {
		return Report{}, errors.New("telemetry v2 payload length does not match frame")
	}

	reader := wireReader{data: frame[HeaderSize:]}
	result := Report{}
	result.CPUUsage = reader.float64()
	result.RAM = reader.memory()
	result.Swap = reader.memory()
	result.Load.Load1 = reader.float64()
	result.Load.Load5 = reader.float64()
	result.Load.Load15 = reader.float64()
	result.Disk = reader.memory()
	result.Network.Up = reader.uint64()
	result.Network.Down = reader.uint64()
	result.Network.TotalUp = reader.uint64()
	result.Network.TotalDown = reader.uint64()
	result.Connections.TCP = int(reader.uint32())
	result.Connections.UDP = int(reader.uint32())
	result.Uptime = reader.uint64()
	result.Process = int(reader.uint32())
	result.Message = reader.string(MaxMessageSize)

	if flags&flagGPU != 0 {
		gpu := &GPU{Detailed: flags&flagGPUDetailed != 0}
		count := int(reader.uint16())
		if count > MaxGPUCount {
			reader.fail(fmt.Errorf("GPU count %d exceeds maximum %d", count, MaxGPUCount))
		}
		if reader.err != nil {
			return Report{}, reader.err
		}
		if gpu.Detailed {
			gpu.AverageUsage = reader.float64()
			gpu.Devices = make([]GPUDevice, count)
			for index := range gpu.Devices {
				gpu.Devices[index] = GPUDevice{
					Name:        reader.string(MaxGPUNameSize),
					MemoryTotal: reader.uint64(),
					MemoryUsed:  reader.uint64(),
					Utilization: reader.float64(),
					Temperature: reader.uint64(),
				}
			}
		} else {
			gpu.Models = make([]string, count)
			for index := range gpu.Models {
				gpu.Models[index] = reader.string(MaxGPUNameSize)
			}
		}
		result.GPU = gpu
	}
	if reader.err != nil {
		return Report{}, reader.err
	}
	if reader.offset != len(reader.data) {
		return Report{}, fmt.Errorf("telemetry v2 frame has %d trailing bytes", len(reader.data)-reader.offset)
	}
	if err := validateReport(result); err != nil {
		return Report{}, fmt.Errorf("invalid telemetry v2 report: %w", err)
	}
	return result, nil
}

func validateReport(report Report) error {
	if !finite(report.CPUUsage) || report.CPUUsage < 0 || report.CPUUsage > 100 {
		return errors.New("CPU usage must be finite and within 0..100")
	}
	if err := validateMemory("RAM", report.RAM); err != nil {
		return err
	}
	if err := validateMemory("swap", report.Swap); err != nil {
		return err
	}
	if err := validateMemory("disk", report.Disk); err != nil {
		return err
	}
	if !finite(report.Load.Load1) || !finite(report.Load.Load5) || !finite(report.Load.Load15) {
		return errors.New("load values must be finite")
	}
	if report.Connections.TCP < 0 || uint64(report.Connections.TCP) > math.MaxUint32 ||
		report.Connections.UDP < 0 || uint64(report.Connections.UDP) > math.MaxUint32 {
		return errors.New("connection counts exceed uint32")
	}
	if report.Process < 0 || uint64(report.Process) > math.MaxUint32 {
		return errors.New("process count exceeds uint32")
	}
	if err := validateString("message", report.Message, MaxMessageSize); err != nil {
		return err
	}
	if report.GPU == nil {
		return nil
	}
	if report.GPU.Detailed {
		if len(report.GPU.Devices) > MaxGPUCount {
			return fmt.Errorf("GPU count exceeds %d", MaxGPUCount)
		}
		if !finite(report.GPU.AverageUsage) || report.GPU.AverageUsage < 0 || report.GPU.AverageUsage > 100 {
			return errors.New("GPU average usage must be finite and within 0..100")
		}
		for _, device := range report.GPU.Devices {
			if err := validateString("GPU name", device.Name, MaxGPUNameSize); err != nil {
				return err
			}
			if device.MemoryUsed > device.MemoryTotal {
				return errors.New("GPU used memory exceeds total")
			}
			if !finite(device.Utilization) || device.Utilization < 0 || device.Utilization > 100 {
				return errors.New("GPU utilization must be finite and within 0..100")
			}
		}
		return nil
	}
	if len(report.GPU.Models) > MaxGPUCount {
		return fmt.Errorf("GPU model count exceeds %d", MaxGPUCount)
	}
	for _, model := range report.GPU.Models {
		if err := validateString("GPU model", model, MaxGPUNameSize); err != nil {
			return err
		}
	}
	return nil
}

func validateMemory(name string, memory Memory) error {
	if memory.Used > memory.Total {
		return fmt.Errorf("%s used bytes exceed total", name)
	}
	return nil
}

func validateString(name, value string, maximum int) error {
	if len(value) > maximum {
		return fmt.Errorf("%s is %d bytes, maximum is %d", name, len(value), maximum)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func appendMemory(destination []byte, memory Memory) []byte {
	destination = binary.LittleEndian.AppendUint64(destination, memory.Total)
	return binary.LittleEndian.AppendUint64(destination, memory.Used)
}

func appendFloat64(destination []byte, value float64) []byte {
	return binary.LittleEndian.AppendUint64(destination, math.Float64bits(value))
}

func appendString(destination []byte, value string) []byte {
	destination = binary.LittleEndian.AppendUint16(destination, uint16(len(value)))
	return append(destination, value...)
}

type wireReader struct {
	data   []byte
	offset int
	err    error
}

func (reader *wireReader) fail(err error) {
	if reader.err == nil {
		reader.err = err
	}
}

func (reader *wireReader) take(size int) []byte {
	if reader.err != nil {
		return nil
	}
	if size < 0 || size > len(reader.data)-reader.offset {
		reader.fail(errors.New("telemetry v2 frame is truncated"))
		return nil
	}
	result := reader.data[reader.offset : reader.offset+size]
	reader.offset += size
	return result
}

func (reader *wireReader) uint16() uint16 {
	value := reader.take(2)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(value)
}

func (reader *wireReader) uint32() uint32 {
	value := reader.take(4)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(value)
}

func (reader *wireReader) uint64() uint64 {
	value := reader.take(8)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(value)
}

func (reader *wireReader) float64() float64 {
	return math.Float64frombits(reader.uint64())
}

func (reader *wireReader) memory() Memory {
	return Memory{Total: reader.uint64(), Used: reader.uint64()}
}

func (reader *wireReader) string(maximum int) string {
	size := int(reader.uint16())
	if size > maximum {
		reader.fail(fmt.Errorf("telemetry string is %d bytes, maximum is %d", size, maximum))
		return ""
	}
	value := reader.take(size)
	if value == nil {
		return ""
	}
	if !utf8.Valid(value) {
		reader.fail(errors.New("telemetry string is not valid UTF-8"))
		return ""
	}
	return string(value)
}
