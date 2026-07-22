package netstatic

import "testing"

var benchmarkTrafficTotals map[string]TrafficData

func BenchmarkSumTrafficBetween31Days(b *testing.B) {
	const (
		nicCount      = 8
		bucketsPerDay = 24 * 6
		days          = 31
		start         = uint64(1_700_000_000)
	)

	persisted := make(map[string][]TrafficData, nicCount)
	for nic := 0; nic < nicCount; nic++ {
		name := "benchmark-nic-" + string(rune('a'+nic))
		series := make([]TrafficData, 0, bucketsPerDay*days)
		for bucket := 0; bucket < bucketsPerDay*days; bucket++ {
			series = append(series, TrafficData{
				Timestamp: start + uint64(bucket*600),
				Tx:        uint64(1_000 + nic + bucket%127),
				Rx:        uint64(2_000 + nic + bucket%251),
			})
		}
		persisted[name] = series
	}
	end := start + uint64(bucketsPerDay*days*600)

	b.ReportAllocs()
	b.ReportMetric(float64(nicCount*bucketsPerDay*days), "buckets/op")
	b.ResetTimer()
	for range b.N {
		benchmarkTrafficTotals = sumTrafficBetween(persisted, nil, start, end)
	}
}
