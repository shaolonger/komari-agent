# komari-agent

支持使用环境变量 / JSON 配置文件来传入 agent 参数。

## 安全建议

- 不要在命令行直接传入 `--token` 或 `-t`，这会把 token 暴露给 shell history、进程列表和容器元数据。
- 推荐通过只读挂载的 JSON 配置文件传入敏感参数，并确保配置文件仅对 owner 可读。

## 安全部署与最小权限运行

- 优先使用 `--config` 或 `--token-file` 载入认证材料，不要把 token 放进命令行参数、shell history、systemd/OpenRC/procd/launchd/Windows 服务定义、Docker 启动参数或日志。
- 仅在安装阶段使用管理员或 root 权限；安装完成后，应以专用服务账户运行 agent，并确保配置目录、`auto-discovery.json`、JSON 配置文件只对 owner 或服务账户可读写。
- Linux / macOS 上如果通过 `bash <(curl ...)` 或 `./install.sh` 由普通用户启动系统级安装，`install.sh` 会自动暂存当前脚本并通过 `sudo` 续跑；若环境没有 `sudo`，再改为手工使用 root 执行。
- 远程终端、远程命令执行和 ping 探测默认禁用；优先按需分别启用 `--enable-terminal`、`--enable-remote-exec`、`--enable-ping`，只有确实需要完整远控面时才使用 `--enable-remote-control`。
- 如果显式开启远程终端，默认仍会施加每台 agent 最多 1 个终端会话、300 秒空闲超时、1800 秒最大会话时长；如确有需要，再通过 `--max-terminal-sessions`、`--terminal-idle-timeout`、`--terminal-max-duration` 调整。
- 如果显式开启 ping 探测，默认只允许 `tcp,http` 两类探测、只允许 `80,443` 端口、默认拒绝私有/环回/链路本地等敏感地址，并且会施加 `--max-concurrent-pings` 与 `--ping-min-interval-millis` 限制；只有在业务明确需要时才通过对应参数放宽。
- agent 还会对控制请求施加基础速率限制，默认 10 秒窗口内最多接受 10 个控制请求；如确有需要，可用 `--max-control-requests` 和 `--control-request-window` 调整。
- 如果需要保留任务命令审计，使用显式开关 `--audit-task-commands`，并假定日志只用于受控审计面，因为命令文本会经过脱敏后落日志。
- 如环境不允许自动更新，使用 `--disable-auto-update`，改为人工审批并配合下面的离线校验流程执行升级。
- 容器部署时仅挂载只读配置文件和必要运行目录，不要把宿主机上不相关的敏感路径暴露给容器。

## 当前 fork 的远控与延迟监测默认行为

- 当前 `shaolonger/komari-agent` fork 默认只开启基础监控上报；远程终端、远程命令执行和远程 ping 探测都需要显式开启。
- 当前 fork 的自更新与安装脚本都以 `shaolonger/komari-agent` 的 GitHub Releases 为准；如果你维护的是别的 fork，请同步调整发布仓库、安装脚本和自动更新目标。
- 如果你要在 Komari 面板里使用“延迟监测”或批量 TCP/HTTP Ping，请显式设置 `enable_ping=true` 或启动参数 `--enable-ping`。只有在你确实要开放完整远控面时，才建议使用 `--enable-remote-control`。
- `ignore_unsafe_cert` 仍然会禁用远程终端、远程命令执行和远程 ping；它适合作为临时测试手段，不应作为生产环境常态配置。
- `max_control_requests` / `control_request_window` 现在只用于远程终端和远程命令执行的控制面限流；ping 探测改为只受 `max_concurrent_pings`、`ping_min_interval_millis`、目标白名单与端口白名单约束，避免批量延迟监测被通用控制面限流误判为丢包。

## 延迟监测与批量 Ping 配置建议

如果同一台节点要同时执行多条相同 `interval` 的延迟监测任务，请至少确认以下几点：

- `enable_ping=true`
- `allowed_ping_types` 覆盖你实际要用的类型，默认仅 `tcp,http`
- `allowed_ping_tcp_ports` 覆盖你实际要探测的端口，默认仅 `80,443`
- `max_concurrent_pings` 大于等于同一轮可能同时下发到该节点的任务数
- `ping_min_interval_millis=0`，用于显式关闭每台 agent 的最小接收间隔限制

注意：在当前 fork 中，`ping_min_interval_millis=0` 现在表示“关闭最小接收间隔限制”；只有负值才会回退到安全默认值 `500ms`。这意味着当服务端在同一个调度 tick 内向同一台节点下发多条 Ping 任务时，只要并发数允许，这些任务就不会再因为默认的 `500ms` 最小间隔被直接拒绝。

