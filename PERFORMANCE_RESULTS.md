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

## A-201 Context 驱动的 WebSocket 状态机

主 WebSocket 已从 ticker 内嵌套重试循环改为 context 驱动的 connection generation：启动后立即拨号并立即发送第一份快照；reader、telemetry writer 和 heartbeat worker 共享同一个 generation context，任一读写错误都会取消其余 worker、关闭该连接并等待全部 worker 退出后再创建下一代。

连接失败采用指数上限 + full jitter；已连接后的第一次读失败立即重连。若服务端反复 accept 后立即 close，则第二次短连接起进入 full-jitter 退避，避免形成无上限热循环。拨号和退避均服从 parent context，不再使用不可取消的 `time.Sleep`。

连接边界：

- 控制帧 read limit 64KB；非 text 控制消息 fail closed；
- heartbeat 30 秒，read/pong deadline 75 秒；每次 Pong 原子延长 deadline；
- 所有报告和 Ping 写入设置 10 秒 write deadline；
- 关闭直接中断 Gorilla read/write，generation 返回前 join 三个 worker；
- telemetry v2/v1 协商、JSON fallback 与控制能力鉴权语义保持。

相对旧实现，首次连接不再等待第一个 1 秒 data ticker；reader 退出也不再等下一个发送 tick 才发现断线。测试覆盖：

- 首份报告立即发送、读失败即时取消全部 worker；
- heartbeat、parent cancel 和 50 次重复 generation join/leak 回归；
- 慢写被 deadline 有界终止；
- 64KB read limit、binary 控制帧拒绝；
- accept/close 多 generation 隔离和旧连接单次关闭；
- 连接失败指数 cap、full-jitter 范围、retry 上限和重复短连接退避；
- 真实本地 WebSocket 的 half-open 无 Pong 超时，以及正常 Pong 连续延长 read deadline；
- 专项测试重复运行与 `-race`。

## A-202 有界优先级发送队列与可靠 drain

WebSocket 出站路径现在只有一个实际 socket writer。遥测生产器、heartbeat 和 Ping 结果不再并发写 Gorilla connection，而是进入按语义隔离的有界队列：遥测始终只保留最新一帧，heartbeat 最多保留一个，可靠控制结果使用容量 128 的 FIFO。这样即使网络写入速度低于采样速度，内存也不会随 backlog 无界增长。

可靠 FIFO 默认最多跨 connection generation 尝试 3 次；失败写入执行 NACK 并保留队首，下一代连接仍先发送它。每连续发送 8 个可靠帧后会给 heartbeat/最新遥测一次机会，既保持控制结果 FIFO，也避免持续控制流饿死健康检查和监控数据。所有入队 payload 都复制所有权，调用方后续修改不会造成数据竞争或线上帧损坏。

关闭过程先停止 reader/producer，丢弃已陈旧的 heartbeat/telemetry，再给可靠 FIFO 最多 5 秒排空；超时后强制关闭连接并 join 全部 worker。连接异常只清除 ephemeral 帧，不关闭跨代队列。队列深度、遥测合并数、可靠重试、可靠丢弃和 drain timeout 均进入默认关闭、无敏感字段的 diagnostics 聚合指标。

Apple M4/macOS、100,000 次纯内存 benchmark（不含网络 I/O）：

| Dispatch | ns/op | B/op | allocs/op | 说明 |
|---|---:|---:|---:|---|
| A-201 直接调用 writer 基线 | 1.335～1.343 | 0 | 0 | no-op socket writer，仅保留旧调度成本 |
| 最新遥测合并入队 | 41～61 | 112 | 2 | 队列始终最多保留一份最新遥测 |
| 可靠 FIFO 入队 + Take + Ack | 51～59 | 112 | 1 | 包含 payload 所有权复制和完整队列状态转换 |

