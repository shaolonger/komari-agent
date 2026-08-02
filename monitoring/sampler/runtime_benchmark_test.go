package sampler

import (
	"context"
	"testing"
	"time"
)

func BenchmarkSnapshotTenSamplers(b *testing.B) {
	specs := make([]Spec, 10)
	for index := range specs {
		specs[index] = Spec{
			Name:       samplerNamesForBenchmark[index],
			Interval:   time.Hour,
			Timeout:    time.Second,
			StaleAfter: time.Hour,
			Sample: func(context.Context) (any, error) {
				return 42, nil
			},
		}
	}
	runtime, err := New(specs)
	if err != nil {
		b.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(runtime.Stop)
	for len(runtime.Snapshot().Results) < len(specs) {
		time.Sleep(time.Millisecond)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = runtime.Snapshot()
	}
}

var samplerNamesForBenchmark = [...]string{
	"cpu", "ram", "swap", "load", "disk", "network", "connections", "uptime", "process", "gpu",
}
