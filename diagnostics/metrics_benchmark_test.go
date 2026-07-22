package diagnostics

import (
	"testing"
	"time"
)

func BenchmarkObserveReportDisabled(b *testing.B) {
	resetForTest()
	SetEnabled(false)
	started := time.Now()
	b.ReportAllocs()
	for range b.N {
		ObserveReport(started, 384, nil)
	}
}

func BenchmarkObserveReportEnabled(b *testing.B) {
	resetForTest()
	SetEnabled(true)
	b.Cleanup(func() { SetEnabled(false) })
	started := time.Now()
	b.ReportAllocs()
	for range b.N {
		ObserveReport(started, 384, nil)
	}
}

func BenchmarkCurrentSnapshot(b *testing.B) {
	resetForTest()
	SetEnabled(true)
	b.Cleanup(func() { SetEnabled(false) })
	b.ReportAllocs()
	for range b.N {
		_ = CurrentSnapshot()
	}
}
