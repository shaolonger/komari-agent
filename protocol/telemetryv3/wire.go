package telemetryv3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
)

const (
	Subprotocol    = "komari.telemetry.v3"
	Version        = uint8(3)
	HeaderSize     = 32
	MaxFrameSize   = telemetryv2.MaxFrameSize
	SchemaID       = uint32(0x33525054) // little-endian bytes spell "TPR3"
	MaxSampleCount = 3600
)

var frameMagic = [4]byte{'K', 'M', 'R', '3'}

const (
	flagCheckpoint = uint8(1 << 0)
	knownFlags     = flagCheckpoint
	envelopeSize   = 72
)

// Envelope retains information that would be lost when several local samples
// are represented by one network frame. Counter deltas are unsigned and reset
// to zero when the underlying counter moves backwards.
type Envelope struct {
	Count            uint32
	CPUMin           float64
	CPUMax           float64
	CPUSum           float64
	RAMUsedMin       uint64
	RAMUsedMax       uint64
	RAMUsedSum       uint64
	NetworkUpDelta   uint64
	NetworkDownDelta uint64
}

type Frame struct {
	Sequence   uint64
	SampledAt  time.Time
	Checkpoint bool
	Envelope   Envelope
	Latest     telemetryv2.Report
}

func Encode(frame Frame) ([]byte, error) {
	if err := validateFrame(frame); err != nil {
		return nil, err
	}
	latest, err := telemetryv2.Encode(frame.Latest)
	if err != nil {
		return nil, fmt.Errorf("encode latest report: %w", err)
	}
	if HeaderSize+envelopeSize+len(latest) > MaxFrameSize {
		return nil, errors.New("telemetry v3 frame exceeds maximum size")
	}
	result := make([]byte, HeaderSize, HeaderSize+envelopeSize+len(latest))
	result = binary.LittleEndian.AppendUint32(result, frame.Envelope.Count)
	result = appendFloat64(result, frame.Envelope.CPUMin)
	result = appendFloat64(result, frame.Envelope.CPUMax)
	result = appendFloat64(result, frame.Envelope.CPUSum)
	result = binary.LittleEndian.AppendUint64(result, frame.Envelope.RAMUsedMin)
	result = binary.LittleEndian.AppendUint64(result, frame.Envelope.RAMUsedMax)
	result = binary.LittleEndian.AppendUint64(result, frame.Envelope.RAMUsedSum)
	result = binary.LittleEndian.AppendUint64(result, frame.Envelope.NetworkUpDelta)
	result = binary.LittleEndian.AppendUint64(result, frame.Envelope.NetworkDownDelta)
	result = binary.LittleEndian.AppendUint32(result, uint32(len(latest)))
	result = append(result, latest...)

	copy(result[:4], frameMagic[:])
	result[4] = Version
	if frame.Checkpoint {
		result[5] = flagCheckpoint
	}
	binary.LittleEndian.PutUint16(result[6:8], HeaderSize)
	binary.LittleEndian.PutUint32(result[8:12], uint32(len(result)-HeaderSize))
	binary.LittleEndian.PutUint32(result[12:16], SchemaID)
	binary.LittleEndian.PutUint64(result[16:24], frame.Sequence)
	binary.LittleEndian.PutUint64(result[24:32], uint64(frame.SampledAt.UnixMilli()))
	return result, nil
}

func Decode(data []byte) (Frame, error) {
	if len(data) < HeaderSize {
		return Frame{}, errors.New("telemetry v3 frame is shorter than its header")
	}
	if len(data) > MaxFrameSize {
		return Frame{}, errors.New("telemetry v3 frame exceeds maximum size")
	}
	if string(data[:4]) != string(frameMagic[:]) || data[4] != Version {
		return Frame{}, errors.New("invalid telemetry v3 magic or version")
	}
	flags := data[5]
	if flags&^knownFlags != 0 {
		return Frame{}, fmt.Errorf("invalid telemetry v3 flags 0x%02x", flags)
	}
	if binary.LittleEndian.Uint16(data[6:8]) != HeaderSize || binary.LittleEndian.Uint32(data[12:16]) != SchemaID {
		return Frame{}, errors.New("unsupported telemetry v3 header or schema")
	}
	if uint64(binary.LittleEndian.Uint32(data[8:12]))+HeaderSize != uint64(len(data)) {
		return Frame{}, errors.New("telemetry v3 payload length does not match frame")
	}
	if len(data) < HeaderSize+envelopeSize {
		return Frame{}, errors.New("telemetry v3 envelope is truncated")
	}
	offset := HeaderSize
	next32 := func() uint32 { value := binary.LittleEndian.Uint32(data[offset : offset+4]); offset += 4; return value }
	next64 := func() uint64 { value := binary.LittleEndian.Uint64(data[offset : offset+8]); offset += 8; return value }
	result := Frame{
		Sequence:   binary.LittleEndian.Uint64(data[16:24]),
		SampledAt:  time.UnixMilli(int64(binary.LittleEndian.Uint64(data[24:32]))).UTC(),
		Checkpoint: flags&flagCheckpoint != 0,
	}
	result.Envelope.Count = next32()
	result.Envelope.CPUMin = math.Float64frombits(next64())
	result.Envelope.CPUMax = math.Float64frombits(next64())
	result.Envelope.CPUSum = math.Float64frombits(next64())
	result.Envelope.RAMUsedMin = next64()
	result.Envelope.RAMUsedMax = next64()
	result.Envelope.RAMUsedSum = next64()
	result.Envelope.NetworkUpDelta = next64()
	result.Envelope.NetworkDownDelta = next64()
	latestSize := int(next32())
	if latestSize < telemetryv2.HeaderSize || latestSize != len(data)-offset {
		return Frame{}, errors.New("telemetry v3 nested report length does not match frame")
	}
	latest, err := telemetryv2.Decode(data[offset:])
	if err != nil {
		return Frame{}, fmt.Errorf("decode latest report: %w", err)
	}
	result.Latest = latest
	if err := validateFrame(result); err != nil {
		return Frame{}, fmt.Errorf("invalid telemetry v3 frame: %w", err)
	}
	return result, nil
}

func validateFrame(frame Frame) error {
	if frame.Sequence == 0 {
		return errors.New("sequence must be positive")
	}
	if frame.SampledAt.IsZero() || frame.SampledAt.UnixMilli() <= 0 {
		return errors.New("sample time must be positive")
	}
	envelope := frame.Envelope
	if envelope.Count == 0 || envelope.Count > MaxSampleCount {
		return fmt.Errorf("sample count must be between 1 and %d", MaxSampleCount)
	}
	if !finite(envelope.CPUMin) || !finite(envelope.CPUMax) || !finite(envelope.CPUSum) ||
		envelope.CPUMin < 0 || envelope.CPUMax > 100 || envelope.CPUMin > envelope.CPUMax ||
		envelope.CPUSum < envelope.CPUMin || envelope.CPUSum > envelope.CPUMax*float64(envelope.Count) {
		return errors.New("invalid CPU envelope")
	}
	if envelope.RAMUsedMin > envelope.RAMUsedMax || envelope.RAMUsedSum < envelope.RAMUsedMin {
		return errors.New("invalid RAM envelope")
	}
	return nil
}

func appendFloat64(destination []byte, value float64) []byte {
	return binary.LittleEndian.AppendUint64(destination, math.Float64bits(value))
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
