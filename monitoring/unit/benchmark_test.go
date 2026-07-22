package monitoring

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

var (
	benchmarkCPU         CpuInfo
	benchmarkRAM         RamInfo
	benchmarkMemory      MemoryInfo
	benchmarkStaticHost  StaticHostInfo
	benchmarkDisk        DiskInfo
	benchmarkTCPCount    int
	benchmarkUDPCount    int
	benchmarkProcess     int
	benchmarkNvidiaInfo  []NVIDIAGPUInfo
	benchmarkAMDInfo     []AMDGPUInfo
	benchmarkCPUName     string
	benchmarkProcMemory  *ProcMemInfo
	benchmarkNetworkUp   uint64
	benchmarkNetworkDown uint64
	benchmarkProcNetRows int
	benchmarkPIDCount    int
)

func BenchmarkCPU(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkCPU = Cpu()
	}
}

func BenchmarkRAM(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkRAM = Ram()
	}
}

func BenchmarkSwap(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkRAM = Swap()
	}
}

func BenchmarkMemory(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkMemory = Memory()
	}
}

func BenchmarkStaticHostInfoCached(b *testing.B) {
	benchmarkStaticHost = GetStaticHostInfo()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkStaticHost = GetStaticHostInfo()
	}
}

func BenchmarkDisk(b *testing.B) {
	benchmarkDisk = Disk()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkDisk = Disk()
	}
}

func BenchmarkDiskTopologyAndUsageRefresh(b *testing.B) {
	parts := []disk.PartitionStat{
		{Device: "/dev/root", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/data", Mountpoint: "/data", Fstype: "xfs"},
		{Device: "pool/root", Mountpoint: "/tank", Fstype: "zfs"},
		{Device: "pool/dataset", Mountpoint: "/tank/dataset", Fstype: "zfs"},
	}
	sampler := newDiskSampler(
		func(bool) ([]disk.PartitionStat, error) { return parts, nil },
		func(string) (*disk.UsageStat, error) {
			return &disk.UsageStat{Total: 1 << 40, Used: 1 << 39}, nil
		},
		time.Now,
		time.Hour,
		time.Hour,
	)
	b.ReportAllocs()
	for range b.N {
		benchmarkDisk, _ = sampler.Sample("", true, true)
	}
}

func BenchmarkConnectionsCount(b *testing.B) {
	benchmarkTCPCount, benchmarkUDPCount, _ = ConnectionsCount()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkTCPCount, benchmarkUDPCount, _ = ConnectionsCount()
	}
}

func BenchmarkProcessCount(b *testing.B) {
	benchmarkProcess = ProcessCount()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkProcess = ProcessCount()
	}
}

func BenchmarkConnectionsCountPlatform(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		counts, _ := connectionCountPlatform()
		benchmarkTCPCount, benchmarkUDPCount = counts.tcp, counts.udp
	}
}

func BenchmarkProcessCountPlatform(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkProcess, _ = processCountPlatform()
	}
}

func BenchmarkProcNetTableCount(b *testing.B) {
	const rows = 10_000
	var fixture bytes.Buffer
	fixture.Grow(len(procNetHeader) + rows*96)
	fixture.WriteString(procNetHeader)
	line := []byte("    0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 1\n")
	for range rows {
		fixture.Write(line)
	}
	data := fixture.Bytes()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		benchmarkProcNetRows, _ = countProcNetTable(bytes.NewReader(data))
	}
}

func BenchmarkProcessDirectoryNameCount(b *testing.B) {
	const processes = 50_000
	names := make([]string, 0, processes+2)
	for index := range processes {
		names = append(names, strconv.Itoa(index+1))
	}
	names = append(names, "self", "thread-self")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkPIDCount = countDecimalPIDs(names)
	}
}

func BenchmarkNetworkSpeed(b *testing.B) {
	originalMonthRotate := flags.MonthRotate
	flags.MonthRotate = 0
	b.Cleanup(func() { flags.MonthRotate = originalMonthRotate })
	b.ReportAllocs()
	for range b.N {
		_, _, benchmarkNetworkUp, benchmarkNetworkDown, _ = NetworkSpeed()
	}
}

func BenchmarkProcCPUInfoParsing(b *testing.B) {
	data := []byte(benchmarkProcCPUInfo)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		benchmarkCPUName, _ = readCPUName(bytes.NewReader(data))
	}
}

func BenchmarkProcMeminfoParsing(b *testing.B) {
	data := []byte(benchmarkProcMeminfo)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		benchmarkProcMemory, _ = readProcMeminfo(bytes.NewReader(data))
	}
}

func BenchmarkNvidiaDetailedInfoParsing(b *testing.B) {
	smi := NvidiaSMI{data: []byte(benchmarkNvidiaXML)}
	b.ReportAllocs()
	b.SetBytes(int64(len(smi.data)))
	for range b.N {
		benchmarkNvidiaInfo, _ = smi.GatherDetailedInfo()
	}
}

func BenchmarkAMDDetailedInfoParsing(b *testing.B) {
	smi := ROCmSMI{data: []byte(benchmarkAMDJSON)}
	b.ReportAllocs()
	b.SetBytes(int64(len(smi.data)))
	for range b.N {
		benchmarkAMDInfo, _ = smi.GatherDetailedInfo()
	}
}

const benchmarkNvidiaXML = `
<nvidia_smi_log>
  <gpu>
    <product_name>NVIDIA Benchmark GPU 0</product_name>
    <fb_memory_usage><total>24576 MiB</total><used>4096 MiB</used><free>20480 MiB</free></fb_memory_usage>
    <utilization><gpu_util>37 %</gpu_util></utilization>
    <temperature><gpu_temp>61 C</gpu_temp></temperature>
  </gpu>
  <gpu>
    <product_name>NVIDIA Benchmark GPU 1</product_name>
    <fb_memory_usage><total>24576 MiB</total><used>8192 MiB</used><free>16384 MiB</free></fb_memory_usage>
    <utilization><gpu_util>72 %</gpu_util></utilization>
    <temperature><gpu_temp>68 C</gpu_temp></temperature>
  </gpu>
</nvidia_smi_log>`

const benchmarkAMDJSON = `{
  "card0": {
    "Card series": "AMD Benchmark GPU 0",
    "GPU use (%)": "42",
    "VRAM Total Memory (B)": "25769803776",
    "VRAM Total Used Memory (B)": "4294967296",
    "Temperature (Sensor junction) (C)": "63"
  },
  "card1": {
    "Card series": "AMD Benchmark GPU 1",
    "GPU use (%)": "76",
    "VRAM Total Memory (B)": "25769803776",
    "VRAM Total Used Memory (B)": "8589934592",
    "Temperature (Sensor junction) (C)": "71"
  }
}`

const benchmarkProcCPUInfo = `processor : 0
vendor_id : GenuineIntel
model name : Benchmark CPU
Processor : Benchmark ARM Processor rev 1
`

const benchmarkProcMeminfo = `MemTotal:       32768000 kB
MemFree:         1024000 kB
MemAvailable:   16384000 kB
Buffers:          262144 kB
Cached:          8388608 kB
SwapCached:        65536 kB
SwapTotal:       4194304 kB
SwapFree:        3145728 kB
Shmem:            131072 kB
SReclaimable:     524288 kB
Zswap:                 0 kB
Zswapped:              0 kB
`
