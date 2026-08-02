# Komari Agent v1.4.1

v1.4.1 完成 telemetry v3/Ping 批处理的无丢失可靠性收口，同时保持固定资源预算、
旧服务端兼容和全部安全边界。

## Crash-safe durable spool

- compact 写入 sequence high-water，即使所有 frame 已 ACK，重启也不会复用序列。
- spool 满时明确背压，不再淘汰未确认头部并制造永久序列空洞。
- 崩溃造成的半写尾部恢复到最后完整记录；CRC、magic、边界或中间损坏继续隔离。
- 服务端 durable checkpoint 可修复本地 spool 被删除后的 sequence 基线。

## 无丢失聚合与快速恢复

- v3 聚合采用 Prepare/Commit；只有 frame 已写入并 fsync 到 spool 后才清空窗口、
  推进 sequence，磁盘失败和满载不会静默丢失最新样本。
- telemetry/Ping ACK 先持久化，再清理可靠队列并从 spool 重建 pending 顺序。
- NACK 立即触发连续 durable frame 重放，无需重启 Agent。

## 安全降级

- v3 spool 只在实际协商 v3 后打开；旧服务端不会创建无用恢复文件。
- v3 spool 无法打开时关闭该连接并以 v2/v1 能力重新连接，避免 Agent 启动失败，
  同时绝不在无 durable spool 时发送 v3。
- TLS、Token 隔离、Ping SSRF/端口/DNS/超时/并发限制与默认禁用远程能力不变。

## 验收

全仓 unit、race、vet、fuzz、长断线/满载/截断/ACK/NACK/降级故障矩阵、受限资源
soak 和 Linux/Windows/Darwin/FreeBSD 构建通过。
