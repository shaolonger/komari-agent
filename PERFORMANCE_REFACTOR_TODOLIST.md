# Komari Agent 极致性能重构 Todo

状态：执行中  
设计文档：[`PERFORMANCE_REFACTOR_PLAN.md`](PERFORMANCE_REFACTOR_PLAN.md)

## 执行规则

任务只有满足以下条件才可勾选：

1. 实现、测试、迁移和文档完整；
2. 专项测试、`go test ./...`、`go vet ./...` 通过；
3. 并发任务通过相关 `go test -race`；
4. 性能任务保留修改前后 benchmark；
5. 安全回归和 capability 默认值无回退；
6. 完成一个包含 Todo 勾选的独立 commit。

## Phase A0：基线与性能工程

- [x] **A-000 重构设计与可验收任务清单**
  - 交付：本设计文档和 Todo 清单。
  - 验证：Markdown 链接和任务 ID 完整性检查。
  - 提交：文档独立提交。

- [x] **A-001 修复 race 测试夹具并建立 race 门禁**
  - 将并发任务日志捕获改为同步 writer。
  - CI 增加 `go test -race ./...` 的受支持平台门禁。
  - 测试：重复运行 race suite，不得再由测试基础设施产生竞争。

- [x] **A-002 建立采样、编码、连接和本地流量性能基线**
  - benchmark CPU、网络、内存、磁盘、socket、process、GPU adapter、报告编码和 netstatic。
  - 增加可重复 fake `/proc`/系统源和 benchmark fixture。
  - 输出 allocations/op、bytes/op、duration 和采样 syscall/command 次数。
  - 基线结果：[`PERFORMANCE_BASELINE.md`](PERFORMANCE_BASELINE.md)。

- [x] **A-003 增加低开销本地性能计数与诊断模式**
  - sampler、report、queue、WS、DNS/HTTP、Ping、netstatic 指标。
  - 默认关闭详细诊断，日志和 profile 脱敏。
  - 测试：并发、开关、敏感字段和性能开销。

## Phase A1：Sampler Runtime 与报告快照

- [x] **A-101 Context、多频率、抖动和 stale 感知的 Sampler Runtime**
  - 统一 sampler 生命周期、timeout、interval、backoff 和 generation。
  - 慢 sampler 不阻塞报告或其他 sampler。
  - 测试：fake clock、取消、超时、stale、reload、goroutine 泄漏、race、benchmark。

- [x] **A-102 CPU 与网络非阻塞差分采样**
  - 删除 `cpu.Percent(1s)` 和网络 `Sleep(1s)`。
  - 使用真实 elapsed 和 counter reset/wrap 处理。
  - 测试：首次样本、正常差分、重置、时间漂移、NIC 过滤、对照测试、benchmark。

- [x] **A-103 内存/Swap 共享采样与静态主机信息缓存**
  - 一次读取生成 RAM/Swap。
  - CPU 型号、核心、OS、kernel、虚拟化只在启动/事件刷新。
  - 测试：Linux fixture、平台 fallback、缓存刷新、错误 stale、benchmark。

- [x] **A-104 磁盘拓扑缓存和低频容量采样**
  - 分区拓扑与容量读取解耦。
  - include mountpoints 预解析，处理 ZFS、重复设备和挂载变化。
  - 测试：复杂挂载 fixture、热插拔、错误挂载、刷新、benchmark。

- [x] **A-105 socket/process 平台优化与低频采样**
  - Linux 避免完整 gopsutil connection object 数组。
  - 其他平台降低子进程/句柄创建并缓存平台资源。
  - 测试：大量连接/进程 fixture、权限不足、平台 build、benchmark。

- [ ] **A-106 类型化不可变 Snapshot 与报告编码器**
  - 替换嵌套 `map[string]interface{}`。
  - report build 只读取快照，不触发系统调用。
  - JSON v1 golden 完全兼容，复用有界 buffer。
  - 测试：golden、并发不可变性、空/陈旧字段、race、benchmark。

- [ ] **A-107 Agent 协议 v2 与 v1 安全回退（Agent 部分）**
  - 有 schema、最大帧长的二进制遥测。
  - 通过服务端能力协商，失败回到 JSON v1。
  - 测试：跨仓库 fixture、版本降级、畸形/超限帧、字段一致性、benchmark。

## Phase A2：连接、HTTP 与 DNS