推荐的 JSON 配置示例：

```json
{
	"endpoint": "https://monitor.example.com",
	"token": "replace-with-real-token",
	"enable_ping": true,
	"allowed_ping_types": "tcp,http",
	"allowed_ping_tcp_ports": "80,443",
	"max_concurrent_pings": 24,
	"ping_min_interval_millis": 0
}
```

如果你的延迟监测目标是私有地址、内网负载均衡器或环回 / 链路本地地址，还需要显式设置 `allow_private_ping_targets=true`。默认拒绝这些目标是有意的安全边界，而不是连接故障。

## 认证材料处理规则

- token 不应出现在命令行、历史记录、服务配置、Docker `run` 参数、CI 日志、代理日志、排障截图或工单粘贴内容中。
- 若必须把 endpoint、Cloudflare Access 或 auto-discovery 等参数写入配置文件，仍应通过权限受控的 JSON 配置文件管理，而不是散落到多种服务参数中。
- 生产环境应建立 token 主动轮换和失效流程，避免长期复用同一静态凭据。

## Header 认证迁移策略

- 当前 agent 与服务端都已经切换为 `Authorization: Bearer <token>`；控制面不再接受 `?token=` 查询串作为正式认证方式。
- 建议的升级顺序是先升级 Komari 服务端，再批量升级 agent；不要把新 agent 长时间指向旧服务端，也不要继续保留 query-token 的并行兼容期。
- 兼容策略采用 fail-closed：旧版仍使用 `?token=` 的 agent 在升级后会被服务端拒绝，而不是继续静默兼容。
- 升级后应复核反向代理、WAF、WebSocket 代理和 APM 采集配置，确认不再依赖 URL 查询串中的 token 字段。

## Token 生命周期治理

- 服务端现在支持按 client 维度查询 token 状态，并支持 `rotate`、`revoke`、`reissue` 三类生命周期操作；可选传入 `expires_in_hours` 为新签发 token 设置过期时间。
- 推荐流程是：日常按周期 `rotate`；发现泄漏或怀疑泄漏时立即 `revoke`；确认需要恢复 agent 接入时再 `reissue` 新 token。
- 对应的服务端管理接口为 `/api/admin/client/:uuid/token`、`/api/admin/client/:uuid/token/rotate`、`/api/admin/client/:uuid/token/revoke`、`/api/admin/client/:uuid/token/reissue`。
- token 一旦被 `revoke` 或达到 `expires_at`，服务端会在认证闸门直接拒绝该 token，agent 需要使用新 token 重新建立连接。

## TLS 信任模型

- agent 默认依赖标准 TLS 证书校验；生产环境不应使用 `--ignore-unsafe-cert`。
- 私有 CA 场景应把 CA 证书导入操作系统、容器镜像或运行时信任库，而不是关闭校验。
- 当前实现会在启用 `--ignore-unsafe-cert` 时自动禁用远程终端、远程命令执行、ping 探测和自动更新；如果你看到这个开关出现在生产配置里，应视为临时例外而不是常态配置。
- 当前仓库尚未实现证书固定（pinning）；如果你需要更强约束，应优先通过受控反向代理、内部 PKI 或系统信任链管理来实现。
- 自动更新链路已经与 `--ignore-unsafe-cert` 解耦，即使开启该开关，自更新也会继续要求证书校验和 release 完整性校验。

## 安装供应链安全

- `install.sh` 与 `install.ps1` 会在替换本地 agent 之前校验 GitHub Release 提供的 `.sha256` 资产；校验失败时会拒绝安装。
- Release 工作流还会为每个 `.sha256` 资产生成对应的 `*.sha256.sig` 与 `*.sha256.pem`，用于离线或人工复核时验证该校验文件确实由仓库的正式发布流程签发。
- `install.ps1` 在下载 `nssm-2.24.zip` 后会校验官方发布页公开的摘要；校验失败时不会继续解压或注册服务。
- `--install-ghproxy` 仅允许用于组织自管、可信、HTTPS 的代理或镜像，并且必须同时显式传入 `--install-ghproxy-trusted`。
- 不要使用未知第三方公共代理作为安装源；代理必须原样转发二进制和对应 `.sha256` 资产，不能重打包、二次压缩或篡改内容。
- 如果你直接使用 fork 仓库里的 `install.sh` / `install.ps1`，必须先在该 fork 的 GitHub Releases 发布对应二进制和 `.sha256` 资产；仅推送 `main` 分支代码而不发布 release 时，installer 会明确拒绝继续下载。

