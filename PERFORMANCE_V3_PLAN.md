# Komari Agent Performance V3：无丢失、有界恢复与安全降级

状态：实现完成，目标版本 v1.4.1

V3 收口 Agent 的持久发送语义。目标不是通过丢弃旧数据换取低资源，而是在固定
内存与磁盘预算内保持连续序列、明确背压、断线恢复和协议兼容。

## 1. 可靠性不变量

- telemetry v3 frame 在进入网络队列前必须完整写入权限 `0600` 的本地 spool。
- 只有已认证服务端返回 durable ACK 后才能删除 frame。
- spool 达到 4096 frame 或 4 MiB 上限时返回背压错误，不删除未确认头部。
- sequence 在 compact、全量 ACK、进程重启和服务端高 checkpoint 后仍严格单调。
- 任意损坏记录隔离失败关闭；仅进程崩溃造成的尾部半写可截断到最后完整记录。
- spool 不包含 Token；v3 不可用时只降级到既有 v2/v1，不放宽 TLS、认证或探测权限。

## 2. Crash-safe spool

日志格式增加 high-water record。compact 即使没有 pending frame 也写入最高已发
sequence，避免重启后从 1 复用序列。ACK 可接受服务端更高的 authoritative
checkpoint，用于本地 spool 被人工删除/隔离后的恢复。

每条 append 先构造连续 record，再执行单次 write 和 fsync，检查 short write。
启动扫描记录最后一个完整 offset：完整前缀后的半 header/半 payload 被安全截断；
magic、边界、CRC 或中间记录错误仍隔离整个文件，防止把不可信字节当作遥测发送。

满载策略由“淘汰最旧 frame”改为 `ErrTelemetrySpoolFull`。旧策略会制造服务端永远
无法补齐的序列空洞；新策略保留连续可恢复前缀，让连接退避并等待 durable ACK。

## 3. 两阶段聚合提交

聚合器新增 Prepare/Commit 两阶段：先生成不可变 frame 但不清空窗口，spool 写入
与 fsync 成功后才 Commit 并推进 sequence。如果磁盘写失败或 spool 满，聚合样本
和 sequence 均留在内存，下一次连接可以继续提交，而不是静默丢失最新窗口。

同一互斥锁覆盖 sample、prepare、spool append、commit 和 next sequence 更新，
不存在 ACK/flush 并发导致的序列复用。

## 4. ACK、NACK 与队列对账

收到 telemetry 或 Ping ACK 后，Agent 先持久化 ACK，再推进本地 next，移除网络
队列中旧的可靠帧并从 spool 重建仍 pending 的有序队列。服务端 NACK 会触发同样
的 spool 对账，重放完整连续前缀，而不是等待下一次进程重启。

Ping batch 继续按固定 32 条上限发送；结果在 ACK 前持久保存。telemetry 与 Ping
队列使用不同 frame 分类，对账不会误删控制消息。

## 5. 安全兼容降级

Agent 仅在 WebSocket 实际协商到 v3 后延迟打开 spool。这样旧服务器或明确协商
v2/v1 的环境无需创建 v3 文件。若 v3 已协商但 spool 因权限、只读文件系统或
路径错误无法打开，Agent 关闭该会话并用只声明 v2/v1 的 dialer 重连；服务进程
继续运行并上报，不进入 systemd 重启风暴。

该回退不会伪装 v3 成功，也不会在没有 durable spool 时发送 v3 frame。

## 6. 资源与验收

spool、可靠队列、Ping batch、重连退避、goroutine 和 frame 大小全部有硬上限。
发布门禁覆盖 unit、race、vet、fuzz、长断线/满 spool、截断恢复、ACK/NACK、
v3 spool 故障降级、受限资源 soak，以及 Linux/Windows/Darwin/FreeBSD 构建。