约 50ns 的可靠调度成本远低于真实网络写入，并换取了确定的内存上限、单写者安全和断线恢复。过载时的核心收益不是缩短一次函数调用，而是把 N 份待发遥测压缩为 1 份，消除慢连接下的无界工作和陈旧数据发送。

正确性与安全验证包括：

- 最新遥测合并、heartbeat 去重、可靠 FIFO 顺序和每 8 帧公平调度；
- 容量耗尽时生产者阻塞，Ack 后恢复，context 取消和 Close 可唤醒全部等待者；
- NACK 跨代保留、3 次上限、耗尽丢弃计数和后继 FIFO 顺序；
- Ping capability 默认关闭时的 `ping_result` 仍作为可靠 text frame 入队，安全策略未放宽；
- shutdown 正常 drain、慢 writer 的 5 秒生产上限语义、连接失败后的可靠帧保留；
- 8 路可靠生产者、10,000 次遥测/heartbeat 合并和单 consumer 并发压力；
- `server` 专项测试 10 次、专项 race 3 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-203 长生命周期、按安全策略隔离的 HTTP Transport

Agent 的 HTTP 路径不再为每次基础信息上报、任务结果上传、自动发现或更新检查新建 Transport。现在按权限建立并长期复用四个物理隔离的 client/connection pool：严格遥测、显式不安全遥测、严格控制结果、严格自动更新。每个 pool 拥有独立 Transport、TLS 配置和 idle connections；全局连接上限为 64、单 host idle 上限为 8、单 host 总连接上限为 16。

`--ignore-unsafe-cert` 只会选择不安全遥测 pool，无法改变严格控制或严格更新的 TLS 配置。所有 pool 至少使用 TLS 1.2；控制结果始终校验证书。更新 client 还拒绝 HTTPS 到 HTTP 的 redirect downgrade。进程启动和更新检查均不再写入 `http.DefaultTransport`/`http.DefaultClient`，因此并发上报、控制和更新之间不存在全局安全策略串扰。

自动更新原依赖无法把显式 client 安全地贯穿到 release asset 下载。新的受控 adapter 使用专属严格 client 读取 GitHub release metadata、目标平台二进制和 `.sha256`，并继续复用既有解包与回滚式原子替换能力。其安全边界包括：60 秒全流程预算、8MB metadata、128MB binary、64KB checksum 上限；只选 stable SemVer 和当前 OS/arch；缺少 checksum、hash 不匹配、HTTP URL/降级、非审计 Transport 全部 fail closed。可选 `GITHUB_TOKEN` 只发往 `api.github.com` metadata 请求，不会随资产重定向泄漏。

每次请求使用自身 context deadline，client 本身不设置会污染连接复用的全局 timeout。任务结果重试现在及时 drain/close 上一次响应后再复用连接，错误和非 200 响应不会遗留不可复用的 body。

Apple M4/macOS、1,000,000 次 client 获取 benchmark：

| Client lookup | A-002 基线 | A-203 后 | 结果 |
|---|---:|---:|---:|
| verified/update | 2,292ns，968 B，4 allocs | 9.1～16.1ns，0 B，0 alloc | 约快 142～252×，删除每请求 Transport |
| configured/telemetry | 1,500ns，968 B，4 allocs | 8.4～9.5ns，0 B，0 alloc | 约快 158～179×，删除每请求 Transport |

真实本地 HTTP 集成测试连续 3 次完整请求只创建 1 条 TCP connection；请求 context 取消在 20ms 预算附近中止慢服务。正确性与安全验证还包括：

- 四种 policy 的 client/Transport 指针完全隔离，同 policy 稳定复用；
- 自签名 TLS 仅显式不安全遥测可连接，严格遥测、控制、更新全部拒绝；
- `IgnoreUnsafeCert=true` 时自动更新仍收到专属 verified client，且进程全局 HTTP 对象保持原值；
- release 最高稳定版本和平台选择、旧 release 缺 checksum 不干扰新有效 release；
- checksum mismatch、短 checksum、最新 release 缺 checksum、超限响应、非法 repo slug 和 HTTP downgrade；
- 64 路并发、每路 1,000 次 policy cache 读取的 race 回归；
- 专项测试 5 次、专项 race 3 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-204 自定义 DNS、Happy Eyeballs 和有界缓存

