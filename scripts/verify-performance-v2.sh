#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
verify_tmp=$(mktemp -d "${TMPDIR:-/tmp}/komari-agent-v2.XXXXXX")
trap 'rm -rf "$verify_tmp"' EXIT HUP INT TERM

cd "$repo"
go test ./...
go test -race ./server ./monitoring ./protocol/telemetryv2 ./protocol/telemetryv3
go vet ./...

go test ./protocol/telemetryv2 -run '^$' -fuzz '^FuzzDecodeNeverPanics$' -fuzztime=2s
go test ./protocol/telemetryv3 -run '^$' -fuzz '^FuzzDecodeV3NeverPanics$' -fuzztime=2s

GOMAXPROCS=1 GOMEMLIMIT=32MiB go test ./monitoring ./server \
  -run 'TestV3SeventyTwoHourEquivalentResourceFixture|TestDisconnectedSpoolStaysBoundedAcrossLongLogicalOutage' \
  -count=1

for target in linux/amd64 linux/arm64 windows/amd64 freebsd/amd64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  suffix=
  if [ "$target_os" = windows ]; then suffix=.exe; fi
  CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch \
    go build -trimpath -o "$verify_tmp/agent-$target_os-$target_arch$suffix" .
done

echo "Agent V2 race, fuzz, constrained soak and cross-platform builds passed"
