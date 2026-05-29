#!/bin/bash

# Color definitions for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${NC} $1"
}

log_success() {
    echo -e "${GREEN}${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "${NC} $1"
}

log_config() {
    echo -e "${CYAN}[CONFIG]${NC} $1"
}

redact_url_for_log() {
    local raw_value="$1"
    local redacted_value="$raw_value"

    if [ -z "$redacted_value" ] || [ "$redacted_value" = "(direct)" ]; then
        printf '%s' "$redacted_value"
        return
    fi

    redacted_value="$(printf '%s' "$redacted_value" | sed -E \
        -e 's#://[^/@[:space:]]+:[^/@[:space:]]+@#://<redacted>@#g' \
        -e 's#([?&](token|key|secret|signature|sig|auth|password|access_token|client_secret)=)[^&#[:space:]]+#\1<redacted>#g')"

    printf '%s' "$redacted_value"
}

redact_arg_value_for_log() {
    local flag="$1"
    local value="$2"

    case "$flag" in
        --token|-t|--auto-discovery|--cf-access-client-secret|--cf-access-client-id)
            printf '<redacted>'
            ;;
        --endpoint|-e)
            redact_url_for_log "$value"
            ;;
        *)
            printf '%s' "$value"
            ;;
    esac
}

json_escape() {
    local value="$1"

    value=${value//\\/\\\\}
    value=${value//\"/\\\"}
    value=${value//$'\n'/\\n}
    value=${value//$'\r'/\\r}
    value=${value//$'\t'/\\t}

    printf '%s' "$value"
}

write_komari_config_file() {
    local config_file="$1"
    local escaped_token

    if [ -z "$komari_token" ]; then
        return 0
    fi

    escaped_token="$(json_escape "$komari_token")"
    if ! (umask 077 && cat > "$config_file" << EOF
{
  "token": "$escaped_token"
}
EOF
); then
        return 1
    fi

    chmod 600 "$config_file"
}

redact_komari_args() {
    local raw_args="$1"
    local redacted_args=""
    local pending_flag=""
    local arg

    # shellcheck disable=SC2086
    for arg in $raw_args; do
        if [ -n "$pending_flag" ]; then
            redacted_args="$redacted_args $(redact_arg_value_for_log "$pending_flag" "$arg")"
            pending_flag=""
            continue
        fi

        case "$arg" in
            --token|-t|--auto-discovery|--cf-access-client-secret|--cf-access-client-id|--endpoint|-e)
                redacted_args="$redacted_args $arg"
                pending_flag="$arg"
                ;;
            --token=*|-t=*|--auto-discovery=*|--cf-access-client-secret=*|--cf-access-client-id=*|--endpoint=*|-e=*)
                redacted_args="$redacted_args ${arg%%=*}=$(redact_arg_value_for_log "${arg%%=*}" "${arg#*=}")"
                ;;
            *)
                redacted_args="$redacted_args $arg"
                ;;
        esac
    done

    redacted_args="${redacted_args# }"
    printf '%s' "$redacted_args"
}

sha256_file() {
    local file_path="$1"

    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$file_path" | awk '{print $1}'
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$file_path" | awk '{print $1}'
        return
    fi

    if command -v sha256 >/dev/null 2>&1; then
        sha256 -q "$file_path"
        return
    fi

    if command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 -r "$file_path" | awk '{print $1}'
        return
    fi

    return 1
}

verify_release_checksum() {
    local file_path="$1"
    local checksum_path="$2"
    local expected_checksum
    local actual_checksum

    expected_checksum="$(awk '{print $1}' "$checksum_path" | tr -d '\r')"
    if [ -z "$expected_checksum" ]; then
        return 1
    fi

    actual_checksum="$(sha256_file "$file_path")" || return 1
    [ "$actual_checksum" = "$expected_checksum" ]
}

normalize_trusted_github_proxy() {
    local proxy_url="$1"
    local trusted_flag="$2"

    if [ -z "$proxy_url" ]; then
        return 0
    fi

    if [ "$trusted_flag" != "true" ]; then
        printf '%s' "Using --install-ghproxy requires --install-ghproxy-trusted. Only organization-controlled trusted HTTPS proxies are supported." >&2
        return 1
    fi

    case "$proxy_url" in
        https://*) ;;
        *)
            printf '%s' "--install-ghproxy must use an https:// URL." >&2
            return 1
            ;;
    esac

    if printf '%s' "$proxy_url" | grep -Eq '://[^/@[:space:]]+@'; then
        printf '%s' "--install-ghproxy must not include embedded credentials." >&2
        return 1
    fi

    if printf '%s' "$proxy_url" | grep -Eq '[?#]'; then
        printf '%s' "--install-ghproxy must not include query strings or fragments." >&2
        return 1
    fi

    printf '%s' "${proxy_url%/}"
}

resolve_release_version() {
    local api_url="https://api.github.com/repos/${release_repo}/releases/latest"
    local latest_release_response

    if [ -n "$install_version" ]; then
        log_info "Attempting to install specified version: ${GREEN}$install_version${NC}"
        version_to_install="$install_version"
        return 0
    fi

    log_step "Fetching latest release version from GitHub API..."
    if ! latest_release_response="$(curl -fsSL "$api_url")"; then
        log_error "No published GitHub release is available in ${release_repo}. Publish a release with binary and .sha256 assets, or rerun with --install-version for an existing release tag."
        exit 1
    fi

    version_to_install="$(printf '%s' "$latest_release_response" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
    if [ -z "$version_to_install" ]; then
        log_error "Failed to determine the latest release tag from ${release_repo}."
        exit 1
    fi

    log_success "Latest version fetched: ${GREEN}$version_to_install${NC}"
}

stage_installer_for_sudo() {
    local script_source="$1"
    local staged_script
    local reexec_source_path="${KOMARI_INSTALLER_REEXEC_PATH:-}"
    local reexec_source_url="${KOMARI_INSTALLER_REEXEC_URL:-https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh}"

    staged_script="$(mktemp "${TMPDIR:-/tmp}/komari-install.XXXXXX")" || return 1
    if [ -f "$script_source" ]; then
        if ! cp "$script_source" "$staged_script"; then
            rm -f "$staged_script"
            return 1
        fi
    elif [ -n "$reexec_source_path" ] && [ -f "$reexec_source_path" ]; then
        if ! cp "$reexec_source_path" "$staged_script"; then
            rm -f "$staged_script"
            return 1
        fi
    else
        if ! command -v curl >/dev/null 2>&1; then
            rm -f "$staged_script"
            return 1
        fi

        if ! curl -fsSL -o "$staged_script" "$reexec_source_url"; then
            rm -f "$staged_script"
            return 1
        fi
    fi

    chmod 700 "$staged_script" || {
        rm -f "$staged_script"
        return 1
    }

    printf '%s' "$staged_script"
}

cleanup_staged_installer_copy() {
    local staged_script="${KOMARI_INSTALLER_TEMP_COPY:-}"

    case "$staged_script" in
        "${TMPDIR:-/tmp}"/komari-install.*)
            if [ -f "$staged_script" ]; then
                rm -f "$staged_script"
            fi
            ;;
    esac
}

reexec_with_sudo() {
    local script_source="${BASH_SOURCE[0]:-$0}"
    local staged_script

    if ! command -v sudo >/dev/null 2>&1; then
        log_error "Please run as root or install sudo"
        exit 1
    fi

    if ! staged_script="$(stage_installer_for_sudo "$script_source")"; then
        log_error "Failed to stage installer for sudo re-execution"
        exit 1
    fi

    log_info "Re-running installer with sudo to complete system-wide installation..."
    exec sudo env KOMARI_INSTALLER_TEMP_COPY="$staged_script" bash "$staged_script" "${original_args[@]}"
}

# Default values
service_name="komari-agent"
target_dir="/opt/komari"
github_proxy=""
github_proxy_trusted=false
install_version=""
release_repo="shaolonger/komari-agent"
operation=""
purge_config=false
assume_yes=false
init_system=""
arch=""
version_to_install=""
file_name=""
download_url=""
checksum_url=""
download_tmp_path=""
checksum_tmp_path=""
komari_token=""
komari_args=""
komari_has_explicit_config=false
komari_explicit_config_path=""
original_args=("$@")
original_arg_count=$#

trap cleanup_staged_installer_copy EXIT

# Detect OS
os_type=$(uname -s)
case $os_type in
    Darwin)
        os_name="darwin"
        target_dir="/usr/local/komari"
        if [ ! -w "/usr/local" ] && [ "$EUID" -ne 0 ]; then
            target_dir="$HOME/.komari"
            log_info "No write permission to /usr/local, using user directory: $target_dir"
        fi
        ;;
    Linux)
        os_name="linux"
        ;;
    FreeBSD)
        os_name="freebsd"
        ;;
    MINGW*|MSYS*|CYGWIN*)
        os_name="windows"
        target_dir="/c/komari"
        ;;
    *)
        log_error "Unsupported operating system: $os_type"
        exit 1
        ;;