HTTP 与 WebSocket 的自定义拨号路径现在共用同一个解析/连接算法。DNS 正结果缓存固定为 256 个 hostname、TTL 60 秒；同一 hostname 的并发 cache miss 合并为一次查询，错误不缓存，避免瞬时 DNS 故障形成负缓存。LRU 淘汰保证长期运行内存上限，缓存返回副本保证调用方无法修改共享状态。

自定义 DNS 配置变更会同时清空地址缓存、隔离正在进行的旧 generation lookup，并关闭/重建 A-203 的所有 HTTP Transport。旧 lookup 即使稍后返回也不能覆盖新 generation 的结果。系统或权威 DNS 地址变化最迟在 60 秒 TTL 后重新查询。

连接算法将 DNS 返回值解析为 `netip.Addr`，去除 IPv4-mapped 重复项和非法地址；每份 DNS 结果最多缓存 32 个不同地址，每次拨号最多使用交错后的 16 个地址。IPv4/IPv6 按本机可用性选择首选族并交错，首选地址立即发起，另一地址族默认在 250ms 后竞速；若首个地址立即失败，则不等待 250ms，马上推进下一地址。成功后取消并回收其他 attempt，全部失败保留聚合错误。

DNS lookup 和全部连接 attempt 共享同一个 context/总 timeout。旧实现可能对每个地址各使用完整 timeout 并串行累加；现在无论 DNS 返回多少地址，调用方预算到期都会取消所有并发拨号。`tcp4`/`tcp6` 等显式单栈 network 会严格过滤另一地址族，自定义 resolver 与系统 resolver 均保留支持。

Apple M4/macOS、10,000 次纯解析 benchmark：

| Path | ns/op | B/op | allocs/op | 结果 |
|---|---:|---:|---:|---|
| A-204 前系统 `localhost` lookup | 83,631～86,581 | 304 | 10 | 每次进入系统 resolver |
| A-204 DNS cache hit | 57.9～108.9 | 48 | 1 | 约快 768～1,496× |
| 8 地址 parse + 去重 + 双栈交错 | 352.6～384.1 | 672 | 6 | 只在 cache miss/新结果时执行 |

正确性、资源边界和并发验证包括：

- cache hit 副本所有权、TTL 到期、容量 2 的确定性 LRU 淘汰；
- 64 路同域并发 miss 只调用一次 loader，等待者可独立取消；
- Clear 与旧 in-flight lookup 竞态下新结果不被旧结果覆盖；
- DNS 配置变更同时失效地址与 Transport cache；
- IPv6 首选悬挂时 IPv4 在 fallback delay 后成功，首地址立即失败时零额外等待；
- 全部连接失败、总 deadline、取消、单栈过滤、非法/重复/超量 DNS 响应；
- 专项测试 25 次、专项 race 10 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-205 基础信息与公网 IP 缓存/全局预算

基础信息上报现在把 CPU 型号/核心、架构、OS、kernel、虚拟化、GPU 型号和 Agent 版本保存为进程级不可变快照。并发冷读通过双重检查只执行一次 loader，普通 WebSocket 重连只重新读取内存和磁盘容量，不再重复运行 GPU/系统探测命令。缓存提供显式失效入口；硬件或配置刷新事件可在不引入周期轮询的前提下重建快照。

公网 IP 探测从 IPv4、IPv6 各自串行尝试多个站点，改为两个地址族并行、同一地址族内所有候选 HTTPS 服务竞速。首个经过 `netip` 校验的公网地址会立即取消其余请求；完整探测共享 5 秒总 context 预算，不再让每个失败站点各消耗 15 秒。响应被限制为 16KB，候选端点限制为 8 个，正则只编译一次；非 2xx、畸形地址、错误地址族、私网、loopback、link-local、CGNAT 和文档保留地址全部拒绝。

