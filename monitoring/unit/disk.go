package monitoring

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

const (
	defaultDiskTopologyInterval = 5 * time.Minute
	defaultDiskSampleInterval   = 30 * time.Second
)

type DiskInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type diskPartitionsSource func(bool) ([]disk.PartitionStat, error)
type diskUsageSource func(string) (*disk.UsageStat, error)

type diskGroup struct {
	id         string
	candidates []disk.PartitionStat
}

type diskTopology struct {
	groups []diskGroup
	list   []string
}

type diskSampler struct {
	mu               sync.Mutex
	partitions       diskPartitionsSource
	usage            diskUsageSource
	now              func() time.Time
	topologyInterval time.Duration
	sampleInterval   time.Duration

	topology        diskTopology
	topologyConfig  string
	topologyAt      time.Time
	topologyChecked bool
	topologyLoaded  bool
	topologyErr     error

	sample        DiskInfo
	sampleAt      time.Time
	sampleChecked bool
	groupSamples  map[string]DiskInfo
}

var defaultDiskSampler = newDiskSampler(
	disk.Partitions,
	disk.Usage,
	time.Now,
	defaultDiskTopologyInterval,
	defaultDiskSampleInterval,
)

func newDiskSampler(
	partitions diskPartitionsSource,
	usage diskUsageSource,
	now func() time.Time,
	topologyInterval time.Duration,
	sampleInterval time.Duration,
) *diskSampler {
	return &diskSampler{
		partitions:       partitions,
		usage:            usage,
		now:              now,
		topologyInterval: topologyInterval,
		sampleInterval:   sampleInterval,
		groupSamples:     make(map[string]DiskInfo),
	}
}

// Disk returns a low-frequency capacity snapshot. Partition topology is
// refreshed separately from usage so a fast report loop does not repeatedly
// enumerate every mount and allocate the complete partition list.
func Disk() DiskInfo {
	result, _ := defaultDiskSampler.Sample(flags.IncludeMountpoints, false, false)
	return result
}

// RefreshDiskTopology is the event hook for mount/config changes. It refreshes
// topology and capacity immediately while preserving the last valid value when
// a transient platform query fails.
func RefreshDiskTopology() DiskInfo {
	result, _ := defaultDiskSampler.Sample(flags.IncludeMountpoints, true, true)
	return result
}

func (sampler *diskSampler) Sample(includeMountpoints string, forceTopology, forceUsage bool) (DiskInfo, error) {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()

	now := sampler.now()
	topologyChanged, topologyErr := sampler.ensureTopologyLocked(now, includeMountpoints, forceTopology)
	if !sampler.topologyLoaded {
		return sampler.sample, topologyErr
	}

	usageDue := !sampler.sampleChecked || forceUsage || topologyChanged ||
		durationElapsed(now, sampler.sampleAt, sampler.sampleInterval)
	if !usageDue {
		return sampler.sample, topologyErr
	}

	result, groupSamples, usageErr := sampleDiskTopology(sampler.topology, sampler.groupSamples, sampler.usage)
	sampler.sampleAt = now
	sampler.sampleChecked = true
	if len(sampler.topology.groups) == 0 || len(groupSamples) > 0 {
		sampler.sample = result
		sampler.groupSamples = groupSamples
	}
	return sampler.sample, errors.Join(topologyErr, usageErr)
}

func (sampler *diskSampler) List(includeMountpoints string, forceTopology bool) ([]string, error) {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()

	_, err := sampler.ensureTopologyLocked(sampler.now(), includeMountpoints, forceTopology)
	if !sampler.topologyLoaded {
		return nil, err
	}
	return append([]string(nil), sampler.topology.list...), err
}

