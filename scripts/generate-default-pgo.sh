#!/bin/sh

set -eu

GOMAXPROCS=${GOMAXPROCS:-1}
export GOMAXPROCS

if [ "$(go env GOVERSION)" != "go1.26.5" ]; then
    echo "PGO generation requires Go 1.26.5" >&2
    exit 2
fi

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

go test ./monitoring \
	-o "$work_dir/monitoring.test" \
    -run '^$' \
    -bench 'Benchmark(GenerateReport|EncodeReportV1|EncodeReportV2)$' \
    -benchtime=1s \
    -cpuprofile "$work_dir/monitoring.pprof"

go test ./server \
	-o "$work_dir/server.test" \
    -run '^$' \
    -bench 'Benchmark(OutboundQueueTelemetryCoalesce|OutboundQueueReliableRoundTrip|PingPolicyCacheHit|PingHTTPClientCacheHit)$' \
    -benchtime=1s \
    -cpuprofile "$work_dir/server.pprof"

go test ./monitoring/netstatic \
	-o "$work_dir/netstatic.test" \
    -run '^$' \
    -bench 'BenchmarkSumTrafficBetween31Days/prefix-index$' \
    -benchtime=1s \
    -cpuprofile "$work_dir/netstatic.pprof"

go tool pprof -proto \
    "$work_dir/monitoring.pprof" \
    "$work_dir/server.pprof" \
    "$work_dir/netstatic.pprof" > default.pgo

go tool pprof -top default.pgo >/dev/null
