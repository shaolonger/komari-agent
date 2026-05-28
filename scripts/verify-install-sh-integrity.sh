#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
INSTALLER_PATH="${REPO_ROOT}/install.sh"

extract_function() {
    local function_name="$1"
    sed -n "/^${function_name}() {/,/^}/p" "$INSTALLER_PATH" | tr -d '\r'
}

source <(
    extract_function "sha256_file"
    printf '\n'
    extract_function "verify_release_checksum"
    printf '\n'
    extract_function "normalize_trusted_github_proxy"
    printf '\n'
    extract_function "stage_installer_for_sudo"
)

tmp_dir="$(mktemp -d)"
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT

payload_path="${tmp_dir}/agent"
checksum_path="${tmp_dir}/agent.sha256"

printf 'trusted payload' > "$payload_path"
printf '%s  %s\n' "$(sha256_file "$payload_path")" "agent" > "$checksum_path"

verify_release_checksum "$payload_path" "$checksum_path"

printf 'tampered payload' > "$payload_path"
if verify_release_checksum "$payload_path" "$checksum_path"; then
    printf 'expected tampered payload verification to fail\n' >&2
    exit 1
fi

normalized_proxy="$(normalize_trusted_github_proxy 'https://mirror.example.com/github-release/' 'true')"
if [ "$normalized_proxy" != 'https://mirror.example.com/github-release' ]; then
    printf 'unexpected normalized proxy: %s\n' "$normalized_proxy" >&2
    exit 1
fi

if normalize_trusted_github_proxy 'https://mirror.example.com/github-release' 'false' >/dev/null 2>&1; then
    printf 'expected unacknowledged proxy usage to fail\n' >&2
    exit 1
fi

if normalize_trusted_github_proxy 'http://mirror.example.com/github-release' 'true' >/dev/null 2>&1; then
    printf 'expected non-https proxy to fail\n' >&2
    exit 1
fi

if normalize_trusted_github_proxy 'https://user:pass@mirror.example.com/github-release' 'true' >/dev/null 2>&1; then
    printf 'expected embedded credentials to fail\n' >&2
    exit 1
fi

if normalize_trusted_github_proxy 'https://mirror.example.com/github-release?token=123' 'true' >/dev/null 2>&1; then
    printf 'expected proxy query string to fail\n' >&2
    exit 1
fi

source_script="${tmp_dir}/source-installer.sh"
printf '%s\n' '#!/usr/bin/env bash' 'printf test' > "$source_script"

staged_script="$(stage_installer_for_sudo "$source_script")"
if ! cmp -s "$source_script" "$staged_script"; then
    printf 'expected staged installer copy to match original source\n' >&2
    exit 1
fi

if [ ! -x "$staged_script" ]; then
    printf 'expected staged installer copy to be executable\n' >&2
    exit 1
fi

fallback_source="${tmp_dir}/fallback-installer.sh"
printf '%s\n' '#!/usr/bin/env bash' 'printf fallback' > "$fallback_source"

KOMARI_INSTALLER_REEXEC_PATH="$fallback_source"
fallback_staged_script="$(stage_installer_for_sudo "$tmp_dir")"
unset KOMARI_INSTALLER_REEXEC_PATH

if ! cmp -s "$fallback_source" "$fallback_staged_script"; then
    printf 'expected fallback staged installer copy to match downloaded source\n' >&2
    exit 1
fi

if [ ! -x "$fallback_staged_script" ]; then
    printf 'expected fallback staged installer copy to be executable\n' >&2
    exit 1
fi

printf 'install.sh integrity checks passed\n'