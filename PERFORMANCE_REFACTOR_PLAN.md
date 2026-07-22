# Komari Agent 极致性能重构设计

状态：执行中  
基线：`2ac1fc8`（`v1.2.8` 后续终端修复）  
配套清单：[`PERFORMANCE_REFACTOR_TODOLIST.md`](PERFORMANCE_REFACTOR_TODOLIST.md)
性能基线：[`PERFORMANCE_BASELINE.md`](PERFORMANCE_BASELINE.md)
重构结果：[`PERFORMANCE_RESULTS.md`](PERFORMANCE_RESULTS.md)

## 1. 文档目标

本文定义 Komari Agent 从串行阻塞式采集器重构为低开销、多频率、快照化、可靠连接的跨平台监控 Agent 的完整方案。

目标不是简单减少几次分配，而是保证：

- 配置为 1 秒时能够稳定生成和发送 1 秒级快照；
- 报告生成路径不执行 sleep、外部命令、全量 socket/process/disk 枚举；
- 高连接数、复杂挂载、GPU 主机和网络异常下资源使用仍然有界；
- 重连、DNS、TLS、自动更新、Ping、远程执行和终端的安全边界不回退；
- 继续支持 Linux、Windows、macOS、FreeBSD 和轻量静态构建；
- 具备 benchmark、race、故障注入、跨平台构建和持续性能回归。

## 2. 安全与兼容硬约束

### 2.1 控制能力

- 远程执行、终端、Ping 继续显式 opt-in；
- `IgnoreUnsafeCert` 启用时继续禁用远程控制和自动更新；
- 控制消息继续受独立限流、并发、时长和输出大小限制；
- 性能队列不得丢弃任务结果、安全错误或控制响应；
- Token 继续使用 Header，禁止写入 URL、日志和命令行示例。

### 2.2 TLS、DNS 与更新

- 控制面和更新默认严格验证 TLS；
- 不安全 TLS Transport 与安全 Transport 永不共享；
- 自动更新继续校验签名/摘要并 fail closed；
- 不通过修改全局 `http.DefaultClient` 或 `DefaultTransport` 切换安全策略；
- DNS 优化必须保留自定义 DNS 能力和 IPv4/IPv6 可用性；
- Ping 解析和连接必须防止 DNS rebinding、私网绕过和重定向绕过。

### 2.3 资源边界

- 报告、控制消息、响应体、队列、goroutine、外部命令输出都有硬上限；
- 采样超时不会阻塞其他采样器或连接状态机；
- shutdown 在固定预算内停止并 flush，不用 `os.Exit` 跳过 defer；
- 本地流量数据原子持久化，不能用关闭 fsync/直接覆盖换性能。

### 2.4 协议兼容

- JSON v1 保持支持；
- 二进制协议 v2 通过能力握手协商；
- 服务端不支持 v2 时自动使用 v1，而不是关闭 TLS/认证；
- 报告字段含义和 counter reset 语义保持兼容。

## 3. 当前性能模型

### 3.1 串行报告生成

当前 `GenerateReport` 依次调用 CPU、内存、Swap、负载、磁盘、网络、socket、uptime、process 和 GPU。

其中：

- CPU 使用率调用 `cpu.Percent(1s)`，每次还读取静态 CPU 信息；
- 网络速度读取两次 counters，中间主动 sleep 1 秒；
- RAM 与 Swap 重复读取内存信息；
- 每份报告重新枚举磁盘分区和每个挂载点；
- 每份报告分别物化全部 TCP 和 UDP 连接；
- macOS/FreeBSD 的 process 统计可能每次启动 `ps`；
- GPU 可能每次启动 `nvidia-smi`/`rocm-smi` 并解析完整输出；
- 报告通过嵌套 `map[string]interface{}` 构造和反射 JSON 编码。

因此默认 1 秒配置无法代表真实发送周期；高 socket 数、慢挂载或 GPU 命令会进一步放大抖动。

### 3.2 连接与重连

连接只在 data ticker 到达时建立。固定重试循环阻塞采样 select；读协程退出不能通知主循环；重连无指数退避和抖动；写入没有统一 deadline；报告采样、重连和心跳耦合在同一循环。

### 3.3 HTTP、DNS 和 IP 探测