探测结果按隐私相关配置建立最多 8 项的 LRU：完整成功缓存 1 小时，全部或部分失败缓存 1 分钟；同配置并发冷读合并为一次探测，显式失效和配置 key 变化会重新加载。NIC 模式严格只访问本地接口，绝不回退公网服务；自定义地址先验证地址族，已配置的地址族不会发出网络请求。默认服务全部使用 HTTPS/TLS 1.2+，正文和日志均不记录发现到的公网 IP。

Apple M4/macOS、100,000 次稳态 benchmark：

| Hot path | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 公网 IP cache hit | 81.1～142.2 | 0 | 0 |
| 静态基础信息 cache hit | 10.7～12.1 | 0 | 0 |
| 44-byte 公网地址响应解析/校验 | 636～842 | 377～378 | 7 |

旧实现每次基础信息上报都重新采集静态系统/GPU 信息，并可能串行访问 11 个 IP 端点；理论最坏等待约 165 秒。新实现重连热路径只执行纳秒级缓存读取，首次公网探测有 5 秒硬上限，快服务返回时无需等待慢服务。

正确性、安全和资源边界验证包括：

- 全部服务失败、慢服务取消、非 2xx、Content-Length/streaming 响应炸弹和全局 deadline；
- IPv4/IPv6 地址族、自定义地址校验、敏感地址拒绝、NIC 隐私模式零公网请求；
- 1 小时成功 TTL、1 分钟失败/部分失败 TTL、配置 key、显式失效、容量 8 LRU；
- 64 路并发冷读只执行一次公网 loader，64 路静态信息冷读只执行一次系统 loader；
- 专项测试 10 次、专项 race 5 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-301 GPU 懒初始化、targeted 采样和命令边界

GPU 子系统不再在 Linux package 初始化时执行 `nvidia-smi` 来猜测 vendor。默认 collector 的构造不做文件查询或子进程调用；只有 `--gpu` 显式启用后，GPU sampler 才会查找可用 provider 并采集。基础信息路径在 GPU capability 关闭时直接报告 `None`，不会运行 `system_profiler`、`lspci`、`pciconf` 或详细采集工具；report engine 也不会创建 GPU worker。Linux 基础型号优先读取 sysfs DRM，Windows 保留原生 DXGI，必须使用命令的 fallback 统一进入受控 runner。

首次启用时依次尝试实际可执行的 NVIDIA、AMD provider，静态 UUID/card ID、型号和总显存只采集一次；每 3 秒动态采样只获取已用显存、利用率和温度。64 路并发静态冷读合并为一次 provider 调用，返回值始终复制所有权。显卡拓扑改变会使静态元数据失效并在 backoff 后重建；手动刷新也有显式失效入口。

NVIDIA 从完整 `nvidia-smi -q -x` XML 改成以稳定 UUID 关联的 selective CSV query。静态命令只请求 `uuid,name,memory.total`，动态命令只请求 `uuid,memory.used,utilization.gpu,temperature.gpu`；官方标记为 N/A/Not Supported 的可选动态指标安全归零，真实 NaN、Inf、越界利用率和显存矛盾则拒绝。AMD 从 `--showallinfo` 改为 `showproductname/showuse/showmeminfo/showtemp` 的 JSON 定向查询，card key 排序确保多卡顺序稳定，并兼容 junction/edge/memory 温度字段。

所有 GPU 外部命令直接使用参数数组、不经过 shell，并同时服从 sampler context 和 2 秒内部上限。stdout、stderr 各自最多保留 1MiB，超限后只丢弃后续字节并返回错误；进程等待额外限制为 250ms。provider 发现失败和动态命令失败分别执行 3 秒起、最高 1 分钟的指数冷却，外层 sampler 继续提供独立 backoff 和 stale 快照，持续故障不会形成命令风暴。设备数最多 64、名称最多 256 UTF-8 bytes，动态数值必须有限且在协议边界内。