esac

show_banner() {
    echo -e "${WHITE}===========================================${NC}"
    echo -e "${WHITE}     Komari Agent Management Script      ${NC}"
    echo -e "${WHITE}===========================================${NC}"
    echo ""
}

show_usage() {
    show_banner
    cat << EOF
用法:
  ./install.sh                         打开交互菜单
  ./install.sh --install [agent flags] 首次安装 Agent
  ./install.sh --upgrade               升级 Agent 二进制并重启服务
  ./install.sh --reconfigure [flags]   重建 Agent 配置与服务定义
  ./install.sh --uninstall             卸载 Agent 服务与二进制
  ./install.sh --status                查看 Agent 服务状态
  ./install.sh --logs                  查看 Agent 服务日志
  ./install.sh --restart               重启 Agent 服务
  ./install.sh --stop                  停止 Agent 服务

常用安装参数:
  --install-dir PATH
  --install-service-name NAME
  --install-version TAG
  --install-ghproxy URL --install-ghproxy-trusted
  --purge-config       卸载时额外删除配置文件
  --yes                跳过卸载确认

常用 Agent 参数:
  --endpoint URL
  --token TOKEN
  --config PATH
  --enable-ping
  --max-concurrent-pings N
  --ping-min-interval-millis N

说明:
  1. 首次安装/重配会重建服务定义。
  2. 升级只替换二进制并重启现有服务，不再重建配置。
  3. 无参数执行时会进入交互式菜单。
EOF
}

prompt_yes_no() {
    local prompt="$1"
    local default_answer="$2"
    local answer

    while true; do
        if [ "$default_answer" = "true" ]; then
            read -r -p "$prompt [Y/n]: " answer
            answer=${answer:-Y}
        else
            read -r -p "$prompt [y/N]: " answer
            answer=${answer:-N}
        fi

        case "$answer" in
            [Yy]|[Yy][Ee][Ss]) return 0 ;;
            [Nn]|[Nn][Oo]) return 1 ;;
            *) log_error "请输入 y 或 n。" ;;
        esac
    done
}

prompt_with_default() {
    local prompt="$1"
    local default_value="$2"
    local answer

    read -r -p "$prompt [$default_value]: " answer
    printf '%s' "${answer:-$default_value}"
}

