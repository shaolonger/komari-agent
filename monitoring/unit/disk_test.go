package monitoring

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

func TestBuildAutomaticDiskTopologyFiltersAndGroups(t *testing.T) {
	parts := []disk.PartitionStat{
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/sdb1", Mountpoint: "/data/subvolume", Fstype: "ext4"},
		{Device: "/dev/sdb1", Mountpoint: "/data", Fstype: "ext4"},
		{Device: "pool/dataset", Mountpoint: "/tank/dataset", Fstype: "zfs"},
		{Device: "pool/root", Mountpoint: "/tank", Fstype: "zfs"},
		{Device: "tmpfs", Mountpoint: "/tmp", Fstype: "tmpfs"},
		{Device: "server:/export", Mountpoint: "/mnt/nfs", Fstype: "nfs"},
		{Device: "/dev/loop0", Mountpoint: "/snap/tool", Fstype: "squashfs"},
		{Mountpoint: "/mnt/anonymous-a", Fstype: "ext4"},
		{Mountpoint: "/mnt/anonymous-b", Fstype: "ext4"},
	}

	topology := buildAutomaticDiskTopology(parts)
	if len(topology.groups) != 5 {
		t.Fatalf("groups = %d, want 5: %+v", len(topology.groups), topology.groups)
	}
	wantList := []string{
		"/ (ext4)",
		"/data (ext4)",
		"/mnt/anonymous-a (ext4)",
		"/mnt/anonymous-b (ext4)",
		"/tank (zfs)",
	}
	if !reflect.DeepEqual(topology.list, wantList) {
		t.Fatalf("list = %#v, want %#v", topology.list, wantList)
	}
	if got := len(topology.groups[1].candidates); got != 2 {
		t.Fatalf("duplicate device candidates = %d, want 2", got)
	}
	if got := len(topology.groups[2].candidates); got != 2 {
		t.Fatalf("ZFS candidates = %d, want 2", got)
	}
}

func TestDiskSamplerPreparsedIncludesBypassPartitionEnumeration(t *testing.T) {
	var partitionCalls atomic.Int32
	var usageCalls atomic.Int32
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) {
			partitionCalls.Add(1)
			return nil, errors.New("must not enumerate")
		},
		func(path string) (*disk.UsageStat, error) {
			usageCalls.Add(1)
			switch path {
			case "/data":
				return &disk.UsageStat{Total: 100, Used: 40}, nil
			case "/backup":
				return &disk.UsageStat{Total: 200, Used: 80}, nil
			default:
				return nil, errors.New("unexpected mount")
			}
		},
		time.Now,
		time.Hour,
		time.Hour,
	)

	got, err := sampler.Sample(" /data ; /backup; /data;; ", false, false)
	if err != nil {
		t.Fatalf("Sample failed: %v", err)
	}
	if got != (DiskInfo{Total: 300, Used: 120}) {
		t.Fatalf("sample = %+v", got)
	}
	if partitionCalls.Load() != 0 || usageCalls.Load() != 2 {
		t.Fatalf("source calls: partitions=%d usage=%d", partitionCalls.Load(), usageCalls.Load())
	}
	list, err := sampler.List("/data;/backup", false)
	if err != nil || !reflect.DeepEqual(list, []string{"/data", "/backup"}) {
		t.Fatalf("list = %#v, err = %v", list, err)
	}
}

func TestDiskSamplerSeparatesTopologyAndCapacityIntervals(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	var partitionCalls atomic.Int32
	var usageCalls atomic.Int32
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) {
			partitionCalls.Add(1)
			return []disk.PartitionStat{{Device: "/dev/root", Mountpoint: "/", Fstype: "ext4"}}, nil
		},
		func(string) (*disk.UsageStat, error) {
			usageCalls.Add(1)
			return &disk.UsageStat{Total: 100, Used: 50}, nil
		},
		func() time.Time { return current },
		5*time.Minute,
		30*time.Second,
	)

	assertDiskSample := func(wantPartitionCalls, wantUsageCalls int32) {
		t.Helper()
		got, err := sampler.Sample("", false, false)
		if err != nil || got != (DiskInfo{Total: 100, Used: 50}) {
			t.Fatalf("sample = %+v, err = %v", got, err)
		}
		if partitionCalls.Load() != wantPartitionCalls || usageCalls.Load() != wantUsageCalls {
			t.Fatalf("source calls: partitions=%d usage=%d, want %d/%d",
				partitionCalls.Load(), usageCalls.Load(), wantPartitionCalls, wantUsageCalls)
		}
	}

	assertDiskSample(1, 1)
	current = current.Add(29 * time.Second)
	assertDiskSample(1, 1)
	current = current.Add(2 * time.Second)
	assertDiskSample(1, 2)
	current = current.Add(5 * time.Minute)
	assertDiskSample(2, 3)
	current = current.Add(-10 * time.Minute)
	assertDiskSample(3, 4)
}

