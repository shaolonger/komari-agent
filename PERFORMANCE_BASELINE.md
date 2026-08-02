# Komari Agent 性能基线

采集日期：2026-07-22  
对应任务：A-002  
基线提交：`3622ac3` 加入 benchmark 前的生产实现  
运行命令：

```sh
go test ./... -run '^$' -bench . -benchtime=1x -benchmem
```

## 环境

- OS：macOS 26.5.2（25F84）
- Architecture：arm64
- CPU：Apple M4，10 logical CPUs
- Memory：32 GiB
- Go：go1.26.4 darwin/arm64

本文件是方向性基线，不是跨硬件容量承诺。`-benchtime=1x` 用于让包含 legacy 一秒阻塞的采样器可以快速、可重复地运行。优化验收时必须在相同机器、相同电源状态和相同命令下重复多次，并使用 benchstat 比较。

## 端到端报告

| Benchmark | ns/op | B/op | allocs/op | 额外指标 |
|---|---:|---:|---:|---:|
| GenerateReport | 2,134,791,458 | 1,240,832 | 4,236 | 392 bytes/report |

端到端报告超过 2.13 秒，直接证实默认 1 秒采集周期无法稳定兑现。主要固定等待来自 CPU 百分比采样和网络速率采样。

## 系统采样

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| CPU | 1,001,848,791 | 215,552 | 2,852 |
| NetworkSpeed | 1,016,193,666 | 114,544 | 502 |
| RAM | 137,750 | 2,888 | 59 |
| Swap | 26,208 | 192 | 4 |
| Disk | 155,208 | 90,344 | 270 |
| ConnectionsCount | 86,116,667 | 69,432 | 304 |
| ProcessCount | 33,461,750 | 737,952 | 93 |

CPU 和 NetworkSpeed 各自约一秒；socket/process 在当前 macOS 主机上也明显昂贵，因此必须移出每秒报告热路径。

### 当前每份报告的平台调用结构

| 采样项 | 当前调用/命令次数 |
|---|---|
| CPU | `cpu.Info` 1、`cpu.Counts` 1、`cpu.Percent` 1（内部等待 1 秒） |
| RAM + Swap | 两条独立系统内存采样路径 |
| Disk | `Partitions` 1，另加每个候选挂载点一次 `Usage` |
| Network | `IOCounters` 2，中间等待 1 秒 |
| Connections | `Connections("tcp")` 1、`Connections("udp")` 1，并物化全部 socket |
| Process（本机 macOS） | 外部 `ps -A` 命令 1 |
| GPU disabled | 外部动态采样命令 0，但 Linux package 初始化仍可能探测一次 vendor |
| GPU enabled | 每报告至少一个 `nvidia-smi` 或 `rocm-smi` 命令，错误降级可能增加调用 |

上述计数来自当前调用图和确定性 fixture；A-101 后 sampler source 会提供可注入计数器，使 CI 能直接断言每个采样周期的实际 source 调用次数。

## 确定性解析与长期流量

| Benchmark | ns/op | B/op | allocs/op | Fixture |
|---|---:|---:|---:|---|
| Proc CPU info parse | 22,917 | 4,424 | 9 | fake `/proc/cpuinfo` |
| Proc meminfo parse | 55,208 | 5,552 | 28 | fake `/proc/meminfo` |
| NVIDIA detailed parse | 125,791 | 13,464 | 288 | 2 GPU XML |
| AMD detailed parse | 106,208 | 2,408 | 49 | 2 GPU JSON |
| 31-day traffic sum | 64,208 | 400 | 2 | 8 NIC × 4,464 buckets |

GPU 数字只覆盖已有输出的解析，不包含启动外部命令；A-301 会分别测量命令/API adapter。31 天流量求和当前每次线性扫描 35,712 个桶。

## HTTP 与请求构造

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| New verified HTTP client | 2,292 | 968 | 4 |
| New configured HTTP client | 1,500 | 968 | 4 |
| New JSON control request | 145,417 | 1,704 | 17 |
| New WebSocket headers | 60,291 | 5,536 | 21 |

客户端构造本身延迟不大，但每次新建 Transport 会丢失连接池并让 TLS/DNS 成为真实网络路径上的主要成本，A-203/A-204 负责改造。

## 后续比较规则

1. 每项采样重构至少保留本表对应 benchmark；
2. 报告热路径不得再包含固定等待；
3. benchmark 使用 `-count=10` 生成结果并由 benchstat 比较；
4. CI smoke 使用短 benchtime，专用性能 runner 使用固定 `-count`；
5. OS 采样 benchmark 必须按平台分别存档；
6. 任何为了降低数字而跳过安全验证或改变业务语义的结果无效。