refresh_derived_values() {
    komari_args="${komari_args# }"
    komari_agent_path="${target_dir}/agent"
    komari_config_file="${target_dir}/komari-agent.json"
    legacy_komari_token_file="${target_dir}/komari-agent.token"
    komari_service_args="$komari_args"
    if [ -n "$komari_token" ]; then
        komari_service_args="$komari_service_args --config $komari_config_file"
    fi
    komari_service_args="${komari_service_args# }"
    komari_service_args_log="$(redact_komari_args "$komari_service_args")"
    github_proxy_log="$(redact_url_for_log "${github_proxy:-"(direct)"}")"
}

parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --install)
                operation="install"
                shift
                ;;
            --upgrade)
                operation="upgrade"
                shift
                ;;
            --reconfigure)
                operation="reconfigure"
                shift
                ;;
            --uninstall)
                operation="uninstall"
                shift
                ;;
            --status)
                operation="status"
                shift
                ;;
            --logs)
                operation="logs"
                shift
                ;;
            --restart)
                operation="restart"
                shift
                ;;
            --stop)
                operation="stop"
                shift
                ;;
            --menu)
                operation="menu"
                shift
                ;;
            --help|-h)
                operation="help"
                shift
                ;;
            --yes|-y)
                assume_yes=true
                shift
                ;;
            --purge-config)
                purge_config=true
                shift
                ;;
            --install-dir)
                target_dir="$2"
                shift 2
                ;;
            --install-service-name)
                service_name="$2"
                shift 2
                ;;
            --install-ghproxy)
                if [ $# -lt 2 ]; then
                    log_error "Missing value for $1"
                    exit 1
                fi
                github_proxy="$2"
                shift 2
                ;;
            --install-ghproxy-trusted)
                github_proxy_trusted=true
                shift
                ;;
            --install-version)
                install_version="$2"
                shift 2
                ;;
            --token|-t)
                if [ $# -lt 2 ]; then
                    log_error "Missing value for $1"
                    exit 1
                fi
                komari_token="$2"
                shift 2
                ;;
            --token=*)
                komari_token="${1#*=}"
                shift
                ;;
            -t=*)
                komari_token="${1#*=}"
                shift
                ;;
            --config)
                if [ $# -lt 2 ]; then
                    log_error "Missing value for $1"
                    exit 1
                fi
                komari_has_explicit_config=true
                komari_explicit_config_path="$2"
                komari_args="$komari_args $1 $2"
                shift 2
                ;;
            --config=*)
                komari_has_explicit_config=true
                komari_explicit_config_path="${1#*=}"
                komari_args="$komari_args $1"
                shift
                ;;
            --install*)
                log_warning "Unknown install parameter: $1"
                shift
                ;;
            *)
                komari_args="$komari_args $1"
                shift
                ;;
        esac
    done

    if [ -z "$operation" ]; then
        if [ "$original_arg_count" -eq 0 ]; then
            operation="menu"
        else
            operation="install"
        fi
    fi
}

validate_argument_state() {
    if [ -n "$komari_token" ] && [ "$komari_has_explicit_config" = true ]; then
        log_error "Cannot combine --token with an explicit --config. Remove --config and let the installer generate a protected config file."
        exit 1
    fi

    if [ -n "$github_proxy" ]; then
        proxy_validation_output="$(normalize_trusted_github_proxy "$github_proxy" "$github_proxy_trusted" 2>&1)"
        if [ $? -ne 0 ]; then
            log_error "$proxy_validation_output"
            exit 1
        fi
        github_proxy="$proxy_validation_output"
        log_warning "Using --install-ghproxy only with an organization-controlled HTTPS proxy that mirrors GitHub release binaries and .sha256 assets without modification."
    fi

    refresh_derived_values
}

operation_needs_root() {
    [ "$operation" != "help" ]
}

# macOS doesn't always require sudo for everything
if [ "$os_name" = "darwin" ] && command -v brew >/dev/null 2>&1; then
    require_root_for_deps=false
else
    require_root_for_deps=true
fi

parse_arguments "$@"
validate_argument_state

if [ "$EUID" -ne 0 ] && [ "$require_root_for_deps" = true ] && operation_needs_root; then
    reexec_with_sudo
fi

show_operation_configuration() {
    local heading="$1"
    show_banner
    log_config "$heading"
    log_config "  Service name: ${GREEN}$service_name${NC}"
    log_config "  Install directory: ${GREEN}$target_dir${NC}"
    log_config "  GitHub proxy: ${GREEN}$github_proxy_log${NC}"
    if [ -n "$komari_service_args_log" ]; then
        log_config "  Agent arguments: ${GREEN}$komari_service_args_log${NC}"
    fi
    if [ -n "$komari_token" ]; then
        log_config "  Config file: ${GREEN}$komari_config_file${NC}"
    elif [ "$komari_has_explicit_config" = true ]; then
        log_config "  Config file: ${GREEN}$komari_explicit_config_path${NC}"
    fi
    if [ -n "$install_version" ]; then
        log_config "  Target version: ${GREEN}$install_version${NC}"
    else
        log_config "  Target version: ${GREEN}Latest${NC}"
    fi
    echo ""
}