func TestDiskSamplerRefreshesHotplugAndKeepsFailedMountStale(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	parts := []disk.PartitionStat{{Device: "/dev/root", Mountpoint: "/", Fstype: "ext4"}}
	usage := map[string]*disk.UsageStat{
		"/":     {Total: 100, Used: 50},
		"/data": {Total: 200, Used: 100},
	}
	failedMount := ""
	partitionErr := error(nil)
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) {
			return append([]disk.PartitionStat(nil), parts...), partitionErr
		},
		func(path string) (*disk.UsageStat, error) {
			if path == failedMount {
				return nil, errors.New("fixture mount failure")
			}
			stat := *usage[path]
			return &stat, nil
		},
		func() time.Time { return current },
		5*time.Minute,
		30*time.Second,
	)

	if got, err := sampler.Sample("", false, false); err != nil || got.Total != 100 {
		t.Fatalf("initial sample = %+v, err = %v", got, err)
	}
	parts = append(parts, disk.PartitionStat{Device: "/dev/data", Mountpoint: "/data", Fstype: "xfs"})
	if got, err := sampler.Sample("", true, true); err != nil || got != (DiskInfo{Total: 300, Used: 150}) {
		t.Fatalf("hotplug sample = %+v, err = %v", got, err)
	}

	current = current.Add(31 * time.Second)
	usage["/"] = &disk.UsageStat{Total: 120, Used: 60}
	failedMount = "/data"
	got, err := sampler.Sample("", false, false)
	if err == nil {
		t.Fatal("mount failure was not reported")
	}
	if got != (DiskInfo{Total: 320, Used: 160}) {
		t.Fatalf("partial failure did not retain stale group: %+v", got)
	}

	partitionErr = errors.New("fixture topology failure")
	failedMount = ""
	got, err = sampler.Sample("", true, true)
	if err == nil || got != (DiskInfo{Total: 320, Used: 160}) {
		t.Fatalf("topology failure sample = %+v, err = %v", got, err)
	}
}

func TestDiskSamplerZFSSelectsLargestDatasetAndBoundsCounters(t *testing.T) {
	topology := buildAutomaticDiskTopology([]disk.PartitionStat{
		{Device: "pool/quota", Mountpoint: "/tank/quota", Fstype: "zfs"},
		{Device: "pool/root", Mountpoint: "/tank", Fstype: "zfs"},
		{Device: "/dev/overflow", Mountpoint: "/overflow", Fstype: "ext4"},
	})
	stats := map[string]*disk.UsageStat{
		"/tank/quota": {Total: 50, Used: 30},
		"/tank":       {Total: 100, Used: 75},
		"/overflow":   {Total: ^uint64(0), Used: ^uint64(0)},
	}
	got, _, err := sampleDiskTopology(topology, nil, func(path string) (*disk.UsageStat, error) {
		return stats[path], nil
	})
	if err != nil {
		t.Fatalf("sampleDiskTopology failed: %v", err)
	}
	if got.Total != ^uint64(0) || got.Used != ^uint64(0) {
		t.Fatalf("overflow was not saturated: %+v", got)
	}
}

func TestDiskSamplerConcurrentColdReadUsesOneSourcePass(t *testing.T) {
	var partitionCalls atomic.Int32
	var usageCalls atomic.Int32
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) {
			partitionCalls.Add(1)
			return []disk.PartitionStat{{Device: "/dev/root", Mountpoint: "/", Fstype: "ext4"}}, nil
		},
		func(string) (*disk.UsageStat, error) {
			usageCalls.Add(1)
			return &disk.UsageStat{Total: 100, Used: 50}, nil
		},
		time.Now,
		time.Hour,
		time.Hour,
	)

	const readers = 32
	var wait sync.WaitGroup
	wait.Add(readers)
	for range readers {
		go func() {
			defer wait.Done()
			if got, err := sampler.Sample("", false, false); err != nil || got.Total != 100 {
				t.Errorf("sample = %+v, err = %v", got, err)
			}
		}()
	}
	wait.Wait()
	if partitionCalls.Load() != 1 || usageCalls.Load() != 1 {
		t.Fatalf("source calls: partitions=%d usage=%d", partitionCalls.Load(), usageCalls.Load())
	}
}

func TestDiskSamplerTopologyFailureIsCachedThenRetried(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	var calls atomic.Int32
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) {
			if calls.Add(1) == 1 {
				return nil, errors.New("fixture topology failure")
			}
			return []disk.PartitionStat{{Device: "/dev/root", Mountpoint: "/", Fstype: "ext4"}}, nil
		},
		func(string) (*disk.UsageStat, error) {
			return &disk.UsageStat{Total: 100, Used: 50}, nil
		},
		func() time.Time { return current },
		5*time.Minute,
		30*time.Second,
	)

	if got, err := sampler.Sample("", false, false); err == nil || got != (DiskInfo{}) {
		t.Fatalf("failed initial sample = %+v, err = %v", got, err)
	}
	if got, err := sampler.Sample("", false, false); err == nil || got != (DiskInfo{}) {
		t.Fatalf("cached failure sample = %+v, err = %v", got, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("failed topology was retried immediately: calls=%d", calls.Load())
	}
	current = current.Add(5 * time.Minute)
	if got, err := sampler.Sample("", false, false); err != nil || got.Total != 100 {
		t.Fatalf("retried sample = %+v, err = %v", got, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("topology retry calls=%d, want 2", calls.Load())
	}
}

func TestDiskListReturnsCopy(t *testing.T) {
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) {
			return []disk.PartitionStat{{Device: "/dev/root", Mountpoint: "/", Fstype: "ext4"}}, nil
		},
		func(string) (*disk.UsageStat, error) { return &disk.UsageStat{}, nil },
		time.Now,
		time.Hour,
		time.Hour,
	)
	first, err := sampler.List("", false)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	first[0] = "mutated"
	second, err := sampler.List("", false)
	if err != nil || second[0] != "/ (ext4)" {
		t.Fatalf("cached list was mutable: %#v, err = %v", second, err)
	}
}