func (sampler *diskSampler) ensureTopologyLocked(now time.Time, rawInclude string, force bool) (bool, error) {
	includeMounts := parseIncludeMountpoints(rawInclude)
	config := strings.Join(includeMounts, "\x00")
	if len(includeMounts) == 0 {
		config = "<auto>"
	}
	due := !sampler.topologyChecked || force || config != sampler.topologyConfig ||
		durationElapsed(now, sampler.topologyAt, sampler.topologyInterval)
	if !due {
		return false, sampler.topologyErr
	}

	sampler.topologyChecked = true
	sampler.topologyAt = now
	sampler.topologyConfig = config

	var (
		topology diskTopology
		err      error
	)
	if len(includeMounts) > 0 {
		topology = buildIncludedDiskTopology(includeMounts)
	} else {
		var partitions []disk.PartitionStat
		partitions, err = sampler.partitions(true)
		if err == nil {
			topology = buildAutomaticDiskTopology(partitions)
		}
	}
	if err != nil {
		sampler.topologyErr = err
		return false, err
	}

	changed := !sampler.topologyLoaded || !equalDiskTopology(sampler.topology, topology)
	sampler.topology = topology
	sampler.topologyLoaded = true
	sampler.topologyErr = nil
	return changed, nil
}

func durationElapsed(now, previous time.Time, interval time.Duration) bool {
	if interval <= 0 || previous.IsZero() || now.Before(previous) {
		return true
	}
	return now.Sub(previous) >= interval
}

func parseIncludeMountpoints(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, strings.Count(raw, ";")+1)
	for _, value := range strings.Split(raw, ";") {
		mountpoint := strings.TrimSpace(value)
		if mountpoint == "" {
			continue
		}
		if _, exists := seen[mountpoint]; exists {
			continue
		}
		seen[mountpoint] = struct{}{}
		result = append(result, mountpoint)
	}
	return result
}

func buildIncludedDiskTopology(mountpoints []string) diskTopology {
	topology := diskTopology{
		groups: make([]diskGroup, 0, len(mountpoints)),
		list:   append([]string(nil), mountpoints...),
	}
	for _, mountpoint := range mountpoints {
		topology.groups = append(topology.groups, diskGroup{
			id: "include:" + mountpoint,
			candidates: []disk.PartitionStat{{
				Mountpoint: mountpoint,
			}},
		})
	}
	return topology
}

func buildAutomaticDiskTopology(partitions []disk.PartitionStat) diskTopology {
	topology := diskTopology{}
	groupIndexes := make(map[string]int)
	for _, partition := range partitions {
		if !isPhysicalDisk(partition) {
			continue
		}
		id := diskDeviceID(partition)
		if index, exists := groupIndexes[id]; exists {
			topology.groups[index].candidates = append(topology.groups[index].candidates, partition)
			continue
		}
		groupIndexes[id] = len(topology.groups)
		topology.groups = append(topology.groups, diskGroup{id: id, candidates: []disk.PartitionStat{partition}})
	}

	topology.list = make([]string, 0, len(topology.groups))
	for _, group := range topology.groups {
		partition := group.candidates[0]
		for _, candidate := range group.candidates[1:] {
			if len(candidate.Mountpoint) < len(partition.Mountpoint) ||
				(len(candidate.Mountpoint) == len(partition.Mountpoint) && candidate.Mountpoint < partition.Mountpoint) {
				partition = candidate
			}
		}
		topology.list = append(topology.list, fmt.Sprintf("%s (%s)", partition.Mountpoint, partition.Fstype))
	}
	sort.Strings(topology.list)
	return topology
}

func diskDeviceID(partition disk.PartitionStat) string {
	deviceID := partition.Device
	if strings.EqualFold(partition.Fstype, "zfs") {
		if index := strings.Index(deviceID, "/"); index >= 0 {
			deviceID = deviceID[:index]
		}
	}
	if deviceID == "" {
		return "mount:" + partition.Mountpoint
	}
	return "device:" + deviceID
}

