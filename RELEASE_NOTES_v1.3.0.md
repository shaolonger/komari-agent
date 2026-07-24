# Komari Agent v1.3.0

v1.3.0 完成了 Agent 采样、报告快照、连接状态机、DNS/HTTP/Ping、本地流量持久化、生命周期和发布供应链的系统性重构。目标是以更低、更稳定且有硬上界的资源成本运行，同时保持默认安全能力、旧服务端兼容和跨平台单文件交付。

## 性能与可靠性

- 新增 context 驱动、多频率、带抖动和 stale 状态的 sampler runtime；CPU/网络使用非阻塞差分采样，不再在报告路径 sleep。
- 内存/Swap 共享采样；静态主机信息、磁盘拓扑、socket/process 和 GPU 按变化频率缓存或懒初始化。
- 报告由类型化不可变 snapshot 生成；JSON v1 使用有界复用编码器，协议 v2 通过能力协商启用并可安全回退 v1。
- WebSocket reader/writer/heartbeat 归属同一 generation，使用指数退避和 full jitter；有界优先级队列合并过期 telemetry，但可靠控制响应会有限重试并在关闭时 drain。
- strict control、strict update 和可选 insecure telemetry 使用物理隔离的长期 Transport；自定义 DNS 支持 IPv4/IPv6 竞速和有界缓存。
- 公网 IP/基础信息带全局预算和缓存；Ping policy 预编译，DNS 验证后固定 IP，HTTP 探测复用有安全身份 key 的有界连接池。
- 本地月流量由线性扫描改为前缀索引；持久化在锁外编码和同步写入，使用同目录临时文件、原子 rename 和 generation 化生命周期。

## 安全与兼容

- 远程执行、终端和 Ping 继续默认关闭；必须显式启用。
- `ignore-unsafe-cert` 只影响物理隔离的遥测连接，并继续强制禁用远程控制和自动更新。
- Token 继续只通过 Header 发送；配置、Token、HTTP 响应、WebSocket 帧、队列、命令输出、并发和时限均有硬上界。
- 自动更新只接受稳定 SemVer、当前平台、HTTPS 和配套 SHA-256；受控 client、响应上限、重定向凭据隔离与原子回滚保持 fail closed。
- v1.3.0 Agent 连接 v1.2.13 Server 时自动使用 JSON v1；连接 v1.3.0 Server 时协商二进制遥测 v2。
- SIGINT/SIGTERM 走统一有界关闭路径，依次 drain 连接、队列、任务、sampler 和 netstatic 持久化。

## 构建与供应链

- 发布工具链锁定 Go 1.26.5；所有 GitHub Actions 固定到完整 40 位 commit SHA。
- 所有原生和容器二进制复用同一构建器，启用 `-trimpath -s -w`、空 build ID、稳定 Version/Commit 和版本化 PGO profile。
- 可复现性测试验证两次构建逐字节一致、无本机路径、PGO 元数据存在并可执行。
- Release 矩阵包含 Windows amd64/arm64/386、Linux amd64/arm64/386/arm、Darwin amd64/arm64、FreeBSD amd64/arm64/386/arm，共 13 个二进制。
- 每个平台均发布 binary、`.sha256`、keyless cosign `.sig` 与 `.pem`，上传前同时校验 SHA-256 和 GitHub OIDC certificate identity/issuer。

## 验收摘要

- 全部 unit、完整 race、vet、全包 benchmark、安全回归、可复现构建和 13 平台 release matrix 通过。
- 新旧服务端四象限兼容矩阵通过，并通过日志明确观察到旧组合/回退使用 v1、新组合使用 v2。
- 服务端 2,000 连接硬风暴、10,000 连接抖动恢复和约 2 分钟长稳回放均无丢报；Agent 可在真实连接失败循环及正常连接后优雅退出。
- 发布工作流、供应链策略、TLS 例外扫描、Token 查询串扫描和安装命令脱敏门禁通过。

完整设计、逐项实现和量化结果见 [`PERFORMANCE_REFACTOR_PLAN.md`](PERFORMANCE_REFACTOR_PLAN.md)、[`PERFORMANCE_REFACTOR_TODOLIST.md`](PERFORMANCE_REFACTOR_TODOLIST.md) 与 [`PERFORMANCE_RESULTS.md`](PERFORMANCE_RESULTS.md)。
