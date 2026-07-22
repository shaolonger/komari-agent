#!/bin/sh

set -eu

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
    echo "usage: $0 OUTPUT VERSION COMMIT [PGO_PROFILE]" >&2
    exit 2
fi

output=$1
version=$2
commit=$3
pgo_profile=${4:-default.pgo}

case "$version" in
    ''|*[!0-9A-Za-z._+-]*)
        echo "VERSION contains unsupported characters" >&2
        exit 2
        ;;
esac
case "$commit" in
    ''|*[!0-9A-Za-z._+-]*)
        echo "COMMIT contains unsupported characters" >&2
        exit 2
        ;;
esac

if [ ! -f "$pgo_profile" ]; then
    echo "PGO profile not found: $pgo_profile" >&2
    exit 2
fi

if [ "$(go env GOVERSION)" != "go1.26.5" ]; then
    echo "release builds require Go 1.26.5" >&2
    exit 2
fi

ldflags="-s -w -buildid= -X github.com/komari-monitor/komari-agent/update.CurrentVersion=$version -X github.com/komari-monitor/komari-agent/update.BuildCommit=$commit"

CGO_ENABLED=${CGO_ENABLED:-0}
export CGO_ENABLED

go build \
    -mod=readonly \
    -trimpath \
    -buildvcs=false \
    -pgo="$pgo_profile" \
    -ldflags="$ldflags" \
    -o "$output" \
    .