install_dependencies() {
    log_step "Checking and installing dependencies..."

    local deps="curl"
    local missing_deps=""
    for cmd in $deps; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            missing_deps="$missing_deps $cmd"
        fi
    done

    if [ -n "$missing_deps" ]; then
        if command -v apt >/dev/null 2>&1; then
            log_info "Using apt to install dependencies..."
            apt update
            apt install -y $missing_deps
        elif command -v yum >/dev/null 2>&1; then
            log_info "Using yum to install dependencies..."
            yum install -y $missing_deps
        elif command -v apk >/dev/null 2>&1; then
            log_info "Using apk to install dependencies..."
            apk add $missing_deps
        elif command -v brew >/dev/null 2>&1; then
            log_info "Using Homebrew to install dependencies..."
            brew install $missing_deps
        else
            log_error "No supported package manager found (apt/yum/apk/brew)"
            return 1
        fi

        for cmd in $missing_deps; do
            if ! command -v "$cmd" >/dev/null 2>&1; then
                log_error "Failed to install $cmd"
                return 1
            fi
        done
        log_success "Dependencies installed successfully"
    else
        log_success "Dependencies already satisfied"
    fi
}

resolve_architecture() {
    local machine_arch
    machine_arch=$(uname -m)
    case $machine_arch in
        x86_64)
            arch="amd64"
            ;;
        aarch64|arm64)
            arch="arm64"
            ;;
        i386|i686)
            case $os_name in
                freebsd|linux|windows) arch="386" ;;
                *) log_error "32-bit x86 architecture not supported on $os_name"; return 1 ;;
            esac
            ;;
        armv7*|armv6*)
            case $os_name in
                freebsd|linux) arch="arm" ;;
                *) log_error "32-bit ARM architecture not supported on $os_name"; return 1 ;;
            esac
            ;;
        *)
            log_error "Unsupported architecture: $machine_arch on $os_name"
            return 1
            ;;
    esac
    log_info "Detected OS: ${GREEN}$os_name${NC}, Architecture: ${GREEN}$arch${NC}"
}

prepare_download_context() {
    install_dependencies || return 1
    resolve_architecture || return 1
    version_to_install=""
    resolve_release_version || return 1

    file_name="komari-agent-${os_name}-${arch}"
    if [ -n "$github_proxy" ]; then
        download_url="${github_proxy}/https://github.com/shaolonger/komari-agent/releases/download/${version_to_install}/${file_name}"
    else
        download_url="https://github.com/shaolonger/komari-agent/releases/download/${version_to_install}/${file_name}"
    fi
    checksum_url="${download_url}.sha256"
    download_tmp_path="${komari_agent_path}.download.$$"
    checksum_tmp_path="${download_tmp_path}.sha256"
    mkdir -p "$target_dir"
}

download_release_binary() {
    if [ -n "$github_proxy" ]; then
        log_step "Downloading $file_name via proxy..."
    else
        log_step "Downloading $file_name directly..."
    fi
    log_info "URL: ${CYAN}$(redact_url_for_log "$download_url")${NC}"
    if ! curl -fL -o "$download_tmp_path" "$download_url"; then
        rm -f "$download_tmp_path" "$checksum_tmp_path"
        log_error "Download failed. Ensure ${release_repo} release ${version_to_install} includes ${file_name}."
        return 1
    fi

    log_step "Downloading SHA256 checksum for $file_name..."
    log_info "URL: ${CYAN}$(redact_url_for_log "$checksum_url")${NC}"
    if ! curl -fL -o "$checksum_tmp_path" "$checksum_url"; then
        rm -f "$download_tmp_path" "$checksum_tmp_path"
        log_error "Checksum download failed. Ensure ${release_repo} release ${version_to_install} includes ${file_name}.sha256."
        return 1
    fi

    log_step "Verifying SHA256 checksum..."
    if ! verify_release_checksum "$download_tmp_path" "$checksum_tmp_path"; then
        rm -f "$download_tmp_path" "$checksum_tmp_path"
        log_error "Checksum verification failed"
        return 1
    fi

    if ! chmod +x "$download_tmp_path"; then
        rm -f "$download_tmp_path" "$checksum_tmp_path"
        log_error "Failed to set executable permissions on the downloaded binary"
        return 1
    fi
}

replace_downloaded_binary() {
    if ! mv -f "$download_tmp_path" "$komari_agent_path"; then
        rm -f "$download_tmp_path" "$checksum_tmp_path"
        log_error "Failed to replace the installed binary"
        return 1
    fi
    rm -f "$checksum_tmp_path"
    log_success "Komari-agent binary updated at ${GREEN}$komari_agent_path${NC}"
}

has_endpoint_arg() {
    case " $komari_args " in
        *" --endpoint "*|*" -e "*|*" --endpoint="*|*" -e="*)
            return 0
            ;;
    esac
    return 1
}

config_has_endpoint() {
    local config_path="$1"
    grep -Eq '"endpoint"[[:space:]]*:' "$config_path"
}

ensure_install_inputs() {
    if [ -n "$komari_token" ] && ! has_endpoint_arg; then
        log_error "When generating a config from --token, you must also provide --endpoint."
        return 1
    fi

    if [ "$komari_has_explicit_config" = true ]; then
        if [ ! -f "$komari_explicit_config_path" ]; then
            log_error "The specified config file does not exist: $komari_explicit_config_path"
            return 1
        fi
        if ! has_endpoint_arg && ! config_has_endpoint "$komari_explicit_config_path"; then
            log_error "The selected config file does not contain an endpoint, and no --endpoint flag was provided."
            return 1
        fi
        return 0
    fi

    if [ -n "$komari_token" ]; then
        return 0
    fi

    if [ -f "$komari_config_file" ]; then
        komari_has_explicit_config=true
        komari_explicit_config_path="$komari_config_file"
        komari_args="$komari_args --config $komari_config_file"
        refresh_derived_values
        if ! has_endpoint_arg && ! config_has_endpoint "$komari_config_file"; then
            log_error "The default config file does not contain an endpoint, and no --endpoint flag was provided."
            return 1
        fi
        return 0
    fi

    log_error "No usable agent config was found. Provide --config, or provide --endpoint with --token, or use the interactive install menu."
    return 1
}

