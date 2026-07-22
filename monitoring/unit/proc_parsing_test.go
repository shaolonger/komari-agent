package monitoring

import (
	"strings"
	"testing"
)

func TestReadCPUNameFromFixture(t *testing.T) {
	name, err := readCPUName(strings.NewReader(benchmarkProcCPUInfo))
	if err != nil {
		t.Fatalf("readCPUName failed: %v", err)
	}
	if name != "Benchmark CPU" {
		t.Fatalf("unexpected CPU name %q", name)
	}
}

func TestReadProcMeminfoFromFixture(t *testing.T) {
	info, err := readProcMeminfo(strings.NewReader(benchmarkProcMeminfo))
	if err != nil {
		t.Fatalf("readProcMeminfo failed: %v", err)
	}
	if info.MemTotal != 32_768_000*1024 {
		t.Fatalf("unexpected MemTotal %d", info.MemTotal)
	}
	if info.MemAvailable != 16_384_000*1024 {
		t.Fatalf("unexpected MemAvailable %d", info.MemAvailable)
	}
	if info.SwapTotal != 4_194_304*1024 {
		t.Fatalf("unexpected SwapTotal %d", info.SwapTotal)
	}
}