## Release 签名与保管模型

- 发布签名采用 GitHub Actions OIDC 的 keyless cosign 流程：每次 release 发布时临时生成签名密钥，并由 Sigstore/Fulcio 为该次工作流签发短期证书。
- 仓库、CI secret 和运维环境中不保管长期签名私钥；签名信任锚转移为 GitHub release workflow 身份、仓库权限和 tag/release 审批流程。
- 如需调整签名身份、仓库名或工作流路径，必须同步更新下方 `cosign verify-blob` 命令中的 `certificate-identity` 约束，并重新评审发布权限。

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
2. 如需校验该 `.sha256` 文件本身来自正式 release 流程，可额外下载同名 `*.sha256.sig` 与 `*.sha256.pem`，然后执行：

```sh
cosign verify-blob \
	--signature komari-agent-linux-amd64.sha256.sig \
	--certificate komari-agent-linux-amd64.sha256.pem \
	--certificate-identity-regexp 'https://github.com/shaolonger/komari-agent/.github/workflows/release.yml@refs/tags/.*' \
	--certificate-oidc-issuer https://token.actions.githubusercontent.com \
	komari-agent-linux-amd64.sha256
```

若你校验的是别的 fork 或上游仓库发布的资产，请把 `--certificate-identity-regexp` 中的仓库路径改成对应仓库。

3. 在联网主机完成签名校验和哈希校验后，再把已验证的文件传到离线主机。

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

4. Windows 如需 `nssm.exe`，应在可信联网主机单独下载 `https://nssm.cc/release/nssm-2.24.zip`，并校验发布页公开的摘要 `be7b3577c6e3a280e5106a9e9db5b3775931cefc` 后，再把解压得到的 `win32/nssm.exe` 传到目标主机或预先放入 `PATH`。
5. 离线主机只执行已校验的二进制和 `nssm.exe`，并通过 JSON 配置文件提供 token 等敏感参数。

## 供应链验收

- `scripts/verify-install-sh-integrity.sh` 会验证 `install.sh` 的哈希校验和可信代理约束在篡改样本上 fail closed。
- `scripts/verify-install-ps1-integrity.ps1` 会验证 `install.ps1` 的哈希校验和可信代理约束在篡改样本上 fail closed。
- `scripts/verify-supply-chain-stage.ps1` 会串联 Release 资产检查、`go test ./update` 和安装脚本验收；在 live release 尚未补齐 `.sha256` / `.sha256.sig` / `.sha256.pem` 资产前，可临时使用 `-SkipLiveReleaseCheck` 只运行仓库内验证。

## Token 泄漏应急处置

1. 立即在服务端吊销或轮换泄漏 token，并确认旧 token 失效。
2. 回收所有可能包含旧 token 的介质：命令行历史、服务定义、容器编排文件、代理日志、CI 日志、排障截图与临时脚本。
3. 检查相关节点是否启用了 `--ignore-unsafe-cert`、`--enable-remote-control`、`--enable-terminal`、`--enable-remote-exec` 或 `--enable-ping`；如有，优先下线不必要的远控能力并恢复证书校验，同时复核是否错误放宽了 ping 目标范围或速率限制。
4. 使用受控 JSON 配置文件或 `--token-file` 重新部署，随后执行一次连接验证和最小权限复核。
5. 如果泄漏范围不明，追加审计代理/网关访问日志与任务执行日志，确认是否出现异常终端、任务下发或更新行为。

## 旧部署迁移到安全配置模式

通用迁移原则：

1. 先记录现有部署中除 token 之外的参数，例如 `--endpoint`、网卡过滤、磁盘过滤、自定义 DNS、显式启用的远控能力。
2. 把 token 从服务参数中移出，改为受限权限的 JSON 配置文件或 `--token-file`；服务定义里只保留 `--config` 或 `--token-file` 路径。
3. 迁移前先备份原有服务定义和配置目录；迁移后先确认 agent 能重新上线，再删除旧的明文 token 参数或遗留文件。
4. 如果旧部署依赖远程任务、终端或 ping，迁移时必须显式补上 `--enable-remote-exec`、`--enable-terminal`、`--enable-ping` 或 `--enable-remote-control`，不要假设默认仍然开启。
5. 重启后检查进程列表、服务定义和安装日志，确认只出现 `--config` / `--token-file`，不再出现 `--token` 明文值。

Linux / macOS / BSD：