write_generated_config_if_needed() {
    if [ -n "$komari_token" ]; then
        log_step "Writing service config file..."
        if ! write_komari_config_file "$komari_config_file"; then
            log_error "Failed to write config file: $komari_config_file"
            return 1
        fi
        log_success "Service config stored at ${GREEN}$komari_config_file${NC}"
    fi
}

detect_init_system() {
    if [ -f /etc/NIXOS ]; then
        echo "nixos"
        return
    fi

    if [ -f /etc/alpine-release ]; then
        if command -v rc-service >/dev/null 2>&1 || [ -f /sbin/openrc-run ]; then
            echo "openrc"
            return
        fi
    fi

    local pid1_process
    pid1_process=$(ps -p 1 -o comm= 2>/dev/null | tr -d ' ')

    if [ "$pid1_process" = "systemd" ] || [ -d /run/systemd/system ]; then
        if command -v systemctl >/dev/null 2>&1 && systemctl list-units >/dev/null 2>&1; then
            echo "systemd"
            return
        fi
    fi

    if [ "$pid1_process" = "openrc-init" ]; then
        if command -v rc-service >/dev/null 2>&1; then
            echo "openrc"
            return
        fi
    fi

    if [ "$pid1_process" = "init" ] && [ ! -f /etc/alpine-release ]; then
        if [ -d /run/openrc ] && command -v rc-service >/dev/null 2>&1; then
            echo "openrc"
            return
        fi
        if [ -f /sbin/openrc ] && command -v rc-service >/dev/null 2>&1; then
            echo "openrc"
            return
        fi
    fi

    if command -v uci >/dev/null 2>&1 && [ -f /etc/rc.common ]; then
        echo "procd"
        return
    fi

    if [ "$os_name" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
        echo "launchd"
        return
    fi

    if command -v systemctl >/dev/null 2>&1 && systemctl list-units >/dev/null 2>&1; then
        echo "systemd"
        return
    fi

    if command -v rc-service >/dev/null 2>&1 && [ -d /etc/init.d ]; then
        echo "openrc"
        return
    fi

    if command -v initctl >/dev/null 2>&1 && [ -d /etc/init ]; then
        echo "upstart"
        return
    fi

    echo "unknown"
}

ensure_init_system_detected() {
    if [ -z "$init_system" ]; then
        init_system=$(detect_init_system)
        log_info "Detected init system: ${GREEN}$init_system${NC}"
    fi
}

launchd_system_plist() {
    printf '/Library/LaunchDaemons/com.komari.%s.plist' "$service_name"
}

launchd_user_plist() {
    printf '%s/Library/LaunchAgents/com.komari.%s.plist' "$HOME" "$service_name"
}

service_exists() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            systemctl list-unit-files | grep -Fq "${service_name}.service"
            ;;
        openrc|procd)
            [ -f "/etc/init.d/${service_name}" ]
            ;;
        upstart)
            [ -f "/etc/init/${service_name}.conf" ]
            ;;
        launchd)
            [ -f "$(launchd_system_plist)" ] || [ -f "$(launchd_user_plist)" ]
            ;;
        *)
            return 1
            ;;
    esac
}

stop_registered_service() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            systemctl stop ${service_name}.service >/dev/null 2>&1 || true
            ;;
        openrc)
            rc-service ${service_name} stop >/dev/null 2>&1 || true
            ;;
        procd)
            /etc/init.d/${service_name} stop >/dev/null 2>&1 || true
            ;;
        upstart)
            initctl stop ${service_name} >/dev/null 2>&1 || true
            ;;
        launchd)
            if [ -f "$(launchd_system_plist)" ]; then
                launchctl bootout system "$(launchd_system_plist)" >/dev/null 2>&1 || true
            fi
            if [ -f "$(launchd_user_plist)" ]; then
                launchctl bootout gui/$(id -u) "$(launchd_user_plist)" >/dev/null 2>&1 || true
            fi
            ;;
        *)
            ;;
    esac
}

start_registered_service() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            systemctl start ${service_name}.service
            ;;
        openrc)
            rc-service ${service_name} start
            ;;
        procd)
            /etc/init.d/${service_name} start
            ;;
        upstart)
            initctl start ${service_name}
            ;;
        launchd)
            if [ -f "$(launchd_system_plist)" ]; then
                launchctl bootstrap system "$(launchd_system_plist)"
            elif [ -f "$(launchd_user_plist)" ]; then
                launchctl bootstrap gui/$(id -u) "$(launchd_user_plist)"
            else
                log_error "Launchd plist not found for ${service_name}"
                return 1
            fi
            ;;
        *)
            log_error "Unsupported or unknown init system detected: $init_system"
            return 1
            ;;
    esac
}

restart_registered_service() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            systemctl restart ${service_name}.service
            ;;
        openrc)
            rc-service ${service_name} restart
            ;;
        procd)
            /etc/init.d/${service_name} restart
            ;;
        upstart)
            initctl restart ${service_name}
            ;;
        launchd)
            stop_registered_service
            start_registered_service
            ;;
        *)
            log_error "Unsupported or unknown init system detected: $init_system"
            return 1
            ;;
    esac
}