Apple M4/macOS、`GOMAXPROCS=1`、500,000 次稳态 benchmark：

| Hot path | ns/op | B/op | allocs/op | 结果 |
|---|---:|---:|---:|---|
| NVIDIA 完整 XML detailed parse | 11,619～11,847 | 7,960 | 211 | 同进程兼容基线 |
| NVIDIA targeted dynamic CSV | 351～408 | 336 | 6 | 快 28.5～33.7×，分配次数减少 97.2% |
| AMD targeted typed JSON | 3,089～3,225 | 1,848 | 25 | 相对 A-002 基线约快 33×，分配次数减少 49% |
| cached model adapter | 39.3～42.7 | 16 | 1 | 不执行系统调用/命令 |
| dynamic collector adapter（fake provider） | 95.7～102.4 | 96 | 2 | 含串行化、校验和所有权复制 |

真实收益主要来自每周期只启动一个定向命令、删除启动时命令、静态字段不再重复查询，以及故障时的有界冷却；解析 benchmark 不包含外部工具本身的运行时间。

正确性、安全和资源边界验证包括：

- GPU 关闭时基础信息和 report engine 的 GPU source 调用数严格为零；
- 静态信息并发冷读、缓存所有权、显式失效、动态重复采样、NVIDIA→AMD provider failover；
- provider/dynamic 指数 backoff、错误恢复、拓扑变化后的静态重建和等待者 context 取消；
- NVIDIA/AMD 双卡乱序 fixture、UUID/card ID 对齐、N/A 指标、畸形字段、NaN、显存矛盾和响应上限；
- 真实 helper 子进程的 context 强制终止和 1MiB 输出炸弹；空设备报告保持平均值 0，不产生 NaN；
- 专项测试 10 次、专项 race 5 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-302 Ping 配置预编译与 DNS/IP 固定安全连接

Ping 授权不再为每个任务重复 `Split`/`Atoi` 类型和端口字符串。当前类型、端口、私网开关组成配置 key，首次使用编译为不可变 type bitmask 与端口 set，并通过 atomic pointer 发布；同配置并发读取复用同一快照。只接受 `tcp/http/icmp`，空配置保留原默认值，非空但无效或超过 1KB 的配置 fail closed；最多允许 128 个端口。最大并发被硬限制为 64，最小任务间隔被限制在 0～1 小时，异常大数无法造成 channel 巨额分配或 duration 溢出。

旧路径在授权时只检查 DNS 的第一个地址，执行 ICMP/TCP/HTTP 时再次解析，存在混合公私记录和 DNS rebinding 的 TOCTOU。新路径把任务构造成不可变 target：解析一次 hostname，最多接收 32 个地址，逐个执行 `netip` 校验；只要任意答案属于 loopback、private、link-local、CGNAT、benchmark、documentation 或其他保留范围，整个任务就拒绝。重复项去除后固定第一个已验证地址，后续 ICMP、TCP 和 HTTP 都只使用该数值 IP，不再查询 DNS。IPv4-mapped 地址先 unmap；显式私网 opt-in 仍拒绝 unspecified 和 multicast。

DNS 解析在类型/端口检查、并发 slot 和频率限制之后执行，服从 3 秒 context，并继续使用自定义 DNS 配置。错误对日志只暴露通用类别，不转发 resolver 的潜在敏感细节。hostname 经过 IDNA Lookup 规范化，HTTP 只接受 `http`/`https`，拒绝 userinfo、非法端口、超长 target 和 scoped IPv6 URL。

HTTP 请求保留原始规范化 hostname 作为 URL Host；Transport 关闭代理并只拨固定 IP/端口。HTTPS 显式使用原 hostname 作为 SNI/证书名，最低 TLS 1.2，绝不继承 `--ignore-unsafe-cert`。全部 redirect 禁止跟随，3xx 作为失败，因此 redirect 到私网、不同端口或新域名均不能触发第二次连接。响应头限制为 64KB；A-303 继续负责请求方法、正文边界和跨任务连接复用。