1. 如果使用 `install.sh`，可直接带原有非敏感参数重新执行安装；当你传入 `--token` 且未显式传 `--config` 时，脚本会自动生成受保护的 `komari-agent.json`，并让 systemd/OpenRC/procd/launchd 只引用 `--config <path>`。
2. 如果手工维护服务定义，把 token 写入仅 owner 或服务账户可读的配置文件，然后把 `ExecStart`、`command_args` 或 `ProgramArguments` 改成类似 `agent --config /etc/komari/komari-agent.json`。
3. 对配置文件执行最小权限控制，例如 `chmod 600`，并把 owner 设为 root 或专用服务账户；随后重启服务并确认 agent 回连正常。

Windows：

1. 如果使用 `install.ps1`，可带原有非敏感参数重新执行安装；当你传入 `--token` 且未显式传 `--config` 时，脚本会在安装目录生成受保护的 `komari-agent.json`，并让 NSSM 服务参数只保留 `--config "...\komari-agent.json"`。
2. 如果手工维护服务，先停止服务，再把 token 写入受限 ACL 的配置文件，并把 NSSM 或 `sc.exe` 的参数改成只引用 `--config` 或 `--token-file`。
3. 重启后用服务管理器或 `nssm get <service> AppParameters` 复核参数，确认安装日志和服务参数中不再有明文 token。

容器：

1. 把 token 放进只读挂载的 secret/config 文件，不要再通过 `docker run ... --token ...` 或编排平台的明文启动参数传递。
2. 推荐把完整 JSON 配置文件挂载到容器内，例如 `/run/komari-agent/config.json`，然后仅传 `--config /run/komari-agent/config.json`。
3. 在 Kubernetes 等编排环境中，优先通过 Secret 卷挂载文件，并在滚动发布后检查 Pod spec、事件日志和 sidecar/代理日志中是否还残留旧 token。

建议的迁移验收：

1. agent 重启后能够重新上线，且主要监控上报恢复正常。
2. 进程列表、服务定义、容器参数和安装日志里只保留 `--config` / `--token-file`，不再出现明文 token。
3. 配置文件与 `auto-discovery.json` 的权限满足最小权限要求。
4. 如果显式启用了远控能力，确认新的 capability 开关与旧部署预期一致。

## 已安装节点如何升级到当前修复版

### Linux / macOS（官方安装脚本或 systemd 服务）

默认安装目录是 `/opt/komari`，默认服务名是 `komari-agent`，如果安装时传过 `-t/--token` 且没有显式传 `--config`，安装脚本通常已经为你生成了受限权限的配置文件 `/opt/komari/komari-agent.json`。

推荐升级步骤：

1. 备份现有配置文件：`sudo cp /opt/komari/komari-agent.json /opt/komari/komari-agent.json.bak`
2. 按原先的非敏感参数重新运行安装脚本，优先直接复用已有配置文件，例如：

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh) \
	--config /opt/komari/komari-agent.json \
	--enable-ping \
	--max-concurrent-pings 24 \
	--ping-min-interval-millis 0
```

3. 安装脚本会替换二进制并重建服务定义，随后自动执行 `systemctl daemon-reload`、`systemctl enable komari-agent.service` 和 `systemctl start komari-agent.service`
4. 升级后立即检查：

```sh
sudo systemctl status komari-agent
sudo journalctl -u komari-agent -n 100 -f
```

如果你是手工管理二进制，也可以直接替换 `/opt/komari/agent` 后手工执行 `sudo systemctl restart komari-agent`。

### Windows（install.ps1 / NSSM 服务）

默认安装目录是 `%ProgramFiles%\Komari`，默认配置文件是 `%ProgramFiles%\Komari\komari-agent.json`，默认服务名是 `komari-agent`。

推荐升级步骤：

1. 先确认现有配置文件确实存在，再备份 `komari-agent.json`
2. 如果配置文件存在，优先通过 `--config` 复用现有配置文件，而不是再次把 token 写进命令行
3. 如果配置文件不存在，不要把一个不存在的路径直接传给 `--config`；请先恢复备份，或改为传入 `--endpoint` 加 `--token` 让安装脚本重新生成配置文件
4. 安装脚本会通过 NSSM 重新注册并启动 `komari-agent` 服务
5. 升级后检查服务状态，并确认新的 Ping 配置已经写进配置文件

### 升级后重点复核什么

- 需要延迟监测的节点是否已显式开启 `enable_ping`
- 若同节点承载多条同 interval Ping 任务，`ping_min_interval_millis` 是否为 `0`
- `max_control_requests` / `control_request_window` 只会影响终端和远程命令执行，不再影响 Ping
- `ignore_unsafe_cert` 是否已经从生产环境配置中移除

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