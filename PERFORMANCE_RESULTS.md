# Komari Agent 性能重构结果

基线：[`PERFORMANCE_BASELINE.md`](PERFORMANCE_BASELINE.md)

所有结果必须在相同环境、相同命令下理解。本文件记录每个性能 Todo 的方向性证据；最终发布前会在受控环境重新执行完整 benchmark 和长稳测试。

## A-102 CPU 与网络非阻塞差分采样

环境与基线文档相同。验证命令：

```sh
go test ./monitoring/unit ./monitoring \
  -run '^$' \
  -bench 'Benchmark(CPU|NetworkSpeed|GenerateReport)$' \
  -benchtime=1x -benchmem -count=10
```

| Benchmark | 修改前 | 修改后典型值 | 结果 |
|---|---:|---:|---:|
| CPU | 1,001,848,791 ns/op | 474,500 ns/op | 约 2,111× 更快，删除固定 1 秒等待 |
| NetworkSpeed | 1,016,193,666 ns/op | 2,233,708 ns/op | 约 455× 更快，删除固定 1 秒等待 |
| GenerateReport | 2,134,791,458 ns/op | 97,713,292 ns/op | 约 21.8× 更快 |

修改后十次 GenerateReport 范围约 96.6～121.3ms。剩余主要耗时和分配来自 socket/process 全量采样、静态 CPU 信息重复读取、磁盘枚举和动态 map 编码，将由 A-103～A-106 继续消除。

正确性验证包括：

- CPU 首次样本、正常差分、计数器重置和 0～100 边界；
- 网络首次样本、真实 elapsed、counter reset、NIC include/exclude；
- source 错误传播；
- 旧 JSON 字段和公开函数签名保持不变；
- 完整 unit、vet、race 和 benchmark 回归。

## A-103 内存共享采样与静态主机信息缓存

Linux 报告路径现在只读取一次 `/proc/meminfo`，由同一不可变输入同时计算 RAM 与 Swap；CPU 型号、架构、核心数、OS、kernel 和虚拟化信息采用并发安全的启动缓存，并提供显式刷新入口。刷新失败保留最后一个有效值，首次失败则允许后续调用重试。

验证命令：

```sh
go test ./monitoring/unit ./monitoring \
  -run '^$' \
  -bench 'Benchmark(Memory|RAM|Swap|StaticHostInfoCached|CPU|GenerateReport)$' \
  -benchtime=1x -benchmem -count=10
```

Apple M4/macOS 热路径结果：

| Benchmark | A-102 后 | A-103 后典型值 | 结果 |
|---|---:|---:|---:|
| CPU | 474,500 ns/op | 14,042～54,000 ns/op | 静态信息不再重复探测；热调用约快 9～34× |
| StaticHostInfoCached | 无 | 42～125 ns/op，0 alloc | 读缓存为极低开销热路径 |
| GenerateReport allocations | 约 4,236 allocs/op（原始基线） | 1,123～1,134 allocs/op | 热报告分配约减少 73% |

完整报告仍约 95～99ms，说明现阶段时延主导项已经转移到 socket/process 全量枚举；A-105 将针对这一热点。macOS 没有 `/proc/meminfo`，其 `Memory` 仍走原生平台 fallback（典型 16～32µs）；Linux 的共享读取和计算由固定 fixture、边界计数器与 race 测试覆盖。

正确性验证包括：

- 32 路并发首次读取只调用一次静态信息 loader；
- 显式刷新发布新 generation，刷新失败保留旧值，首次失败可重试；
- RAM/Swap 由同一 Linux fixture 计算，`includeCache` 与 htop-like 语义保持；
- 异常大计数器不会整数下溢/溢出，也不会报告 `used > total`；
- basic-info 和周期 report 复用同一静态缓存与内存快照。

## A-104 磁盘拓扑缓存和低频容量采样

磁盘采样现在把“分区拓扑发现”和“容量查询”拆为两个独立周期：默认每 5 分钟刷新挂载拓扑、每 30 秒刷新容量。报告热路径只读取带锁快照；配置或挂载事件可调用 `RefreshDiskTopology` 立即刷新。自定义 mountpoint 在配置进入采样器时完成去空白和去重，不再枚举全部分区。

验证命令：

```sh
go test ./monitoring/unit ./monitoring \
  -run '^$' \
  -bench 'Benchmark(Disk|DiskTopologyAndUsageRefresh|GenerateReport)$' \
  -benchtime=100x -benchmem -count=5
```

Apple M4/macOS 结果：

| Benchmark | 修改前 | A-104 后 | 结果 |
|---|---:|---:|---:|
| Disk 热路径 | 155,208 ns/op，90,344 B，270 allocs | 78～80 ns/op，0 B，0 alloc | 约 1,940～1,990× 更快，删除每报告分区枚举 |
| 4 mount fixture 强制拓扑+容量刷新 | 无 | 1.57～1.80µs，约 1.66KB，27 allocs | 刷新成本独立且有界 |
| GenerateReport allocations | 1,123～1,134 allocs/op | 857～863 allocs/op | 在 A-103 基础上再减少约 24% |

GenerateReport 仍约 94.5～95.7ms；磁盘已经不再是报告热路径的平台调用，剩余主要耗时是 socket/process 枚举。

正确性验证包括：

- 复杂 mount fixture 过滤 tmpfs、NFS、loop 与容器临时挂载；
- 重复设备与 ZFS pool 分组，容量选取最大可见 dataset，避免 quota 重复统计；
- 自定义 mountpoint 预解析、去重且完全绕过分区枚举；
- fake clock 验证 30 秒容量周期、5 分钟拓扑周期和时钟回拨；
- 热插拔强制刷新、拓扑失败退避、单 mount 失败保留该组最后有效值；
- 32 路并发冷启动只有一次拓扑/容量 source pass；
- 超大平台计数器采用饱和加法，保证 `used <= total`。
