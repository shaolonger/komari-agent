package monitoring

import "testing"

var benchmarkEncodedReport []byte

// BenchmarkGenerateReport intentionally exercises the current end-to-end
// report path, including platform calls. Run it with -benchtime=1x when a
// quick baseline is needed because the legacy CPU and network samplers block.
func BenchmarkGenerateReport(b *testing.B) {
	originalGPU := flags.EnableGPU
	originalMonthRotate := flags.MonthRotate
	flags.EnableGPU = false
	flags.MonthRotate = 0
	b.Cleanup(func() {
		flags.EnableGPU = originalGPU
		flags.MonthRotate = originalMonthRotate
	})

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkEncodedReport = GenerateReport()
	}
	b.StopTimer()
	if len(benchmarkEncodedReport) > 0 {
		b.ReportMetric(float64(len(benchmarkEncodedReport)), "bytes/report")
	}
}
