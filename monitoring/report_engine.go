package monitoring

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/monitoring/sampler"
	unit "github.com/komari-monitor/komari-agent/monitoring/unit"
)

var flags = pkg_flags.GlobalConfig

type reportSources struct {
	cpu         func() unit.CpuInfo
	memory      func() unit.MemoryInfo
	load        func() unit.LoadInfo
	disk        func() unit.DiskInfo
	network     func() (uint64, uint64, uint64, uint64, error)
	connections func() (int, int, error)
	uptime      func() (uint64, error)
	process     func() int
	gpu         func(context.Context) ([]unit.DetailedGPUInfo, error)
	gpuModels   func(context.Context) ([]string, error)
}

func defaultReportSources() reportSources {
	return reportSources{
		cpu:         unit.Cpu,
		memory:      unit.Memory,
		load:        unit.Load,
		disk:        unit.Disk,
		network:     unit.NetworkSpeed,
		connections: unit.ConnectionsCount,
		uptime:      unit.Uptime,
		process:     unit.ProcessCount,
		gpu:         unit.GetDetailedGPUInfoContext,
		gpuModels:   unit.GetDetailedGPUHostContext,
	}
}

type reportEngineConfig struct {
	enableGPU          bool
	fastInterval       time.Duration
	uptimeInterval     time.Duration
	connectionInterval time.Duration
	processInterval    time.Duration
	diskInterval       time.Duration
	gpuInterval        time.Duration
}

func defaultReportEngineConfig(enableGPU bool) reportEngineConfig {
	return reportEngineConfig{
		enableGPU:          enableGPU,
		fastInterval:       time.Second,
		uptimeInterval:     5 * time.Second,
		connectionInterval: 5 * time.Second,
		processInterval:    5 * time.Second,
		diskInterval:       30 * time.Second,
		gpuInterval:        3 * time.Second,
	}
}

type ReportEngine struct {
	store   *reportSnapshotStore
	runtime *sampler.Runtime
}

func newReportEngine(sources reportSources, config reportEngineConfig, now func() time.Time) (*ReportEngine, error) {
	store := newReportSnapshotStore(now)
	specs := buildReportSpecs(store, sources, config)
	runtime, err := sampler.New(specs)
	if err != nil {
		return nil, err
	}
	return &ReportEngine{store: store, runtime: runtime}, nil
}

func (engine *ReportEngine) Start(ctx context.Context) error {
	return engine.runtime.Start(ctx)
}

func (engine *ReportEngine) Stop() {
	engine.runtime.Stop()
}

func (engine *ReportEngine) Snapshot() ReportSnapshot {
	return engine.store.snapshot()
}

func (engine *ReportEngine) EncodeV1() ([]byte, error) {
	return encodeReportV1(engine.store.load())
}