- 多条路径每次新建 `http.Client` 和 `Transport`，损失 keep-alive；
- 自定义 DialContext 解析全部 IP 后顺序拨号，没有 Happy Eyeballs；
- 基础信息上传和公网 IP 服务存在无界响应读取；
- 公网 IP 依次尝试多个服务，每个可耗时 15 秒；
- 自动更新临时替换全局 HTTP client；
- `IgnoreUnsafeCert` 修改全局 default transport。

### 3.4 Ping 和本地流量

Ping 允许性检查解析一次 DNS，实际 TCP/HTTP Ping 再解析一次；只检查第一个地址，并允许 HTTP 默认重定向。配置中的类型和端口每个任务重复解析。

本地流量范围查询每份报告扫描长期桶；持久化在全局锁内序列化完整 JSON 并写盘。

## 4. 目标架构

```text
                 +---------------- 静态信息（启动/事件刷新）
                 |
系统源 -> 多频率采样器 -> typed sample -> 原子 Snapshot Store
                 |                         |
                 + CPU/网络差分            v
                 + 内存/负载 1s      typed Report Encoder
                 + socket/process 5-10s    |
                 + disk 30-60s             v
                 + GPU 2-5s          有界优先级发送队列
                                             |
                                             v
                               Context 驱动 WebSocket 状态机
                                             |
                              +--------------+---------------+
                              |                              |
                         遥测/心跳                     控制消息 worker
```

### 4.1 Sampler Runtime

定义统一 sampler 接口：

```go
type Sampler[T any] interface {
    Sample(context.Context, SampleInput) (T, error)
}
```

Runtime 为每类指标配置：

- interval；
- timeout；
- stale-after；
- jitter；
- error backoff；
- 上一次 counters/state。

采样结果写入单一类型化 Snapshot。读者只获取不可变快照，不持有内部可变切片或 map。

### 4.2 采样频率

默认建议：

| 数据 | 周期 | 策略 |
|---|---:|---|
| CPU usage、网络 counters、内存、负载 | 1s | 非阻塞差分/单次读取 |
| uptime | 5s | 低成本读取 |
| process/socket count | 5～10s | 平台优化实现 |
| disk usage | 30～60s | 缓存挂载拓扑 |
| GPU 动态指标 | 2～5s | API/targeted query |
| CPU 型号、核心、OS、kernel、虚拟化 | 启动/事件 | 静态缓存 |
| 公网 IP | 数小时/网络变化 | 全局预算、缓存 |

报告 interval 与 sampler interval 解耦。慢采样器保留上次值并标记 stale，不能阻塞报告发送。

### 4.3 CPU、网络、内存和 socket

- CPU 使用相邻 times 计算差分，禁止 `Percent(1s)`；
- 网络使用相邻 IOCounters 和真实 elapsed 计算 bytes/s；
- RAM/Swap 共用一次平台内存快照；
- socket 在 Linux 优先读取 `/proc/net/{tcp,tcp6,udp,udp6}` 或 inet_diag；
- process 在 Linux 避免不必要对象创建，其他平台复用原生句柄或降低频率；
- counter wrap/reset 有显式处理；
- include/exclude NIC/mount 配置启动时编译。

### 4.4 GPU

- 只有 EnableGPU 时懒探测 vendor；
- 静态型号/显存总量缓存；
- 优先 NVML/ROCm API；不可用时用 targeted query；
- 外部命令使用 context、输出上限和低频 backoff；
- 无 GPU、命令失败和空结果不得生成 NaN 或阻塞其他报告。

### 4.5 Report Encoder 与协议

- 使用稳定的 typed struct 替代嵌套 interface map；
- JSON v1 使用复用缓冲区并保持字段兼容；
- v2 使用有 schema 和最大帧长的二进制编码；
- 禁止在 snapshot 中直接存 Token/控制命令等敏感数据；
- telemetry 队列过载时只合并为最新快照；任务结果和控制响应使用独立可靠队列。

### 4.6 WebSocket 状态机

状态：Disconnected -> Connecting -> Connected -> Backoff -> Connecting。

- 启动立即连接；
- reader、writer、heartbeat 和 telemetry producer 由同一 context/errgroup 管理；
- reader 退出立即取消连接 generation；
- 指数退避 + full jitter；
- write deadline、read limit、pong deadline；
- 新连接替换旧连接时 generation 防止旧 goroutine 误关闭新连接；
- shutdown 停止接收新控制任务、等待有界 drain、关闭连接。

### 4.7 HTTP 与 DNS

