package telemetryv2

import "testing"

var (
	benchmarkFrame  []byte
	benchmarkReport Report
)

func BenchmarkEncode(b *testing.B) {
	report := goldenReport()
	b.ReportAllocs()
	for range b.N {
		benchmarkFrame, _ = Encode(report)
	}
}

func BenchmarkDecode(b *testing.B) {
	frame, err := Encode(goldenReport())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for range b.N {
		benchmarkReport, _ = Decode(frame)
	}
}