- [ ] **A-201 Context 驱动的 WebSocket 状态机**
  - 启动立即连接，reader/writer/heartbeat 同 generation。
  - 指数退避 + full jitter，读失败立即触发重连。
  - read limit、write deadline、pong deadline。
  - 测试：断线、半开、慢写、超限消息、替换连接、取消、race、泄漏检测。

- [ ] **A-202 有界优先级发送队列与可靠 drain**
  - telemetry 可合并最新，任务结果/控制响应可靠。
  - 队列、重试、关闭都有明确上限和指标。
  - 测试：过载、顺序、合并、可靠消息、shutdown、race、benchmark。

- [ ] **A-203 长生命周期、按安全策略隔离的 HTTP Transport**
  - strict control、strict update、可选 insecure telemetry 物理隔离。
  - 合理连接池和 per-request deadline。
  - 删除每请求 Transport 和全局默认对象修改。
  - 测试：连接复用、证书、IgnoreUnsafeCert capability、安全更新、race。

- [ ] **A-204 自定义 DNS、Happy Eyeballs 和有界缓存**
  - IPv4/IPv6 并发竞速，自定义 resolver 保持支持。
  - lookup 和 dial 服从总 context 预算。
  - 测试：v4/v6 单栈、首地址失败、超时、取消、DNS 变更、benchmark。

- [ ] **A-205 基础信息与公网 IP 缓存/全局预算**
  - 静态基本信息缓存，连接重试不重复昂贵采集。
  - IP 请求并行竞速、响应上限、正则预编译、小时级缓存。
  - 测试：全部服务失败、慢服务、响应炸弹、NIC 模式、缓存失效、隐私配置。

## Phase A3：GPU、Ping 与本地流量

- [ ] **A-301 GPU 懒初始化、targeted 采样和命令边界**
  - GPU 未启用时零子进程。
  - 静态/动态信息分频率，优先原生 API。
  - 外部命令 context、输出上限、错误 backoff；空 GPU 不产生 NaN。
  - 测试：无 GPU、NVIDIA/AMD fixture、超时、大输出、多卡、benchmark。

- [ ] **A-302 Ping 配置预编译与 DNS/IP 固定安全连接**
  - allowed types/ports immutable set。
  - 验证全部解析地址并固定通过验证的地址。
  - 保留 HTTPS SNI/校验，重定向禁止或逐跳验证。
  - 测试：DNS rebinding、混合公私地址、redirect 私网、IPv6、端口/并发/频率限制、安全回归。

- [ ] **A-303 Ping HTTP/TCP 受控连接复用与执行预算**
  - 避免每 Ping 新建 Transport。
  - HEAD/Range 或有界 GET，不下载无界 body。
  - DNS 时间和连接时间统计语义明确。
  - 测试：keep-alive、慢 body、状态码、TLS、取消、benchmark。

- [ ] **A-304 netstatic 月累计索引与锁外持久化**
  - 月流量查询 O(网卡数)/O(log n)。
  - 锁内 snapshot，锁外 marshal/write；sync + atomic rename。
  - generation 化 start/reload/stop。
  - 测试：月切换、counter reset、恢复、损坏文件、写失败、并发、race、benchmark。

## Phase A4：生命周期、构建与发布

- [ ] **A-401 配置参数验证和优雅关闭**
  - interval、timeout、retry、queue、并发和持久化参数统一验证。
  - signal context 驱动停止，不调用 `os.Exit` 跳过 defer。
  - 有界等待 sampler、队列、netstatic 和连接关闭。
  - 测试：非法配置、SIGTERM、超时任务、保存失败、退出码、race。

- [ ] **A-402 可复现构建、二进制瘦身和 PGO**
  - Go toolchain 与 go.mod/CI 对齐。
  - release 使用 `-trimpath -s -w`、稳定版本元数据和代表性 PGO。
  - 保持 checksum/signature/cosign 供应链门禁。
  - 测试：全平台 build、重复构建、PGO smoke、签名校验、安全回归。

- [ ] **A-403 全量跨平台、压力、安全回归和发布验收**
  - 完成单元、集成、race、vet、benchmark、连接风暴和长稳测试。
  - 完成新旧服务端、v1/v2 协议兼容矩阵。
  - 核对全部 Todo、提交、工作树和发布资产。
  - 推送分支，创建新 SemVer Release，等待全部 GitHub Actions 成功并验证资产。
