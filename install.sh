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
install_version="" # New parameter for specifying version
release_repo="shaolonger/komari-agent"
original_args=("$@")

trap cleanup_staged_installer_copy EXIT
 

# Detect OS
os_type=$(uname -s)
case $os_type in
    Darwin)
        os_name="darwin"
        target_dir="/usr/local/komari"  # Use /usr/local on macOS
        # Check if we can write to /usr/local, fallback to user directory
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
        target_dir="/c/komari"  # Use C:\komari on Windows
        ;;
    *)
        log_error "Unsupported operating system: $os_type"
        exit 1
        ;;
esac

# Parse install-specific arguments
komari_token=""
komari_args=""
komari_has_explicit_config=false
while [[ $# -gt 0 ]]; do
    case $1 in
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
            komari_args="$komari_args $1 $2"
            shift 2
            ;;
        --config=*)
            komari_has_explicit_config=true
            komari_args="$komari_args $1"
            shift
            ;;
        --install*)
            log_warning "Unknown install parameter: $1"
            shift
            ;;
        *)
            # Non-install arguments go to komari_args
            komari_args="$komari_args $1"
            shift
            ;;
    esac
done

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

# Remove leading space from komari_args if present
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

# macOS doesn't always require sudo for everything
if [ "$os_name" = "darwin" ] && command -v brew >/dev/null 2>&1; then
    # On macOS with Homebrew, we can run without root for dependencies
    require_root_for_deps=false
else
    require_root_for_deps=true
fi

if [ "$EUID" -ne 0 ] && [ "$require_root_for_deps" = true ]; then
    reexec_with_sudo
fi

echo -e "${WHITE}===========================================${NC}"
echo -e "${WHITE}    Komari Agent Installation Script     ${NC}"
echo -e "${WHITE}===========================================${NC}"
echo ""
log_config "Installation configuration:"
log_config "  Service name: ${GREEN}$service_name${NC}"
log_config "  Install directory: ${GREEN}$target_dir${NC}"
log_config "  GitHub proxy: ${GREEN}$github_proxy_log${NC}"
log_config "  Binary arguments: ${GREEN}$komari_service_args_log${NC}"
if [ -n "$komari_token" ]; then
    log_config "  Config file: ${GREEN}$komari_config_file${NC}"
fi
if [ -n "$install_version" ]; then
    log_config "  Specified agent version: ${GREEN}$install_version${NC}"
else
    log_config "  Agent version: ${GREEN}Latest${NC}"
fi
echo ""

