# Komari Agent v1.4.2 Stability Hotfix Todo

## 根因与发送闭环

- [x] **A-HF-001** 复现 partial durable ACK 导致 ACK/replay 无限反馈
- [x] **A-HF-002** 普通 ACK 禁止重建或重放 telemetry 队列
- [x] **A-HF-003** duplicate/stale ACK 实现零写入幂等
- [x] **A-HF-004** compact 持久化 acknowledged-through 水位
- [x] **A-HF-005** NACK 仅重放 expected suffix 且受连接代际取消

## 鉴权与兼容

- [x] **A-HF-101** 401/403 禁止 BasicInfo 双请求
- [x] **A-HF-102** WebSocket 鉴权失败使用固定冷却且不退出生命周期
- [x] **A-HF-103** 错误日志不包含 Token 或服务端响应体
- [x] **A-HF-104** 保持 v1/v2 fallback、TLS 与能力安全边界

## 验收与发布

- [x] **A-HF-201** feedback-loop、spool、NACK 与鉴权定向回归
- [x] **A-HF-202** 全仓 unit、race、vet、fuzz 和 32 MiB soak
- [x] **A-HF-203** Linux/ARM64/Windows/FreeBSD 交叉构建
- [x] **A-HF-204** 与 Komari v1.4.3 的共享 schema 和跨仓契约门禁
- [x] **A-HF-205** 提交、推送、v1.4.2 tag、GitHub Release 与资产验收
