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

func TestMemoryFromSingleProcSnapshot(t *testing.T) {
	info, err := readProcMeminfo(strings.NewReader(benchmarkProcMeminfo))
	if err != nil {
		t.Fatalf("readProcMeminfo failed: %v", err)
	}

	got := memoryFromProc(info, false)
	if want := uint64(22_700_032 * 1024); got.RAM.Used != want {
		t.Fatalf("htop-like RAM used = %d, want %d", got.RAM.Used, want)
	}
	if got.RAM.Total != 32_768_000*1024 || got.RAM.Mode != "htoplike" {
		t.Fatalf("unexpected RAM result: %+v", got.RAM)
	}
	if want := uint64(983_040 * 1024); got.Swap.Used != want {
		t.Fatalf("swap used = %d, want %d", got.Swap.Used, want)
	}
	if got.Swap.Total != 4_194_304*1024 {
		t.Fatalf("unexpected swap result: %+v", got.Swap)
	}

	includeCache := memoryFromProc(info, true)
	if want := uint64(31_744_000 * 1024); includeCache.RAM.Used != want {
		t.Fatalf("include-cache RAM used = %d, want %d", includeCache.RAM.Used, want)
	}
	if includeCache.RAM.Mode != "includeCache" {
		t.Fatalf("unexpected include-cache mode %q", includeCache.RAM.Mode)
	}
}

func TestMemoryFromProcBoundsCorruptCounters(t *testing.T) {
	info := &ProcMemInfo{
		MemTotal:     1024,
		MemFree:      512,
		Cached:       ^uint64(0),
		SReclaimable: ^uint64(0),
		Buffers:      ^uint64(0),
		Shmem:        ^uint64(0),
		SwapTotal:    512,
		SwapFree:     256,
		SwapCached:   ^uint64(0),
	}

	got := memoryFromProc(info, false)
	if got.RAM.Used > got.RAM.Total {
		t.Fatalf("RAM used exceeds total: %+v", got.RAM)
	}
	if got.Swap.Used > got.Swap.Total {
		t.Fatalf("swap used exceeds total: %+v", got.Swap)
	}
}