# Function to uninstall the previous installation
uninstall_previous() {
    log_step "Checking for previous installation..."
    
    # Stop and disable service if it exists
    if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files | grep -q "${service_name}.service"; then
        log_info "Stopping and disabling existing systemd service..."
        systemctl stop ${service_name}.service
        systemctl disable ${service_name}.service
        rm -f "/etc/systemd/system/${service_name}.service"
        systemctl daemon-reload
    elif command -v rc-service >/dev/null 2>&1 && [ -f "/etc/init.d/${service_name}" ]; then
        log_info "Stopping and disabling existing OpenRC service..."
        rc-service ${service_name} stop
        rc-update del ${service_name} default
        rm -f "/etc/init.d/${service_name}"
    elif command -v uci >/dev/null 2>&1 && [ -f "/etc/init.d/${service_name}" ]; then
        log_info "Stopping and disabling existing procd service..."
        /etc/init.d/${service_name} stop
        /etc/init.d/${service_name} disable
        rm -f "/etc/init.d/${service_name}"
    elif command -v initctl >/dev/null 2>&1 && [ -f "/etc/init/${service_name}.conf" ]; then
        log_info "Stopping and removing existing upstart service..."
        initctl stop ${service_name}
        rm -f "/etc/init/${service_name}.conf"
    elif [ "$os_name" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
        # macOS launchd service - check both system and user locations
        system_plist="/Library/LaunchDaemons/com.komari.${service_name}.plist"
        user_plist="$HOME/Library/LaunchAgents/com.komari.${service_name}.plist"
        
        if [ -f "$system_plist" ]; then
            log_info "Stopping and removing existing system launchd service..."
            launchctl bootout system "$system_plist" 2>/dev/null || true
            rm -f "$system_plist"
        fi
        
        if [ -f "$user_plist" ]; then
            log_info "Stopping and removing existing user launchd service..."
            launchctl bootout gui/$(id -u) "$user_plist" 2>/dev/null || true
            rm -f "$user_plist"
        fi
    fi
    
    if [ -f "$komari_agent_path" ]; then
        log_info "Existing binary will be replaced after checksum verification."
    fi

    if [ -f "$komari_config_file" ]; then
        log_info "Removing old config file..."
        rm -f "$komari_config_file"
    fi

    if [ -f "$legacy_komari_token_file" ]; then
        log_info "Removing old token file..."
        rm -f "$legacy_komari_token_file"
    fi
}

# Uninstall previous installation
uninstall_previous

install_dependencies() {
    log_step "Checking and installing dependencies..."

    local deps="curl"
    local missing_deps=""
    for cmd in $deps; do
        if ! command -v $cmd >/dev/null 2>&1; then
            missing_deps="$missing_deps $cmd"
        fi
    done

    if [ -n "$missing_deps" ]; then
        # Check package manager and install dependencies
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
            exit 1
        fi
        
        # Verify installation
        for cmd in $missing_deps; do
            if ! command -v $cmd >/dev/null 2>&1; then
                log_error "Failed to install $cmd"
                exit 1
            fi
        done
        log_success "Dependencies installed successfully"
    else
        log_success "Dependencies already satisfied"
    fi
}

 
# Install dependencies
install_dependencies

 

# Architecture detection with platform-specific support
arch=$(uname -m)
case $arch in
    x86_64)
        arch="amd64"
        ;;
    aarch64|arm64)
        arch="arm64"
        ;;
    i386|i686)
        # x86 (32-bit) support
        case $os_name in
            freebsd|linux|windows)
                arch="386"
                ;;
            *)
                log_error "32-bit x86 architecture not supported on $os_name"
                exit 1
                ;;
        esac
        ;;
    armv7*|armv6*)
        # ARM 32-bit support
        case $os_name in
            freebsd|linux)
                arch="arm"
                ;;
            *)
                log_error "32-bit ARM architecture not supported on $os_name"
                exit 1
                ;;
        esac
        ;;
    *)
        log_error "Unsupported architecture: $arch on $os_name"
        exit 1
        ;;
esac
log_info "Detected OS: ${GREEN}$os_name${NC}, Architecture: ${GREEN}$arch${NC}"

version_to_install=""
resolve_release_version

# Construct download URL
file_name="komari-agent-${os_name}-${arch}"
download_path="download/${version_to_install}"

if [ -n "$github_proxy" ]; then
    # Use proxy for GitHub releases
    download_url="${github_proxy}/https://github.com/shaolonger/komari-agent/releases/${download_path}/${file_name}"
else
    # Direct access to GitHub releases
    download_url="https://github.com/shaolonger/komari-agent/releases/${download_path}/${file_name}"
fi

checksum_url="${download_url}.sha256"
download_tmp_path="${komari_agent_path}.download.$$"
checksum_tmp_path="${download_tmp_path}.sha256"

log_step "Creating installation directory: ${GREEN}$target_dir${NC}"
mkdir -p "$target_dir"

# Download binary
if [ -n "$github_proxy" ]; then
    log_step "Downloading $file_name via proxy..."
    log_info "URL: ${CYAN}$(redact_url_for_log "$download_url")${NC}"
else
    log_step "Downloading $file_name directly..."
    log_info "URL: ${CYAN}$(redact_url_for_log "$download_url")${NC}"
fi
if ! curl -fL -o "$download_tmp_path" "$download_url"; then
    rm -f "$download_tmp_path" "$checksum_tmp_path"
    log_error "Download failed. Ensure ${release_repo} release ${version_to_install} includes ${file_name}."
    exit 1
fi

