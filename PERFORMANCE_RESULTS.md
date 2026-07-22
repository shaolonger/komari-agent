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

## A-105 socket/process 平台优化与低频采样

socket 与 process 统计改为 5 秒低频快照，采样失败保留最后有效值，支持显式事件刷新。Linux socket 直接流式计数 `/proc/net/{tcp,tcp6,udp,udp6}`，不再物化 gopsutil 的完整连接对象；Linux process 使用 256 项分批目录读取和无分配 PID 判断。macOS process 改为原生 `sysctl kern.proc.all`，FreeBSD 使用内核 process API，Windows 使用可增长的 `EnumProcesses` 缓冲且不再每次加载 DLL/解析函数地址。

macOS 平台 source 对比（`-benchtime=1x -count=10`）：

| Source | 修改前 | A-105 后典型值 | 结果 |
|---|---:|---:|---:|
| socket count | 86.12ms，69KB，304 allocs | 32.6～35.2ms，约 62KB，210～213 allocs | TCP/UDP 两次枚举合并为一次，约快 2.4～2.6× |
| process count | 33.46ms，738KB，93 allocs | 83～126µs，549KB，3 allocs | 删除 `ps` 子进程，约快 266～403× |

稳态热路径（`-benchtime=1000x -count=5`）：

| Benchmark | A-105 后 | 结果 |
|---|---:|---:|
| ConnectionsCount | 37.6～41.4ns，0 alloc | 报告只读低频快照 |
| ProcessCount | 37.5～39.4ns，0 alloc | 报告只读低频快照 |
| GenerateReport | 1.712～1.748ms，约 68.5KB，459 allocs | 从 A-104 的约 95ms 再降低约 54～56× |

Linux 确定性压力 fixture：10,000 条 `/proc/net` socket 行约 120～137µs、1,072 B、2 allocs；50,000 个 process 目录名的 PID 分类约 107～110µs、0 分配。该结果不包含内核文件读取时间，但证明解析成本与内存不随连接对象复杂度膨胀。

正确性验证包括：

- 25,000 socket 行、空行、超长恶意行和 IPv4/IPv6 表计数；
- 50,000 process 名称、非 PID 特殊目录和无分配分类；
- 首次失败立即重试，暂时性错误保留 stale，成功后清除错误；
- fake clock 周期、强制刷新、时钟回拨和 64 路并发冷读单 source call；
- 非 Linux TCP/UDP socket 类型分类；
- Linux amd64/arm64、Windows amd64、FreeBSD amd64、macOS arm64 的 `CGO_ENABLED=0 go build ./...`。

## A-106 类型化不可变 Snapshot 与报告编码器

报告系统现在由 A-101 的 context sampler runtime 驱动：CPU、memory、load、network 为 1 秒采样，uptime/socket/process 为 5 秒，disk 为 30 秒，GPU 为 3 秒。每个 worker 独立更新 copy-on-write 的类型化快照；`GenerateReport` 只执行一次 atomic snapshot load 和 JSON v1 编码，不再调用系统 API、读取 `/proc` 或启动命令。

类型化 wire struct 的字段顺序与旧 `map[string]interface{}` 的字典序 JSON 保持一致；CPU 最小值、错误 message、GPU detailed/fallback 和所有 v1 字段保持兼容。编码器复用上限 64KB 的 buffer，输出仍使用独立 byte slice，避免队列/网络异步消费时被覆盖。NaN/Inf 在副本中归零，不会破坏已发布快照或导致整份遥测丢失。

验证命令：

```sh
go test ./monitoring -run '^$' \
  -bench 'Benchmark(GenerateReport|EncodeReportV1)$' \
  -benchtime=100000x -benchmem -count=5
```

Apple M4/macOS 稳态结果：

| Benchmark | A-105 后 | A-106 后 | 结果 |
|---|---:|---:|---:|
| GenerateReport | 1.712～1.748ms | 0.621～0.877µs | 报告构建约快 1,950～2,810× |
| allocations | 459 allocs/op | 3 allocs/op | 减少 99.35% |
| allocated bytes | 约 68.5KB/op | 1,313 B/op | 减少约 98.1% |
| EncodeReportV1 | 无 | 0.605～0.642µs | 366-byte v1 fixture |

与最初 2.135 秒的串行报告基线相比，稳态报告构建约快 240 万～340 万倍；系统采样成本由独立 worker 按其自身频率承担，不再叠加到发送时延。

正确性验证包括：

- 无 GPU、GPU fallback、GPU detailed 空数组和错误 message 的 JSON v1 byte golden；
- copy-on-write 发布时复制 GPU slice，外部 source 和 snapshot reader 均无法修改内部状态；
- stale deadline、失败后保留最后值和 metadata 错误；
- NaN/Inf 清洗不修改调用方快照；
- 并发 publish/encode 的 race 回归；
- fake platform sources 采样完成后连续编码 1,000 次，source 调用数严格不变；
- 全量 unit、vet、race 与 Linux/Windows/FreeBSD 静态构建。

## A-107 Agent 协议 v2 与 v1 安全回退

Agent 现在按标准 WebSocket subprotocol 依次声明 `komari.telemetry.v2`、`komari.telemetry.v1`。服务端选择 v2 时发送 binary frame；旧服务端没有选择、显式选择 v1 或代理剥离协商头时继续发送原 JSON text frame。未知选择直接中止连接；任意 v2 编码错误都会在同一发送周期回退 JSON v1 text，不会丢失遥测。

v2 使用固定 schema：16-byte `KMR2` header、version、flags、精确 payload length、schema ID 和 little-endian typed payload。最大帧 64KB、message 4KB、GPU 64 个、名称 256 UTF-8 bytes。encoder/decoder 拒绝未知版本/flag/schema、截断/尾随、非法 UTF-8、NaN/Inf、负/溢出计数和 `used > total`。完整 schema：[`protocol/telemetryv2/SCHEMA.md`](protocol/telemetryv2/SCHEMA.md)。

跨仓库 golden：[`protocol/telemetryv2/testdata/report_v2.hex`](protocol/telemetryv2/testdata/report_v2.hex)，与 Komari 服务端同路径 fixture 逐字节一致；双方分别编码/解码并验证 v1/v2 字段一致。

Apple M4/macOS、100,000 次稳态 benchmark：

| Encoder | 帧大小 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| JSON v1（无 GPU） | 366 B | 607～844 | 1,313 | 3 |
| Binary v2（无 GPU） | 150 B | 66～72 | 288 | 2 |
| Binary v2 protocol detailed fixture | 199 B | 52～78 | 256 | 1 |

无 GPU v2 帧缩小约 59%，编码约快 8.4～12.8×，分配字节减少约 78%。协议 detailed fixture decoder 约 104～121ns、118 B、4 allocs；Komari 端完成 `common.Report` 转换约 178～191ns。

正确性和安全验证包括：

- v1/v2 全字段一致、detailed GPU 与 models fallback round trip；
- 无协商/v1/v2/未知 subprotocol 的降级和 fail-closed；
- terminal/generic WebSocket dialer 不携带 telemetry subprotocol；
- v2 binary、v1 text 和 v2 编码失败 text fallback 的消息类型；
- 全部截断前缀、header 字段破坏、尾随/超限数据和 fuzz seed；
- frame/string/GPU/整数/浮点边界；
- Komari 服务端 K-104 的 full unit/vet/race 与 cross-repo fixture 验收。
