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