log_step "Downloading SHA256 checksum for $file_name..."
log_info "URL: ${CYAN}$(redact_url_for_log "$checksum_url")${NC}"
if ! curl -fL -o "$checksum_tmp_path" "$checksum_url"; then
    rm -f "$download_tmp_path" "$checksum_tmp_path"
    log_error "Checksum download failed. Ensure ${release_repo} release ${version_to_install} includes ${file_name}.sha256."
    exit 1
fi

log_step "Verifying SHA256 checksum..."
if ! verify_release_checksum "$download_tmp_path" "$checksum_tmp_path"; then
    rm -f "$download_tmp_path" "$checksum_tmp_path"
    log_error "Checksum verification failed"
    exit 1
fi

# Set executable permissions and replace the target only after verification succeeds
if ! chmod +x "$download_tmp_path"; then
    rm -f "$download_tmp_path" "$checksum_tmp_path"
    log_error "Failed to set executable permissions on the downloaded binary"
    exit 1
fi

if ! mv -f "$download_tmp_path" "$komari_agent_path"; then
    rm -f "$download_tmp_path" "$checksum_tmp_path"
    log_error "Failed to replace the installed binary"
    exit 1
fi

rm -f "$checksum_tmp_path"
log_success "Komari-agent installed to ${GREEN}$komari_agent_path${NC}"

if [ -n "$komari_token" ]; then
    log_step "Writing service config file..."
    if ! write_komari_config_file "$komari_config_file"; then
        log_error "Failed to write config file: $komari_config_file"
        exit 1
    fi
    log_success "Service config stored at ${GREEN}$komari_config_file${NC}"
fi

# Detect init system and configure service
log_step "Configuring system service..."

