# komari-agent

支持使用环境变量 / JSON 配置文件来传入 agent 参数。

## 安全建议

- 不要在命令行直接传入 `--token` 或 `-t`，这会把 token 暴露给 shell history、进程列表和容器元数据。
- 推荐通过只读挂载的 JSON 配置文件传入敏感参数，并确保配置文件仅对 owner 可读。

## 安全部署与最小权限运行

- 优先使用 `--config` 或 `--token-file` 载入认证材料，不要把 token 放进命令行参数、shell history、systemd/OpenRC/procd/launchd/Windows 服务定义、Docker 启动参数或日志。
- 仅在安装阶段使用管理员或 root 权限；安装完成后，应以专用服务账户运行 agent，并确保配置目录、`auto-discovery.json`、JSON 配置文件只对 owner 或服务账户可读写。
- 远程终端、远程命令执行和 ping 探测默认禁用；优先按需分别启用 `--enable-terminal`、`--enable-remote-exec`、`--enable-ping`，只有确实需要完整远控面时才使用 `--enable-remote-control`。
- 如果显式开启远程终端，默认仍会施加每台 agent 最多 1 个终端会话、300 秒空闲超时、1800 秒最大会话时长；如确有需要，再通过 `--max-terminal-sessions`、`--terminal-idle-timeout`、`--terminal-max-duration` 调整。
- 如果显式开启 ping 探测，默认只允许 `tcp,http` 两类探测、只允许 `80,443` 端口、默认拒绝私有/环回/链路本地等敏感地址，并且会施加 `--max-concurrent-pings` 与 `--ping-min-interval-millis` 限制；只有在业务明确需要时才通过对应参数放宽。
- agent 还会对控制请求施加基础速率限制，默认 10 秒窗口内最多接受 10 个控制请求；如确有需要，可用 `--max-control-requests` 和 `--control-request-window` 调整。
- 如果需要保留任务命令审计，使用显式开关 `--audit-task-commands`，并假定日志只用于受控审计面，因为命令文本会经过脱敏后落日志。
- 如环境不允许自动更新，使用 `--disable-auto-update`，改为人工审批并配合下面的离线校验流程执行升级。
- 容器部署时仅挂载只读配置文件和必要运行目录，不要把宿主机上不相关的敏感路径暴露给容器。

## 认证材料处理规则

- token 不应出现在命令行、历史记录、服务配置、Docker `run` 参数、CI 日志、代理日志、排障截图或工单粘贴内容中。
- 若必须把 endpoint、Cloudflare Access 或 auto-discovery 等参数写入配置文件，仍应通过权限受控的 JSON 配置文件管理，而不是散落到多种服务参数中。
- 生产环境应建立 token 主动轮换和失效流程，避免长期复用同一静态凭据。

## TLS 信任模型

- agent 默认依赖标准 TLS 证书校验；生产环境不应使用 `--ignore-unsafe-cert`。
- 私有 CA 场景应把 CA 证书导入操作系统、容器镜像或运行时信任库，而不是关闭校验。
- 当前仓库尚未实现证书固定（pinning）；如果你需要更强约束，应优先通过受控反向代理、内部 PKI 或系统信任链管理来实现。
- 自动更新链路已经与 `--ignore-unsafe-cert` 解耦，即使开启该开关，自更新也会继续要求证书校验和 release 完整性校验。

## 安装供应链安全

- `install.sh` 与 `install.ps1` 会在替换本地 agent 之前校验 GitHub Release 提供的 `.sha256` 资产；校验失败时会拒绝安装。
- `install.ps1` 在下载 `nssm-2.24.zip` 后会校验官方发布页公开的摘要；校验失败时不会继续解压或注册服务。
- `--install-ghproxy` 仅允许用于组织自管、可信、HTTPS 的代理或镜像，并且必须同时显式传入 `--install-ghproxy-trusted`。
- 不要使用未知第三方公共代理作为安装源；代理必须原样转发二进制和对应 `.sha256` 资产，不能重打包、二次压缩或篡改内容。

示例：

```sh
./install.sh \
	--install-ghproxy https://mirror.example.com/github-release \
	--install-ghproxy-trusted \
	--install-version v1.2.0
```

```powershell
.\install.ps1 `
	--install-ghproxy https://mirror.example.com/github-release `
	--install-ghproxy-trusted `
	--install-version v1.2.0
```

## 离线安装校验流程

1. 在可信联网主机下载目标平台的二进制和同名 `.sha256` 文件，例如 `komari-agent-linux-amd64` 与 `komari-agent-linux-amd64.sha256`。
2. 在联网主机先完成哈希校验，再把已验证的文件传到离线主机。

Linux / FreeBSD:

```sh
sha256sum -c komari-agent-linux-amd64.sha256
```

macOS:

```sh
expected="$(awk '{print $1}' komari-agent-darwin-arm64.sha256)"
actual="$(shasum -a 256 komari-agent-darwin-arm64 | awk '{print $1}')"
test "$expected" = "$actual"
```

Windows PowerShell:

```powershell
$expected = (Get-Content .\komari-agent-windows-amd64.exe.sha256 | Select-Object -First 1).Split()[0].ToLower()
$actual = (Get-FileHash .\komari-agent-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
if ($actual -ne $expected) { throw 'Checksum mismatch' }
```

3. Windows 如需 `nssm.exe`，应在可信联网主机单独下载 `https://nssm.cc/release/nssm-2.24.zip`，并校验发布页公开的摘要 `be7b3577c6e3a280e5106a9e9db5b3775931cefc` 后，再把解压得到的 `win32/nssm.exe` 传到目标主机或预先放入 `PATH`。
4. 离线主机只执行已校验的二进制和 `nssm.exe`，并通过 JSON 配置文件提供 token 等敏感参数。

## 供应链验收

- `scripts/verify-install-sh-integrity.sh` 会验证 `install.sh` 的哈希校验和可信代理约束在篡改样本上 fail closed。
- `scripts/verify-install-ps1-integrity.ps1` 会验证 `install.ps1` 的哈希校验和可信代理约束在篡改样本上 fail closed。
- `scripts/verify-supply-chain-stage.ps1` 会串联 Release 资产检查、`go test ./update` 和安装脚本验收；在 live release 尚未补齐 `.sha256` 资产前，可临时使用 `-SkipLiveReleaseCheck` 只运行仓库内验证。

## Token 泄漏应急处置

1. 立即在服务端吊销或轮换泄漏 token，并确认旧 token 失效。
2. 回收所有可能包含旧 token 的介质：命令行历史、服务定义、容器编排文件、代理日志、CI 日志、排障截图与临时脚本。
3. 检查相关节点是否启用了 `--ignore-unsafe-cert`、`--enable-remote-control`、`--enable-terminal`、`--enable-remote-exec` 或 `--enable-ping`；如有，优先下线不必要的远控能力并恢复证书校验，同时复核是否错误放宽了 ping 目标范围或速率限制。
4. 使用受控 JSON 配置文件或 `--token-file` 重新部署，随后执行一次连接验证和最小权限复核。
5. 如果泄漏范围不明，追加审计代理/网关访问日志与任务执行日志，确认是否出现异常终端、任务下发或更新行为。

## Docker 示例

示例配置文件 `komari-agent.json`:

```json
{
	"endpoint": "https://example.com",
	"token": "replace-with-real-token"
}
```

推荐启动方式：

```sh
docker run --rm \
	-v "$PWD/komari-agent.json:/run/komari-agent/config.json:ro" \
	komari-agent --config /run/komari-agent/config.json
```

可用参数详见 `cmd/flags/flag.go` 及 `cmd/root.go`。