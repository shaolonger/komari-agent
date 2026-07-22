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
