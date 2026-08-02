# Komari Agent v1.4.2

v1.4.2 是 telemetry v3 批量掉线和资源风暴热修复。它修复普通 durable ACK 被
错误解释为重放指令的问题，并让重复 ACK 成为零磁盘 I/O 的幂等操作。

## 根因与修复

- 普通 `telemetry_ack` 只推进 durable spool 水位和下一序列，不再删除并重建整个
  telemetry 发送队列；待持久化帧继续安全保留，但不会被 ACK 反复发送。
- 相同或过期 ACK 不再追加 spool record、不再 `fsync`、不再累计 compact 阈值，
  消除 ACK 风暴引起的 CPU、磁盘 I/O、网络和服务端写队列放大。
- compact 同时保存 sequence high-water 与 acknowledged-through，Agent 重启后仍能
  识别旧 ACK，且不会复用序列或重新制造 I/O。
- 只有 `telemetry_nack` 触发重放，并且只重放 `expected` 起始的 durable suffix；
  WebSocket 代际结束会取消重放，长离线积压不会卡住 worker 回收。

## 鉴权失败控制

- `uploadBasicInfo` 收到 401/403 后不再发送第二个旧版兼容请求。
- WebSocket 401/403 不再耗尽普通重试后退出 Agent 生命周期；改为一分钟固定冷却，
  日志明确提示检查 Token 和反向代理 `Authorization` 转发。
- HTTP 错误不回显服务端响应体或 Token；Bearer-only、TLS、SSRF 和远程能力默认
  禁用等安全策略保持不变。

## 验收

新增 partial/stale/duplicate ACK 闭环、零 I/O、compact 重启、NACK suffix/取消、
401 单请求/冷却和响应体不泄漏回归。全仓 unit、race、vet、v2/v3 fuzz、32 MiB
受限资源 72 小时等效 soak、跨仓契约及 Linux/ARM64/Windows/FreeBSD 构建通过。