remove_service_registration() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            if systemctl list-unit-files | grep -Fq "${service_name}.service"; then
                systemctl stop ${service_name}.service >/dev/null 2>&1 || true
                systemctl disable ${service_name}.service >/dev/null 2>&1 || true
                rm -f "/etc/systemd/system/${service_name}.service"
                systemctl daemon-reload
            fi
            ;;
        openrc)
            if [ -f "/etc/init.d/${service_name}" ]; then
                rc-service ${service_name} stop >/dev/null 2>&1 || true
                rc-update del ${service_name} default >/dev/null 2>&1 || true
                rm -f "/etc/init.d/${service_name}"
            fi
            ;;
        procd)
            if [ -f "/etc/init.d/${service_name}" ]; then
                /etc/init.d/${service_name} stop >/dev/null 2>&1 || true
                /etc/init.d/${service_name} disable >/dev/null 2>&1 || true
                rm -f "/etc/init.d/${service_name}"
            fi
            ;;
        upstart)
            if [ -f "/etc/init/${service_name}.conf" ]; then
                initctl stop ${service_name} >/dev/null 2>&1 || true
                rm -f "/etc/init/${service_name}.conf"
            fi
            ;;
        launchd)
            if [ -f "$(launchd_system_plist)" ]; then
                launchctl bootout system "$(launchd_system_plist)" >/dev/null 2>&1 || true
                rm -f "$(launchd_system_plist)"
            fi
            if [ -f "$(launchd_user_plist)" ]; then
                launchctl bootout gui/$(id -u) "$(launchd_user_plist)" >/dev/null 2>&1 || true
                rm -f "$(launchd_user_plist)"
            fi
            ;;
        nixos)
            log_warning "NixOS detected. Please remove the declarative service from your NixOS configuration manually."
            ;;
        *)
            ;;
    esac
}

configure_service() {
    ensure_init_system_detected
    log_step "Configuring system service..."

    case "$init_system" in
        nixos)
            log_warning "NixOS detected. System services must be configured declaratively."
            log_info "Please add the following to your NixOS configuration:"
            echo ""
            echo -e "${CYAN}systemd.services.${service_name} = {${NC}"
            echo -e "${CYAN}  description = \"Komari Agent Service\";${NC}"
            echo -e "${CYAN}  after = [ \"network.target\" ];${NC}"
            echo -e "${CYAN}  wantedBy = [ \"multi-user.target\" ];${NC}"
            echo -e "${CYAN}  serviceConfig = {${NC}"
            echo -e "${CYAN}    Type = \"simple\";${NC}"
            echo -e "${CYAN}    ExecStart = \"${komari_agent_path} ${komari_service_args_log}\";${NC}"
            echo -e "${CYAN}    WorkingDirectory = \"${target_dir}\";${NC}"
            echo -e "${CYAN}    Restart = \"always\";${NC}"
            echo -e "${CYAN}    User = \"root\";${NC}"
            echo -e "${CYAN}  };${NC}"
            echo -e "${CYAN}};${NC}"
            echo ""
            log_info "Then run: sudo nixos-rebuild switch"
            ;;
        openrc)
            local service_file="/etc/init.d/${service_name}"
            cat > "$service_file" << EOF
#!/sbin/openrc-run

name="Komari Agent Service"
description="Komari monitoring agent"
command="${komari_agent_path}"
command_args="${komari_service_args}"
command_user="root"
directory="${target_dir}"
pidfile="/run/${service_name}.pid"
retry="SIGTERM/30"
supervisor=supervise-daemon

depend() {
    need net
    after network
}
EOF
            chmod +x "$service_file"
            rc-update add ${service_name} default >/dev/null 2>&1 || true
            start_registered_service
            ;;
        systemd)
            local service_file="/etc/systemd/system/${service_name}.service"
            cat > "$service_file" << EOF
[Unit]
Description=Komari Agent Service
After=network.target

[Service]
Type=simple
ExecStart=${komari_agent_path} ${komari_service_args}
WorkingDirectory=${target_dir}
Restart=always
User=root

[Install]
WantedBy=multi-user.target
EOF
            systemctl daemon-reload
            systemctl enable ${service_name}.service >/dev/null 2>&1 || true
            start_registered_service
            ;;
        procd)
            local service_file="/etc/init.d/${service_name}"
            cat > "$service_file" << EOF
#!/bin/sh /etc/rc.common

START=99
STOP=10

USE_PROCD=1

PROG="${komari_agent_path}"
ARGS="${komari_service_args}"

start_service() {
    procd_open_instance
    procd_set_param command \$PROG \$ARGS
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_set_param user root
    procd_close_instance
}

stop_service() {
    killall \$(basename \$PROG)
}

reload_service() {
    stop
    start
}
EOF
            chmod +x "$service_file"
            /etc/init.d/${service_name} enable >/dev/null 2>&1 || true
            start_registered_service
            ;;
        launchd)
            local plist_dir plist_file service_user log_dir
            if [[ "$target_dir" =~ ^/Users/.* ]] || [ "$EUID" -ne 0 ]; then
                plist_dir="$HOME/Library/LaunchAgents"
                plist_file="$plist_dir/com.komari.${service_name}.plist"
                service_user="$(whoami)"
                log_dir="$HOME/Library/Logs"
            else
                plist_dir="/Library/LaunchDaemons"
                plist_file="$plist_dir/com.komari.${service_name}.plist"
                service_user="root"
                log_dir="/var/log"
            fi
            mkdir -p "$plist_dir"
            cat > "$plist_file" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.komari.${service_name}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${komari_agent_path}</string>
EOF
            if [ -n "$komari_service_args" ]; then
                echo "$komari_service_args" | xargs -n1 printf "        <string>%s</string>\n" >> "$plist_file"
            fi
            cat >> "$plist_file" << EOF
    </array>
    <key>WorkingDirectory</key>
    <string>${target_dir}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>UserName</key>
    <string>${service_user}</string>
    <key>StandardOutPath</key>
    <string>${log_dir}/${service_name}.log</string>
    <key>StandardErrorPath</key>
    <string>${log_dir}/${service_name}.log</string>
</dict>
</plist>
EOF
            start_registered_service
            ;;
        upstart)
            local service_file="/etc/init/${service_name}.conf"
            cat > "$service_file" << EOF
