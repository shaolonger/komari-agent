# Komari Agent v1.4.0

v1.4.0 与 Komari Server v1.4.0 共同完成低资源、高可靠遥测与 Ping 数据面的升级，同时保留旧服务端兼容和全部默认安全边界。

## 遥测 v3

- 新协议携带单调序列、字段位图、最新/最小/最大/总和/计数与周期检查点；降低发送频率时仍保留尖峰和累计流量。
- 严格限制 frame、字符串、GPU、计数和数值范围；未知版本、flag、schema、尾随数据和非有限值全部拒绝。
- 能力协商优先 v3，随后安全回退 v2/v1；服务端 ACK 后才推进持久序列。

## 有界断线恢复

- 新增权限 `0600` 的无 Token 本地 spool，按大小、年龄和条目数硬限制；损坏或过期数据隔离，不会无限重试。
- ACK 跟踪、重放、合并和物理 compact 均有上界；长时间断网不再因每帧重写 spool 造成 CPU/磁盘放大。
- 连接、队列、goroutine、文件和退出 drain 继续受限，旧的凭据隔离与指数退避保持不变。

## Ping 租约与批处理

- 服务端下发带 revision、phase 和 expiry 的 Ping 计划；断线或租约过期立即停止计划。
- Ping 结果按序列批量发送并在 ACK 前保留，可安全处理重复、乱序、丢包和重连。
- 所有探测仍通过 capability、类型、端口、DNS 固定、私网地址、并发、间隔、超时和响应上限检查；远程能力仍默认关闭。

## 验收与平台

- Server/Agent v1/v2/v3 交叉兼容矩阵、完整 unit/race/vet/fuzz、安全和网络故障门禁通过。
- Linux、Windows、Darwin、FreeBSD 目标构建通过；长断线 spool、72h 网络/堆等效 fixture 保持有界。
- Release 继续发布可复现 PGO 二进制、SHA-256 与 keyless cosign 签名材料。