Apple M4/macOS、`GOMAXPROCS=1`、500,000 次稳态 benchmark：

| Hot path | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| policy compile（配置变更路径） | 184～240 | 256 | 5 |
| policy cache hit（每任务） | 3.33～4.03 | 0 | 0 |
| literal target 解析、全校验与 pin | 163～176 | 128 | 3 |

策略热读取相对每次重新编译快约 46～72×并消除全部临时分配。域名任务的主要成本仍是一次真实 DNS lookup；安全 pin 不增加第二次解析。

正确性、安全和资源边界验证包括：

- 64 路相同配置并发读取同一 policy pointer，配置变更生成新快照，invalid/oversized 配置 fail closed；
- IPv4/IPv6 混合公私答案整体拒绝、空/超过 32 个答案拒绝、保留地址矩阵和显式私网边界；
- scripted DNS rebinding 只调用一次 resolver，真实本地 TCP 连接严格使用第一次固定地址；
- DNS context 取消和 resolver 错误脱敏，IPv6 URL/端口、IDNA、类型/端口、并发/频率限制；
- redirect 私网服务零请求且源站只访问一次；真实 TLS 握手验证 SNI、Host、TLS 1.2 和证书链；
- 专项测试 20 次、专项 race 10 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-303 Ping HTTP/TCP 受控连接复用与执行预算

HTTP Ping 不再每次分配新的 `http.Client`/`Transport` 并立即丢弃连接池。现在以 `(scheme, normalized hostname, port, pinned IP)` 作为完整安全 key：只有域名身份和 A-302 已验证地址都相同的任务才共享 client；同域名 DNS 切换到新 IP、同 IP 不同 TLS/Host 身份、HTTP/HTTPS 或端口变化都会使用物理隔离的 Transport。缓存最多 64 项、TTL 10 分钟、确定性 LRU，过期/淘汰/清空会关闭 idle connections。

每个 target pool 最多 4 条连接、2 条 idle connection，idle 90 秒；代理关闭，TLS SNI/验证、最低 TLS 1.2、固定 endpoint 与 redirect 禁止继续继承 A-302 的安全边界。同一已验证目标的连续 HEAD 探测在真实本地集成测试中只创建 1 条 TCP connection。TCP handshake 探测为保持语义仍会创建新连接，但复用并发安全的长期 `net.Dialer` 配置，不把已连接 socket 冒充新的握手延迟。

HTTP 首先发送 HEAD，因此正常 endpoint 不读取任何正文。仅当服务端明确返回 405/501 时，才在同一个 attempt context 内发送 `GET` + `Range: bytes=0-0` + `Accept-Encoding: identity`；响应正文立即关闭，不会因为服务端忽略 Range、返回 chunked 流或无限慢正文而下载无界数据。响应头继续限制为 64KB，只有 2xx 成功；3xx 不跟随，其他状态直接失败。

每个任务现在共享一个 10 秒 parent budget：DNS 阶段最多 3 秒，每次 ICMP/TCP/HTTP attempt 最多 3 秒，最多三次高延迟复测也不能把 timeout 串行累加到 parent deadline 之外。ICMP 改用 `RunWithContext`，TCP/HTTP dial 和读取同样从 parent 派生。

延迟语义固定为：ICMP 返回单包 RTT；TCP 返回到固定 IP 的 connect handshake；HTTP 返回从 HEAD 开始到最终响应头（若 fallback，则包含 HEAD 协商和 Range GET 首部）的时间。A-302 的 DNS 验证时间不进入协议 `value`，但进入 diagnostics DNS；新 TCP/HTTP connection 进入 diagnostics Dial，HTTP attempt 进入 diagnostics HTTP，已执行任务的完整解析/重试时间进入 diagnostics Ping。keep-alive 命中时 HTTP 数值自然不包含新连接成本。