# KOMARI Agent
description "Komari Agent Service"

chdir ${target_dir}
start on filesystem or runlevel [2345]
stop on runlevel [!2345]

respawn
respawn limit 10 5
umask 022

console none

pre-start script
    test -x ${komari_agent_path} || { stop; exit 0; }
end script

script
    exec ${komari_agent_path} ${komari_service_args}
end script
EOF
            initctl reload-configuration
            start_registered_service
            ;;
        *)
            log_error "Unsupported or unknown init system detected: $init_system"
            return 1
            ;;
    esac

    log_success "Service configuration updated for ${GREEN}$service_name${NC}"
}

show_status() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            systemctl status ${service_name}.service --no-pager
            ;;
        openrc)
            rc-service ${service_name} status
            ;;
        procd)
            /etc/init.d/${service_name} status
            ;;
        upstart)
            initctl status ${service_name}
            ;;
        launchd)
            if [ -f "$(launchd_system_plist)" ]; then
                launchctl print system/com.komari.${service_name}
            elif [ -f "$(launchd_user_plist)" ]; then
                launchctl print gui/$(id -u)/com.komari.${service_name}
            else
                log_error "Launchd plist not found for ${service_name}"
                return 1
            fi
            ;;
        nixos)
            systemctl status ${service_name}.service --no-pager || true
            ;;
        *)
            log_error "Unsupported or unknown init system detected: $init_system"
            return 1
            ;;
    esac
}

show_logs() {
    ensure_init_system_detected
    case "$init_system" in
        systemd)
            journalctl -u ${service_name} -n 100 -f
            ;;
        openrc)
            log_warning "OpenRC environments do not have a single standard service log path. Check your system log (for example /var/log/messages)."
            ;;
        procd)
            if command -v logread >/dev/null 2>&1; then
                logread -f
            else
                log_warning "logread is unavailable on this host. Check the system log manually."
            fi
            ;;
        upstart)
            log_warning "Upstart logs are usually written to /var/log/upstart/${service_name}.log"
            ;;
        launchd)
            if [ -f "/var/log/${service_name}.log" ]; then
                tail -n 100 -f "/var/log/${service_name}.log"
            elif [ -f "$HOME/Library/Logs/${service_name}.log" ]; then
                tail -n 100 -f "$HOME/Library/Logs/${service_name}.log"
            else
                log_warning "Launchd log file not found for ${service_name}."
            fi
            ;;
        *)
            log_error "Unsupported or unknown init system detected: $init_system"
            return 1
            ;;
    esac
}

collect_interactive_install_inputs() {
    local endpoint_input config_choice config_path enable_ping ping_concurrency ping_min_interval

    echo "未提供完整安装参数，进入交互式配置。"
    echo "如需高级参数，请退出后重新执行脚本并追加对应 flags。"
    echo ""

    while true; do
        read -r -p "请输入面板地址 (例如 https://monitor.example.com): " endpoint_input
        if [ -n "$endpoint_input" ]; then
            break
        fi
        log_error "面板地址不能为空。"
    done

    komari_args="--endpoint $endpoint_input"
    komari_token=""
    komari_has_explicit_config=false
    komari_explicit_config_path=""

    if [ -f "$komari_config_file" ]; then
        echo "请选择认证材料来源："
        echo "  1) 复用默认配置文件 ${komari_config_file}"
        echo "  2) 使用自定义配置文件路径"
        echo "  3) 输入节点 Token，并自动生成默认配置文件"
        read -r -p "输入选项 [1-3]: " config_choice
    else
        echo "请选择认证材料来源："
        echo "  1) 使用自定义配置文件路径"
        echo "  2) 输入节点 Token，并自动生成默认配置文件"
        read -r -p "输入选项 [1-2]: " config_choice
    fi

    case "$config_choice" in
        1)
            if [ -f "$komari_config_file" ]; then
                komari_has_explicit_config=true
                komari_explicit_config_path="$komari_config_file"
                komari_args="$komari_args --config $komari_config_file"
            else
                config_path=$(prompt_with_default "请输入现有配置文件路径" "$komari_config_file")
                komari_has_explicit_config=true
                komari_explicit_config_path="$config_path"
                komari_args="$komari_args --config $config_path"
            fi
            ;;
        2)
            if [ -f "$komari_config_file" ]; then
                config_path=$(prompt_with_default "请输入现有配置文件路径" "$komari_config_file")
                komari_has_explicit_config=true
                komari_explicit_config_path="$config_path"
                komari_args="$komari_args --config $config_path"
            else
                read -r -s -p "请输入节点 Token: " komari_token
                echo ""
            fi
            ;;
        3)
            read -r -s -p "请输入节点 Token: " komari_token
            echo ""
            ;;
        *)
            log_error "无效选项"
            return 1
            ;;
    esac

    if prompt_yes_no "是否启用远程 Ping / 延迟监测" false; then
        ping_concurrency=$(prompt_with_default "请输入最大并发 Ping 数" "24")
        ping_min_interval=$(prompt_with_default "请输入最小 Ping 间隔（毫秒）" "0")
        komari_args="$komari_args --enable-ping --max-concurrent-pings $ping_concurrency --ping-min-interval-millis $ping_min_interval"
    fi

    refresh_derived_values
}

