package telemetryv3

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
)

func TestV3RoundTripAndDeterminism(t *testing.T) {
	frame := goldenFrame()
	first, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(frame)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("encoding is not deterministic")
	}
	fixtureText, err := os.ReadFile("testdata/report_v3.hex")
	if err != nil {
		t.Fatalf("read golden fixture: %v; encoded=%x", err, first)
	}
	fixture, err := hex.DecodeString(strings.TrimSpace(string(fixtureText)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, fixture) {
		t.Fatalf("golden mismatch\n got: %x\nwant: %x", first, fixture)
	}
	decoded, err := Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, frame) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", decoded, frame)
	}
}

func TestV3RejectsMalformedAndUnboundedFrames(t *testing.T) {
	valid, err := Encode(goldenFrame())
	if err != nil {
		t.Fatal(err)
	}
	for size := range len(valid) {
		if _, err := Decode(valid[:size]); err == nil {
			t.Fatalf("accepted truncated size %d", size)
		}
	}
	for name, mutate := range map[string]func([]byte){
		"magic":    func(frame []byte) { frame[0] = 'X' },
		"flags":    func(frame []byte) { frame[5] = 0x80 },
		"header":   func(frame []byte) { binary.LittleEndian.PutUint16(frame[6:8], 31) },
		"length":   func(frame []byte) { binary.LittleEndian.PutUint32(frame[8:12], 1) },
		"schema":   func(frame []byte) { binary.LittleEndian.PutUint32(frame[12:16], 0) },
		"sequence": func(frame []byte) { binary.LittleEndian.PutUint64(frame[16:24], 0) },
		"nan":      func(frame []byte) { binary.LittleEndian.PutUint64(frame[36:44], math.Float64bits(math.NaN())) },
	} {
		t.Run(name, func(t *testing.T) {
			frame := append([]byte(nil), valid...)
			mutate(frame)
			if _, err := Decode(frame); err == nil {
				t.Fatal("malformed frame accepted")
			}
		})
	}
	if _, err := Decode(make([]byte, MaxFrameSize+1)); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func FuzzDecodeV3NeverPanics(f *testing.F) {
	valid, _ := Encode(goldenFrame())
	f.Add(valid)
	f.Add([]byte("KMR3"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = Decode(data) })
}

func BenchmarkV3Codec(b *testing.B) {
	frame := goldenFrame()
	encoded, _ := Encode(frame)
	b.ReportAllocs()
	b.SetBytes(int64(len(encoded)))
	for b.Loop() {
		data, err := Encode(frame)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := Decode(data); err != nil {
			b.Fatal(err)
		}
	}
}

func goldenFrame() Frame {
	return Frame{
		Sequence: 9, SampledAt: time.UnixMilli(1_700_000_000_123).UTC(), Checkpoint: true,
		Envelope: Envelope{Count: 5, CPUMin: 10, CPUMax: 30, CPUSum: 100, RAMUsedMin: 3000, RAMUsedMax: 5000, RAMUsedSum: 20000, NetworkUpDelta: 1234, NetworkDownDelta: 5678},
		Latest: telemetryv2.Report{
			CPUUsage: 20, RAM: telemetryv2.Memory{Total: 8000, Used: 4000}, Swap: telemetryv2.Memory{Total: 2000, Used: 100},
			Load: telemetryv2.Load{Load1: 1, Load5: 2, Load15: 3}, Disk: telemetryv2.Memory{Total: 10000, Used: 5000},
			Network:     telemetryv2.Network{Up: 100, Down: 200, TotalUp: 1000, TotalDown: 2000},
			Connections: telemetryv2.Connections{TCP: 12, UDP: 3}, Uptime: 99, Process: 42, Message: "ok",
		},
	}
}