Apple M4/macOS、`GOMAXPROCS=1`、500,000 次稳态 benchmark：

| Client path | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 每次构造 pinned HTTP client/Transport | 180～218 | 1,000 | 7 |
| bounded client cache hit | 70.1～73.5 | 0 | 0 |

纯对象路径快约 2.5～3.1×并消除 1KB/7 次分配；真实网络收益更大，因为 cache hit 同时避免重复 TCP/TLS handshake。安全 key 保证这种复用不能跨越 DNS pin 或 TLS identity。

正确性、安全和资源边界验证包括：

- 同一 pinned target 两次真实请求只建立一条连接；不同 IP/Host 使用不同 client；64 路并发冷读只创建一个 client；
- 容量 2 的 LRU、TTL 到期、显式清空和默认容量 64 边界；
- HEAD 成功路径、405→Range GET、Range/identity headers、2xx/404/3xx 状态；
- 慢无限正文在 250ms 内返回 TTFB 且服务端收到取消，64KB 响应头炸弹拒绝；
- HTTP parent context 取消、统一 retry budget、真实 TCP pin，以及 50ms DNS 不进入 TCP latency 的语义对照；
- 专项测试 20 次、专项 race 10 次、全量 unit/vet/race，以及 Linux amd64/arm64、Windows amd64、FreeBSD amd64 静态构建。

## A-304 netstatic 月累计索引与锁外持久化

本地月流量不再每秒遍历最近 31 天的全部历史桶。持久区和未落盘区分别维护按网卡、时间排序的 prefix index；任意包含首尾边界的时间区间都通过两次二分和前缀差值汇总，复杂度从 O(全部历史桶) 降为 O(网卡数 × log(单网卡桶数))。加载、强制替换、过期清理和 cache flush 都同步维护索引；counter 回绕/重置继续按零增量处理，不会制造月流量尖峰。

周期保存现在只在数据锁内合并 cache 并复制不可变 snapshot，JSON 编码、临时文件写入、文件 `fsync`、原子 `rename` 和目录 `fsync` 全部在锁外完成。临时文件位于目标文件同目录，失败自动清理且不覆盖旧目标；输入文件限制为 64MiB，损坏 JSON 会移动为唯一 `.bak` 后使用安全空状态恢复。磁盘阻塞期间汇总查询和采样不再被全局锁一起阻塞。

start/reload/stop 由递增 generation 管理。生命周期互斥保证并发 start 只创建一代 worker；reload 先分离、取消并 join 旧代，持久化配置后再启动新代；stop 同样等待 worker 完整退出后才写最终 snapshot。旧代在系统计数读取完成后还会再次核对 active generation，因此无法在 reload/stop 后污染新状态或交叉覆盖最终文件。

Apple M4/macOS、31 天、8 网卡、每 10 分钟一桶（共 35,712 桶）benchmark：

| Query | ns/op | B/op | allocs/op | 结果 |
|---|---:|---:|---:|---|
| 线性 reference | 21,454～21,495 | 400 | 2 | 每次扫描 35,712 桶 |
| prefix index | 296.6～300.4 | 400 | 2 | 只查询 8 个网卡，约快 71～72× |

正确性、持久化和并发验证包括：

- 月切换、包含边界、空区间、persisted + pending 合并与线性 reference 逐项一致；
- 首次计数、正常差分、counter reset 后恢复采样且不产生异常增量；
- snapshot round trip、乱序输入规范化、损坏文件备份恢复和 64MiB 读取边界；
- 失败写入保留目标内容并清理同目录临时文件；
- 阻塞磁盘写入期间查询仍可立即完成，证明 marshal/write 不持有数据锁；
- 8 路并发 start 只创建一个 generation，reload/stop 返回前旧 worker 已 join；
- 并发查询、快照读取和采样压力，以及专项 race、全量 unit/vet/race 回归。