func buildReportSpecs(store *reportSnapshotStore, sources reportSources, config reportEngineConfig) []sampler.Spec {
	specs := []sampler.Spec{
		reportSpec("cpu", config.fastInterval, func(ctx context.Context) error {
			started := time.Now()
			if err := ctx.Err(); err != nil {
				return err
			}
			value := sources.cpu()
			err := ctx.Err()
			diagnostics.ObserveSampler(diagnostics.SamplerCPU, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) {
					usage := finiteFloat(value.CPUUsage)
					if usage <= 0.001 {
						usage = 0.001
					}
					snapshot.CPU = CPUReport{Usage: usage}
				}
			}
			store.publish(reportSampleCPU, config.fastInterval*3, err, update)
			return err
		}),
		reportSpec("memory", config.fastInterval, func(ctx context.Context) error {
			started := time.Now()
			if err := ctx.Err(); err != nil {
				return err
			}
			value := sources.memory()
			err := ctx.Err()
			diagnostics.ObserveSampler(diagnostics.SamplerRAM, started, err)
			diagnostics.ObserveSampler(diagnostics.SamplerSwap, time.Now(), err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) {
					snapshot.RAM = MemoryReport{Total: value.RAM.Total, Used: value.RAM.Used}
					snapshot.Swap = MemoryReport{Total: value.Swap.Total, Used: value.Swap.Used}
				}
			}
			store.publish(reportSampleMemory, config.fastInterval*3, err, update)
			return err
		}),
		reportSpec("load", config.fastInterval, func(ctx context.Context) error {
			started := time.Now()
			if err := ctx.Err(); err != nil {
				return err
			}
			value := sources.load()
			err := ctx.Err()
			diagnostics.ObserveSampler(diagnostics.SamplerLoad, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) {
					snapshot.Load = LoadReport{Load1: value.Load1, Load5: value.Load5, Load15: value.Load15}
				}
			}
			store.publish(reportSampleLoad, config.fastInterval*3, err, update)
			return err
		}),
		reportSpec("network", config.fastInterval, func(ctx context.Context) error {
			started := time.Now()
			totalUp, totalDown, up, down, err := sources.network()
			if err == nil {
				err = ctx.Err()
			}
			diagnostics.ObserveSampler(diagnostics.SamplerNetwork, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) {
					snapshot.Network = NetworkReport{Down: down, TotalDown: totalDown, TotalUp: totalUp, Up: up}
				}
			}
			store.publish(reportSampleNetwork, config.fastInterval*3, err, update)
			return err
		}),
		reportSpec("uptime", config.uptimeInterval, func(ctx context.Context) error {
			started := time.Now()
			value, err := sources.uptime()
			if err == nil {
				err = ctx.Err()
			}
			diagnostics.ObserveSampler(diagnostics.SamplerUptime, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) { snapshot.Uptime = value }
			}
			store.publish(reportSampleUptime, config.uptimeInterval*3, err, update)
			return err
		}),
		reportSpec("connections", config.connectionInterval, func(ctx context.Context) error {
			started := time.Now()
			tcp, udp, err := sources.connections()
			if err == nil {
				err = ctx.Err()
			}
			diagnostics.ObserveSampler(diagnostics.SamplerConnections, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) {
					snapshot.Connections = ConnectionsReport{TCP: tcp, UDP: udp}
				}
			}
			store.publish(reportSampleConnections, config.connectionInterval*3, err, update)
			return err
		}),
		reportSpec("process", config.processInterval, func(ctx context.Context) error {
			started := time.Now()
			if err := ctx.Err(); err != nil {
				return err
			}
			value := sources.process()
			err := ctx.Err()
			diagnostics.ObserveSampler(diagnostics.SamplerProcess, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) { snapshot.Process = value }
			}
			store.publish(reportSampleProcess, config.processInterval*3, err, update)
			return err
		}),
		reportSpec("disk", config.diskInterval, func(ctx context.Context) error {
			started := time.Now()
			if err := ctx.Err(); err != nil {
				return err
			}
			value := sources.disk()
			err := ctx.Err()
			diagnostics.ObserveSampler(diagnostics.SamplerDisk, started, err)
			var update func(*ReportSnapshot)
			if err == nil {
				update = func(snapshot *ReportSnapshot) {
					snapshot.Disk = DiskReport{Total: value.Total, Used: value.Used}
				}
			}
			store.publish(reportSampleDisk, config.diskInterval*3, err, update)
			return err
		}),
	}
	if config.enableGPU {
		specs = append(specs, reportSpec("gpu", config.gpuInterval, func(ctx context.Context) error {
			started := time.Now()
			values, err := sources.gpu(ctx)
			var gpu *GPUReport
			if err == nil {
				gpu = detailedGPUReport(values)
			} else if models, modelErr := sources.gpuModels(ctx); modelErr == nil && len(models) > 0 {
				gpu = &GPUReport{Models: append([]string(nil), models...)}
			}
			if err == nil {
				err = ctx.Err()
			}
			diagnostics.ObserveSampler(diagnostics.SamplerGPU, started, err)
			var update func(*ReportSnapshot)
			if gpu != nil {
				update = func(snapshot *ReportSnapshot) { snapshot.GPU = cloneGPUReport(gpu) }
			}
			store.publish(reportSampleGPU, config.gpuInterval*3, err, update)
			return err
		}))
	}
	return specs
}

func reportSpec(name string, interval time.Duration, sample func(context.Context) error) sampler.Spec {
	return sampler.Spec{
		Name:            name,
		Interval:        interval,
		Timeout:         min(interval, 5*time.Second),
		StaleAfter:      interval * 3,
		Jitter:          min(interval/10, 250*time.Millisecond),
		ErrorBackoffMin: interval,
		ErrorBackoffMax: interval * 8,
		Sample: func(ctx context.Context) (any, error) {
			return struct{}{}, sample(ctx)
		},
	}
}

func detailedGPUReport(values []unit.DetailedGPUInfo) *GPUReport {
	result := &GPUReport{Count: len(values), Detailed: true, DetailedInfo: make([]GPUDeviceReport, len(values))}
	if len(values) == 0 {
		return result
	}
	for index, value := range values {
		utilization := finiteFloat(value.Utilization)
		result.AverageUsage += utilization
		result.DetailedInfo[index] = GPUDeviceReport{
			MemoryTotal: value.MemoryTotal,
			MemoryUsed:  value.MemoryUsed,
			Name:        value.Name,
			Temperature: value.Temperature,
			Utilization: utilization,
		}
	}
	result.AverageUsage /= float64(len(values))
	return result
}

var (
	defaultReportEngineMu sync.Mutex
	defaultReportEngine   atomic.Pointer[ReportEngine]
	emptyReportSnapshot   = ReportSnapshot{CPU: CPUReport{Usage: 0.001}}
)

func StartReportSampler(ctx context.Context) error {
	if ctx == nil {
		return errors.New("report sampler requires a parent context")
	}
	defaultReportEngineMu.Lock()
	defer defaultReportEngineMu.Unlock()
	if defaultReportEngine.Load() != nil {
		return nil
	}
	engine, err := newReportEngine(defaultReportSources(), defaultReportEngineConfig(flags.EnableGPU), time.Now)
	if err != nil {
		return err
	}
	if err := engine.Start(ctx); err != nil {
		return err
	}
	defaultReportEngine.Store(engine)
	return nil
}

func StopReportSampler() {
	defaultReportEngineMu.Lock()
	engine := defaultReportEngine.Swap(nil)
	defaultReportEngineMu.Unlock()
	if engine != nil {
		engine.Stop()
	}
}

func GenerateReport() []byte {
	started := time.Now()
	snapshot := &emptyReportSnapshot
	if engine := defaultReportEngine.Load(); engine != nil {
		snapshot = engine.store.load()
	}
	encoded, err := encodeReportV1(snapshot)
	if err != nil {
		log.Printf("Failed to marshal report: %v", err)
	}
	diagnostics.ObserveReport(started, len(encoded), err)
	return encoded
}
