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
