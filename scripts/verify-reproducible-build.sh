#!/bin/sh

set -eu

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

version=v0.0.0-reproducibility-test
commit=0000000000000000000000000000000000000000

mkdir -p "$work_dir/first" "$work_dir/second"
./scripts/build-release.sh "$work_dir/first/komari-agent" "$version" "$commit"
./scripts/build-release.sh "$work_dir/second/komari-agent" "$version" "$commit"

if ! cmp -s "$work_dir/first/komari-agent" "$work_dir/second/komari-agent"; then
    echo "release builds are not byte-for-byte reproducible" >&2
    exit 1
fi

if [ -n "$(go tool buildid "$work_dir/first/komari-agent")" ]; then
    echo "release binary unexpectedly contains a Go build ID" >&2
    exit 1
fi

go version -m "$work_dir/first/komari-agent" | grep -F -- '-pgo=' >/dev/null
strings "$work_dir/first/komari-agent" | grep -F -x -- "$version" >/dev/null
strings "$work_dir/first/komari-agent" | grep -F -x -- "$commit" >/dev/null

CGO_ENABLED=0 go build \
    -mod=readonly \
    -trimpath \
    -buildvcs=false \
    -pgo=default.pgo \
    -o "$work_dir/unstripped" \
    .

stripped_size=$(wc -c < "$work_dir/first/komari-agent")
unstripped_size=$(wc -c < "$work_dir/unstripped")
if [ "$stripped_size" -ge "$unstripped_size" ]; then
    echo "stripped release binary is not smaller than the unstripped binary" >&2
    exit 1
fi

"$work_dir/first/komari-agent" --help >/dev/null

echo "reproducible build verified: $stripped_size bytes (unstripped: $unstripped_size bytes)"
