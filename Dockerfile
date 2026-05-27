FROM alpine:3.21

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

COPY komari-agent-${TARGETOS}-${TARGETARCH} /app/komari-agent

RUN chmod +x /app/komari-agent

RUN touch /.komari-agent-container

ENTRYPOINT ["/app/komari-agent"]
# 运行时请指定参数
# Please specify parameters at runtime.
# Prefer a read-only JSON config file instead of passing --token/-t on the command line.
# eg: docker run --rm -v "$PWD/komari-agent.json:/run/komari-agent/config.json:ro" komari-agent --config /run/komari-agent/config.json
CMD ["--help"]