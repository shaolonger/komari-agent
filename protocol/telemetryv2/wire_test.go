package telemetryv2

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeDecodeGoldenReport(t *testing.T) {
	report := goldenReport()
	encoded, err := Encode(report)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	fixtureText, err := os.ReadFile("testdata/report_v2.hex")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	fixture, err := hex.DecodeString(strings.TrimSpace(string(fixtureText)))
	if err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	if !reflect.DeepEqual(encoded, fixture) {
		t.Fatalf("encoded fixture mismatch\n got: %x\nwant: %x", encoded, fixture)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if !reflect.DeepEqual(decoded, report) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", decoded, report)
	}
}

func TestEncodeDecodeGPUFallback(t *testing.T) {
	report := goldenReport()
	report.GPU = &GPU{Models: []string{"Fixture GPU 0", "Fixture GPU 1"}}
	encoded, err := Encode(report)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if !reflect.DeepEqual(decoded, report) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", decoded, report)
	}
}

func TestEncodeRejectsUnsafeOrUnrepresentableValues(t *testing.T) {
	tests := map[string]func(*Report){
		"non-finite CPU": func(report *Report) { report.CPUUsage = math.NaN() },
		"CPU over 100":   func(report *Report) { report.CPUUsage = 101 },
		"RAM used":       func(report *Report) { report.RAM.Used = report.RAM.Total + 1 },
		"negative TCP":   func(report *Report) { report.Connections.TCP = -1 },
		"negative process": func(report *Report) {
			report.Process = -1
		},
		"message too large": func(report *Report) {
			report.Message = strings.Repeat("x", MaxMessageSize+1)
		},
		"invalid UTF-8": func(report *Report) { report.Message = string([]byte{0xff}) },
		"too many GPUs": func(report *Report) {
			report.GPU.Devices = make([]GPUDevice, MaxGPUCount+1)
		},
		"GPU memory": func(report *Report) {
			report.GPU.Devices[0].MemoryUsed = report.GPU.Devices[0].MemoryTotal + 1
		},
		"GPU utilization": func(report *Report) {
			report.GPU.Devices[0].Utilization = math.Inf(1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			report := goldenReport()
			mutate(&report)
			if _, err := Encode(report); err == nil {
				t.Fatal("invalid report was encoded")
			}
		})
	}
}

func TestDecodeRejectsMalformedFrames(t *testing.T) {
	valid, err := Encode(goldenReport())
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	for size := range len(valid) {
		if _, err := Decode(valid[:size]); err == nil {
			t.Fatalf("truncated frame of %d bytes was accepted", size)
		}
	}

	mutations := map[string]func([]byte){
		"magic":       func(frame []byte) { frame[0] = 'X' },
		"version":     func(frame []byte) { frame[4]++ },
		"flags":       func(frame []byte) { frame[5] = 0x80 },
		"header size": func(frame []byte) { binary.LittleEndian.PutUint16(frame[6:8], 15) },
		"payload size": func(frame []byte) {
			binary.LittleEndian.PutUint32(frame[8:12], uint32(len(frame)))
		},
		"schema": func(frame []byte) { binary.LittleEndian.PutUint32(frame[12:16], 0) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			frame := append([]byte(nil), valid...)
			mutate(frame)
			if _, err := Decode(frame); err == nil {
				t.Fatal("malformed frame was accepted")
			}
		})
	}

	trailing := append(append([]byte(nil), valid...), 0)
	binary.LittleEndian.PutUint32(trailing[8:12], uint32(len(trailing)-HeaderSize))
	if _, err := Decode(trailing); err == nil {
		t.Fatal("trailing byte was accepted")
	}
	oversized := make([]byte, MaxFrameSize+1)
	if _, err := Decode(oversized); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

func FuzzDecodeNeverPanics(f *testing.F) {
	valid, err := Encode(goldenReport())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte("KMR2"))
	f.Add(make([]byte, MaxFrameSize+1))
	f.Fuzz(func(t *testing.T, frame []byte) {
		_, _ = Decode(frame)
	})
}

func goldenReport() Report {
	return Report{
		CPUUsage: 12.5,
		RAM:      Memory{Total: 8_000, Used: 4_000},
		Swap:     Memory{Total: 2_000, Used: 100},
		Load:     Load{Load1: 1.1, Load5: 1.2, Load15: 1.3},
		Disk:     Memory{Total: 1_000, Used: 500},
		Network:  Network{Up: 100, Down: 200, TotalUp: 1_000, TotalDown: 2_000},
		Connections: Connections{
			TCP: 12,
			UDP: 3,
		},
		Uptime:  999,
		Process: 42,
		Message: "ok",
		GPU: &GPU{
			Detailed:     true,
			AverageUsage: 75,
			Devices: []GPUDevice{{
				Name:        "GPU",
				MemoryTotal: 100,
				MemoryUsed:  50,
				Utilization: 75,
				Temperature: 65,
			}},
		},
	}
}
