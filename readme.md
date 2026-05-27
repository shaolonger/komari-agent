# komari-agent

支持使用环境变量 / JSON 配置文件来传入 agent 参数。

## 安全建议

- 不要在命令行直接传入 `--token` 或 `-t`，这会把 token 暴露给 shell history、进程列表和容器元数据。
- 推荐通过只读挂载的 JSON 配置文件传入敏感参数，并确保配置文件仅对 owner 可读。

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