func equalDiskTopology(left, right diskTopology) bool {
	if len(left.groups) != len(right.groups) {
		return false
	}
	for index := range left.groups {
		leftGroup, rightGroup := left.groups[index], right.groups[index]
		if leftGroup.id != rightGroup.id || len(leftGroup.candidates) != len(rightGroup.candidates) {
			return false
		}
		for candidateIndex := range leftGroup.candidates {
			leftCandidate := leftGroup.candidates[candidateIndex]
			rightCandidate := rightGroup.candidates[candidateIndex]
			if leftCandidate.Device != rightCandidate.Device ||
				leftCandidate.Mountpoint != rightCandidate.Mountpoint ||
				leftCandidate.Fstype != rightCandidate.Fstype {
				return false
			}
		}
	}
	return true
}

func sampleDiskTopology(
	topology diskTopology,
	previous map[string]DiskInfo,
	usage diskUsageSource,
) (DiskInfo, map[string]DiskInfo, error) {
	groupSamples := make(map[string]DiskInfo, len(topology.groups))
	failedQueries := 0
	var firstErr error

	for _, group := range topology.groups {
		var best DiskInfo
		found := false
		for _, candidate := range group.candidates {
			stat, err := usage(candidate.Mountpoint)
			if err != nil {
				failedQueries++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if stat == nil {
				failedQueries++
				if firstErr == nil {
					firstErr = errors.New("disk usage source returned nil")
				}
				continue
			}
			if !found || stat.Total > best.Total {
				best = DiskInfo{Total: stat.Total, Used: min(stat.Used, stat.Total)}
				found = true
			}
		}
		if found {
			groupSamples[group.id] = best
		} else if stale, exists := previous[group.id]; exists {
			groupSamples[group.id] = stale
		}
	}

	var result DiskInfo
	for _, value := range groupSamples {
		result.Total = saturatingAdd(result.Total, value.Total)
		result.Used = saturatingAdd(result.Used, value.Used)
	}
	if result.Used > result.Total {
		result.Used = result.Total
	}
	if failedQueries > 0 {
		return result, groupSamples, fmt.Errorf("%d disk usage queries failed: %w", failedQueries, firstErr)
	}
	return result, groupSamples, nil
}

// isPhysicalDisk reports whether a partition should contribute to the default
// local capacity total.
func isPhysicalDisk(part disk.PartitionStat) bool {
	// LXC and similar environments may expose a loop/overlay root; always keep
	// the root mount because it is the capacity visible to the container.
	if part.Mountpoint == "/" {
		return true
	}
	mountpoint := strings.ToLower(part.Mountpoint)
	mountpointsToExcludePrefix := []string{
		"/tmp",
		"/var/tmp",
		"/dev",
		"/run",
		"/var/lib/containers",
		"/var/lib/docker",
		"/proc",
		"/sys",
		"/sys/fs/cgroup",
		"/etc/resolv.conf",
		"/etc/host",
		"/nix/store",
	}
	for _, mountpointPrefix := range mountpointsToExcludePrefix {
		if mountpoint == mountpointPrefix || strings.HasPrefix(mountpoint, mountpointPrefix) {
			return false
		}
	}

	fstype := strings.ToLower(part.Fstype)
	if fstype == "autofs" && !strings.HasPrefix(part.Device, "/dev/") {
		return false
	}
	if fstype == "fuseblk" {
		return true
	}
	fstypesToExclude := []string{
		"tmpfs", "devtmpfs", "udev", "nfs", "cifs", "smb", "vboxsf", "9p", "fuse",
		"overlay", "proc", "devpts", "sysfs", "cgroup", "mqueue", "hugetlbfs", "debugfs",
		"binfmt_misc", "securityfs",
	}
	for _, excludedFSType := range fstypesToExclude {
		if fstype == excludedFSType || strings.HasPrefix(fstype, excludedFSType) {
			return false
		}
	}

	opts := strings.ToLower(strings.Join(part.Opts, ","))
	if strings.Contains(opts, "remote") || strings.Contains(opts, "network") {
		return false
	}
	return !strings.HasPrefix(part.Device, "/dev/loop")
}

func DiskList() ([]string, error) {
	return defaultDiskSampler.List(flags.IncludeMountpoints, false)
}
