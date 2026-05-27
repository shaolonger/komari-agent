# komari-agent

支持使用环境变量 / JSON 配置文件来传入 agent 参数。

## 安全建议

- 不要在命令行直接传入 `--token` 或 `-t`，这会把 token 暴露给 shell history、进程列表和容器元数据。
- 推荐通过只读挂载的 JSON 配置文件传入敏感参数，并确保配置文件仅对 owner 可读。

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