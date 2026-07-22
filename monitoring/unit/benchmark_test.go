package monitoring

import (
	"bytes"
	"testing"
)

var (
	benchmarkCPU         CpuInfo
	benchmarkRAM         RamInfo
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

func BenchmarkDisk(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkDisk = Disk()
	}
}

func BenchmarkConnectionsCount(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkTCPCount, benchmarkUDPCount, _ = ConnectionsCount()
	}
}

func BenchmarkProcessCount(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		benchmarkProcess = ProcessCount()
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