- 构建少量长生命周期客户端：strict control、strict update、可选 insecure telemetry；
- 每个请求使用 context deadline，而不是每次新建 Transport；
- 明确 MaxIdleConns、MaxIdleConnsPerHost、IdleConnTimeout；
- IPv4/IPv6 使用 Happy Eyeballs；
- 自定义 DNS 解析遵守请求 context 和缓存 TTL；
- 所有响应体有最大字节数；
- 自动更新通过注入 client，不修改全局对象。

### 4.8 Ping

- 启动时把 allowed types/ports 编译为不可变 set；
- 解析目标的全部地址，全部执行私网/环回/link-local/multicast 检查；
- 从验证到 dial 使用同一固定 `netip.Addr`；
- HTTPS 保留 hostname/SNI 和证书验证；
- 默认禁止 redirect；允许时每跳重新验证；
- Ping 专用连接复用不能跨目标权限或 TLS 策略；
- 保持现有并发、频率和 capability 限制。

### 4.9 本地流量统计

- 内存中维护当前桶、日/月累计和可选 prefix index；
- 月流量读取为 O(网卡数) 或 O(log buckets)，不每秒扫描 31 天；
- 保存时锁内只复制版本化 snapshot，锁外编码；
- 使用原子临时文件、sync、rename；
- 配置切换以 generation 管理 goroutine，旧 generation 必须退出。

## 5. 性能与可靠性目标

在固定跨平台测试机和数据集上：

- 非 GPU `BuildReport` p99 小于 50ms，且不存在主动 sleep；
- 1 秒报告周期 p99 漂移小于周期的 5%；
- 高连接数主机的 socket 采样不会每秒分配完整连接对象数组；
- 报告编码 allocations/op 相对基线降低至少 70%；
- 网络中断时 goroutine、队列和内存保持有界；
- 10,000 Agent 同时恢复连接时由 jitter 分散，不形成固定 5 秒波峰；
- 公网 IP 获取受单一总预算限制并缓存；
- 月流量读取复杂度不随 31 天桶数线性增加；
- shutdown 在预算内保存流量并退出；
- race、泄漏、丢任务结果、TLS 降级均为零。

绝对 CPU/内存目标按 OS 和硬件分别记录，不能用单一机器结果替代跨平台验证。

## 6. 可观测性

本地低开销统计包括：

- 各 sampler duration、error、timeout、stale age；
- report build/encode bytes 和 duration；
- telemetry/control 队列深度、合并、重试；
- WS 状态、generation、连接持续时间、退避；
- DNS lookup、dial、TLS、HTTP duration；
- Ping rejected reason、执行时间和并发；
- netstatic bucket、flush duration、文件大小；
- goroutine、heap 和 GC 摘要。

默认日志不得包含 Token、完整控制命令、敏感 URL query 或未脱敏响应体。

## 7. 测试策略

### 7.1 单元和 benchmark

- CPU/network 差分、counter reset、elapsed；
- sampler timeout/stale/jitter/backoff；
- Snapshot 不可变性和并发访问；
- JSON v1 golden、v2 cross-repo fixtures；
- 队列合并和可靠优先级；
- DNS address policy、redirect、SNI；
- netstatic prefix/month rotate 和持久化恢复；
- `go test -bench . -benchmem` + benchstat。

### 7.2 集成和故障注入

- 本地 WS server：断开、半开、慢读、超大控制帧；
- DNS 超时、IPv4/IPv6 单栈、坏证书、自定义 CA；
- HTTP 响应炸弹和慢 body；
- GPU 命令超时/大输出/无设备；
- 磁盘只读、文件损坏、rename 失败；
- shutdown 时正在采样、上传、执行控制任务。

### 7.3 跨平台和发布门禁

- Linux/Windows/macOS/FreeBSD build；
- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- 现有安全回归脚本；
- 网络集成测试在受控环境运行；
- benchmark 回退超过阈值失败；
- release 产物 checksum/signature 验证；
- 旧服务端、新服务端的 v1/v2 兼容矩阵。

## 8. 提交、回滚与发布

- 每个 Todo 独立提交，包含实现、测试、文档和清单勾选；
- sampler/runtime 通过 feature flag 或内部适配层渐进切换；
- 新旧采样器在测试中对照，出现平台异常可回退旧实现；
- 协议 v2 失败自动回到安全的 JSON v1；
- Release 使用新 SemVer，不覆盖现有 tag；
- 创建 Release 后等待构建、签名、容器和 release notes workflow 完成并核验资产。
