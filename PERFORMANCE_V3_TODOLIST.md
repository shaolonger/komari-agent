# Komari Agent Performance V3 Todo

状态：实现与测试完成，GitHub 发布步骤待执行。

## A3-0 持久序列

- [x] **A3-001** compact 后保存 sequence high-water，禁止重启复用
- [x] **A3-002** 服务端 authoritative ACK 安全推进本地 sequence
- [x] **A3-003** 完整前缀后的 crash-truncated 尾部恢复
- [x] **A3-004** 任意中间损坏继续 fail closed 并隔离
- [x] **A3-005** spool 满时背压而不是删除未确认头部

## A3-1 无丢失发送

- [x] **A3-101** 聚合 frame 使用 Prepare/Commit 两阶段提交
- [x] **A3-102** spool 写失败时保留聚合窗口与 next sequence
- [x] **A3-103** ACK 后对 telemetry 可靠队列与 spool 对账
- [x] **A3-104** NACK 后立即重放连续 durable frames
- [x] **A3-105** Ping ACK/NACK 同步 sequence 并对账独立队列

## A3-2 兼容与安全

- [x] **A3-201** 仅协商 v3 后延迟创建 spool
- [x] **A3-202** spool 不可用时明确重连并只协商 v2/v1
- [x] **A3-203** 保持 TLS、Token 隔离、SSRF 与默认禁用远程能力策略

## A3-3 验收与发布

- [x] **A3-301** unit、race、vet、fuzz 与可靠队列故障矩阵
- [x] **A3-302** 长断线、满 spool、截断、ACK/NACK 与受限资源 soak
- [x] **A3-303** Linux/Windows/Darwin/FreeBSD 交叉构建
- [x] **A3-304** 与 Komari v1.4.2 的 v1/v2/v3 交叉协议验收
- [ ] **A3-305** 版本、提交、推送、tag 与 GitHub Release