# Function to detect actual init system
detect_init_system() {
    # Check if running on NixOS (special case)
    if [ -f /etc/NIXOS ]; then
        echo "nixos"
        return
    fi
    
    # Alpine Linux MUST be checked first
    # Alpine always uses OpenRC, even in containers where PID 1 might be different
    if [ -f /etc/alpine-release ]; then
        if command -v rc-service >/dev/null 2>&1 || [ -f /sbin/openrc-run ]; then
            echo "openrc"
            return
        fi
    fi
    
    # Get PID 1 process for other detection
    local pid1_process=$(ps -p 1 -o comm= 2>/dev/null | tr -d ' ')
    
    # If PID 1 is systemd, use systemd
    if [ "$pid1_process" = "systemd" ] || [ -d /run/systemd/system ]; then
        if command -v systemctl >/dev/null 2>&1; then
            # Additional verification that systemd is actually functioning
            if systemctl list-units >/dev/null 2>&1; then
                echo "systemd"
                return
            fi
        fi
    fi
    
    # Check for Gentoo OpenRC (PID 1 is openrc-init)
    if [ "$pid1_process" = "openrc-init" ]; then
        if command -v rc-service >/dev/null 2>&1; then
            echo "openrc"
            return
        fi
    fi
    
    # Check for other OpenRC systems (not Alpine, already handled)
    # Some systems use traditional init with OpenRC
    if [ "$pid1_process" = "init" ] && [ ! -f /etc/alpine-release ]; then
        # Check if OpenRC is actually managing services
        if [ -d /run/openrc ] && command -v rc-service >/dev/null 2>&1; then
            echo "openrc"
            return
        fi
        # Check for OpenRC files
        if [ -f /sbin/openrc ] && command -v rc-service >/dev/null 2>&1; then
            echo "openrc"
            return
        fi
    fi
    
    # Check for OpenWrt's procd
    if command -v uci >/dev/null 2>&1 && [ -f /etc/rc.common ]; then
        echo "procd"
        return
    fi
    
    # Check for macOS launchd
    if [ "$os_name" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
        echo "launchd"
        return
    fi
    
    # Fallback: if systemctl exists and appears functional, assume systemd
    if command -v systemctl >/dev/null 2>&1; then
        if systemctl list-units >/dev/null 2>&1; then
            echo "systemd"
            return
        fi
    fi
    
    # Last resort: check for OpenRC without other indicators
    if command -v rc-service >/dev/null 2>&1 && [ -d /etc/init.d ]; then
        echo "openrc"
        return
    fi

    # check for Upstart (CentOS 6)
    if command -v initctl >/dev/null 2>&1 && [ -d /etc/init ]; then
        echo "upstart"
        return
    fi
    
    echo "unknown"
}

init_system=$(detect_init_system)
log_info "Detected init system: ${GREEN}$init_system${NC}"

# Handle each init system
if [ "$init_system" = "nixos" ]; then
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
    log_warning "Service not started automatically on NixOS. Please rebuild your configuration."
elif [ "$init_system" = "openrc" ]; then
    # OpenRC service configuration
    log_info "Using OpenRC for service management"
    service_file="/etc/init.d/${service_name}"
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

    # Set permissions and enable service
    chmod +x "$service_file"
    rc-update add ${service_name} default
    rc-service ${service_name} start
    log_success "OpenRC service configured and started"
elif [ "$init_system" = "systemd" ]; then
    # Systemd service configuration
    log_info "Using systemd for service management"
    service_file="/etc/systemd/system/${service_name}.service"
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

    # Reload systemd and start service
    systemctl daemon-reload
    systemctl enable ${service_name}.service
    systemctl start ${service_name}.service
    log_success "Systemd service configured and started"
elif [ "$init_system" = "procd" ]; then
    # procd service configuration (OpenWrt)
    log_info "Using procd for service management"
    service_file="/etc/init.d/${service_name}"
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

    # Set permissions and enable service
    chmod +x "$service_file"
    /etc/init.d/${service_name} enable
    /etc/init.d/${service_name} start
    log_success "procd service configured and started"
elif [ "$init_system" = "launchd" ]; then
    # macOS launchd service configuration
    log_info "Using launchd for service management"
    
    # Determine if this should be a system or user service based on installation directory
    if [[ "$target_dir" =~ ^/Users/.* ]] || [ "$EUID" -ne 0 ]; then
        # User-level service (LaunchAgent)
        plist_dir="$HOME/Library/LaunchAgents"
        plist_file="$plist_dir/com.komari.${service_name}.plist"
        log_info "Installing as user-level service (LaunchAgent)"
        mkdir -p "$plist_dir"
        service_user="$(whoami)"
        log_dir="$HOME/Library/Logs"
    else
        # System-level service (LaunchDaemon)
        plist_dir="/Library/LaunchDaemons"
        plist_file="$plist_dir/com.komari.${service_name}.plist"
        log_info "Installing as system-level service (LaunchDaemon)"
        service_user="root"
        log_dir="/var/log"
    fi
    
    # Create the launchd plist file
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
    
    # Add program arguments if provided
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
    
    # Load and start the service
    if [[ "$target_dir" =~ ^/Users/.* ]] || [ "$EUID" -ne 0 ]; then
        # User-level service
        if launchctl bootstrap gui/$(id -u) "$plist_file"; then
            log_success "User-level launchd service configured and started"
        else
            log_error "Failed to load user-level launchd service"
            exit 1
        fi
    else
        # System-level service
        if launchctl bootstrap system "$plist_file"; then
            log_success "System-level launchd service configured and started"
        else
            log_error "Failed to load system-level launchd service"
            exit 1
        fi
    fi
elif [ "$init_system" = "upstart" ]; then
    # Upstart service configuration
    log_info "Using upstart for service management"
    service_file="/etc/init/${service_name}.conf"
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

# Start
script
    exec ${komari_agent_path} ${komari_service_args}
end script
EOF
    # enable Upstart unit
    initctl reload-configuration
    initctl start ${service_name}
    log_success "Upstart service configured and started"
else
    log_error "Unsupported or unknown init system detected: $init_system"
    log_error "Supported init systems: systemd, openrc, procd, launchd"
    exit 1
fi

echo ""
echo -e "${WHITE}===========================================${NC}"
if [ -f /etc/NIXOS ]; then
    log_success "Komari-agent binary installed!"
    log_warning "NixOS requires declarative service configuration."
    log_info "Please add the service configuration to your NixOS config and rebuild."
else
    log_success "Komari-agent installation completed!"
fi
log_config "Service: ${GREEN}$service_name${NC}"
log_config "Arguments: ${GREEN}$komari_service_args_log${NC}"
echo -e "${WHITE}===========================================${NC}"