install_agent() {
    local interactive_mode="$1"

    if service_exists || [ -x "$komari_agent_path" ]; then
        log_warning "Agent appears to be installed already. Use upgrade or reconfigure instead of install."
        return 1
    fi

    if [ "$interactive_mode" = "true" ]; then
        collect_interactive_install_inputs || return 1
    fi

    ensure_install_inputs || return 1
    refresh_derived_values
    show_operation_configuration "Installation configuration:"
    prepare_download_context || return 1
    download_release_binary || return 1
    replace_downloaded_binary || return 1
    write_generated_config_if_needed || return 1
    remove_service_registration
    configure_service || return 1
    log_success "Komari-agent installation completed!"
    log_config "Service: ${GREEN}$service_name${NC}"
    log_config "Arguments: ${GREEN}$komari_service_args_log${NC}"
}

reconfigure_agent() {
    local interactive_mode="$1"

    if [ "$interactive_mode" = "true" ]; then
        collect_interactive_install_inputs || return 1
    fi

    ensure_install_inputs || return 1
    refresh_derived_values
    show_operation_configuration "Reconfiguration:"

    if [ ! -x "$komari_agent_path" ] || [ -n "$install_version" ]; then
        log_info "Agent binary is missing or a target version was requested. Downloading binary before reconfiguring..."
        prepare_download_context || return 1
        download_release_binary || return 1
        replace_downloaded_binary || return 1
    else
        log_info "Reusing existing binary at ${GREEN}$komari_agent_path${NC}"
    fi

    write_generated_config_if_needed || return 1
    remove_service_registration
    configure_service || return 1
    log_success "Komari-agent reconfiguration completed!"
}

upgrade_agent() {
    local backup_path=""
    local service_was_registered=false

    if [ ! -x "$komari_agent_path" ]; then
        log_error "Agent binary was not found at $komari_agent_path. Run install first."
        return 1
    fi

    show_operation_configuration "Upgrade configuration:"
    prepare_download_context || return 1

    if service_exists; then
        service_was_registered=true
        log_step "Stopping existing service before upgrade..."
        stop_registered_service
    fi

    backup_path="${komari_agent_path}.backup.$(date +%Y%m%d_%H%M%S)"
    cp "$komari_agent_path" "$backup_path"
    log_info "Backed up current binary to ${GREEN}$backup_path${NC}"

    if ! download_release_binary; then
        if [ "$service_was_registered" = true ]; then
            start_registered_service || true
        fi
        return 1
    fi

    if ! replace_downloaded_binary; then
        mv -f "$backup_path" "$komari_agent_path" >/dev/null 2>&1 || true
        if [ "$service_was_registered" = true ]; then
            start_registered_service || true
        fi
        return 1
    fi

    if [ "$service_was_registered" = true ]; then
        log_step "Starting service after upgrade..."
        if ! start_registered_service; then
            log_error "Failed to restart the service after upgrade. Restoring previous binary..."
            mv -f "$backup_path" "$komari_agent_path" >/dev/null 2>&1 || true
            start_registered_service || true
            return 1
        fi
    else
        log_warning "No registered service was found. Binary has been upgraded, but no service restart was performed."
    fi

    log_success "Komari-agent upgrade completed!"
}

uninstall_agent() {
    if [ "$assume_yes" != "true" ]; then
        if ! prompt_yes_no "这将卸载 Komari Agent。是否继续" false; then
            log_info "已取消卸载。"
            return 0
        fi
    fi

    remove_service_registration

    if [ -f "$komari_agent_path" ]; then
        rm -f "$komari_agent_path"
        log_success "Removed binary: ${GREEN}$komari_agent_path${NC}"
    fi

    if [ -f "$legacy_komari_token_file" ]; then
        rm -f "$legacy_komari_token_file"
    fi

    if [ "$purge_config" = true ] && [ -f "$komari_config_file" ]; then
        rm -f "$komari_config_file"
        log_success "Removed config file: ${GREEN}$komari_config_file${NC}"
    elif [ -f "$komari_config_file" ]; then
        log_warning "Preserved config file: ${GREEN}$komari_config_file${NC}"
        log_info "Use --purge-config if you also want to delete the saved config file."
    fi

    log_success "Komari-agent uninstall completed!"
}

restart_agent() {
    if ! service_exists; then
        log_error "No registered service was found for ${service_name}."
        return 1
    fi
    restart_registered_service
    log_success "Service restarted: ${GREEN}$service_name${NC}"
}

stop_agent() {
    if ! service_exists; then
        log_error "No registered service was found for ${service_name}."
        return 1
    fi
    stop_registered_service
    log_success "Service stopped: ${GREEN}$service_name${NC}"
}

main_menu() {
    show_banner
    echo "请选择操作："
    echo "  1) 安装 Agent"
    echo "  2) 升级 Agent"
    echo "  3) 重配 Agent"
    echo "  4) 卸载 Agent"
    echo "  5) 查看状态"
    echo "  6) 查看日志"
    echo "  7) 重启服务"
    echo "  8) 停止服务"
    echo "  9) 退出"
    echo ""

    read -r -p "输入选项 [1-9]: " choice
    case "$choice" in
        1) install_agent true ;;
        2) upgrade_agent ;;
        3) reconfigure_agent true ;;
        4) uninstall_agent ;;
        5) show_status ;;
        6) show_logs ;;
        7) restart_agent ;;
        8) stop_agent ;;
        9) exit 0 ;;
        *)
            log_error "无效选项"
            return 1
            ;;
    esac
}

run_operation() {
    case "$operation" in
        help)
            show_usage
            ;;
        menu)
            main_menu
            ;;
        install)
            install_agent false
            ;;
        upgrade)
            upgrade_agent
            ;;
        reconfigure)
            reconfigure_agent false
            ;;
        uninstall)
            uninstall_agent
            ;;
        status)
            show_status
            ;;
        logs)
            show_logs
            ;;
        restart)
            restart_agent
            ;;
        stop)
            stop_agent
            ;;
        *)
            log_error "Unsupported operation: $operation"
            return 1
            ;;
    esac
}

run_operation
