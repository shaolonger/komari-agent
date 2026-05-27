# Windows PowerShell installation script for Komari Agent

# Logging functions with colors
function Log-Info { param([string]$Message) Write-Host "$Message"    -ForegroundColor Cyan }
function Log-Success { param([string]$Message) Write-Host "$Message"    -ForegroundColor Green }
function Log-Warning { param([string]$Message) Write-Host "[WARNING] $Message"    -ForegroundColor Yellow }
function Log-Error { param([string]$Message) Write-Host "[ERROR] $Message"    -ForegroundColor Red }
function Log-Step { param([string]$Message) Write-Host "$Message"    -ForegroundColor Magenta }
function Log-Config { param([string]$Message) Write-Host "- $Message"    -ForegroundColor White }

function Format-KomariUrlForLog {
    param([string]$Value)

    if ([string]::IsNullOrWhiteSpace($Value) -or $Value -eq '(direct)') {
        return $Value
    }

    $redactedValue = $Value -replace '://[^/@\s]+:[^/@\s]+@', '://<redacted>@'
    return [System.Text.RegularExpressions.Regex]::Replace(
        $redactedValue,
        '([?&](?:token|key|secret|signature|sig|auth|password|access_token|client_secret)=)[^&#\s]+',
        '$1<redacted>',
        [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
    )
}

function Format-KomariArgValueForLog {
    param(
        [string]$Flag,
        [string]$Value
    )

    switch ($Flag) {
        '--token' { return '<redacted>' }
        '-t' { return '<redacted>' }
        '--auto-discovery' { return '<redacted>' }
        '--cf-access-client-secret' { return '<redacted>' }
        '--cf-access-client-id' { return '<redacted>' }
        '--endpoint' { return Format-KomariUrlForLog -Value $Value }
        '-e' { return Format-KomariUrlForLog -Value $Value }
        default { return $Value }
    }
}

function Format-KomariArgsForLog {
    param([string[]]$Arguments)

    $displayArgs = [System.Collections.Generic.List[string]]::new()
    for ($i = 0; $i -lt $Arguments.Count; $i++) {
        $arg = $Arguments[$i]
        switch -Regex ($arg) {
            '^--token$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^-t$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^--auto-discovery$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^--cf-access-client-secret$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^--cf-access-client-id$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^--endpoint$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^-e$' {
                $displayArgs.Add($arg)
                if ($i + 1 -lt $Arguments.Count) {
                    $displayArgs.Add((Format-KomariArgValueForLog -Flag $arg -Value $Arguments[$i + 1]))
                    $i++
                }
                continue
            }
            '^--token=' {
                $displayArgs.Add('--token=<redacted>')
                continue
            }
            '^-t=' {
                $displayArgs.Add('-t=<redacted>')
                continue
            }
            '^--auto-discovery=' {
                $displayArgs.Add('--auto-discovery=<redacted>')
                continue
            }
            '^--cf-access-client-secret=' {
                $displayArgs.Add('--cf-access-client-secret=<redacted>')
                continue
            }
            '^--cf-access-client-id=' {
                $displayArgs.Add('--cf-access-client-id=<redacted>')
                continue
            }
            '^--endpoint=' {
                $displayArgs.Add("--endpoint=$(Format-KomariArgValueForLog -Flag '--endpoint' -Value $arg.Substring('--endpoint='.Length))")
                continue
            }
            '^-e=' {
                $displayArgs.Add("-e=$(Format-KomariArgValueForLog -Flag '-e' -Value $arg.Substring('-e='.Length))")
                continue
            }
            default {
                $displayArgs.Add($arg)
            }
        }
    }

    return $displayArgs -join ' '
}

function Write-KomariConfigFile {
    param(
        [string]$Path,
        [string]$Token
    )

    if ([string]::IsNullOrWhiteSpace($Token)) {
        return
    }

    $configJson = @{ token = $Token.Trim() } | ConvertTo-Json -Depth 2
    [System.IO.File]::WriteAllText($Path, "$configJson`r`n", [System.Text.UTF8Encoding]::new($false))

    $icaclsOutput = & icacls $Path /inheritance:r /grant:r "Administrators:F" /grant:r "SYSTEM:F" 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to restrict config file ACLs: $icaclsOutput"
    }
}

function Get-KomariFileHash {
    param(
        [string]$Path,
        [ValidateSet('SHA1', 'SHA256')]
        [string]$Algorithm
    )

    return (Get-FileHash -Path $Path -Algorithm $Algorithm).Hash.ToLowerInvariant()
}

function Get-KomariChecksumValue {
    param([string]$Path)

    $line = Get-Content -Path $Path -TotalCount 1
    if ([string]::IsNullOrWhiteSpace($line)) {
        throw "Checksum file is empty: $Path"
    }

    $value = ($line -split '\s+')[0].Trim().ToLowerInvariant()
    if ([string]::IsNullOrWhiteSpace($value)) {
        throw "Checksum file does not contain a usable hash: $Path"
    }

    return $value
}

function Assert-KomariFileHash {
    param(
        [string]$Path,
        [ValidateSet('SHA1', 'SHA256')]
        [string]$Algorithm,
        [string]$ExpectedHash,
        [string]$Label
    )

    $actualHash = Get-KomariFileHash -Path $Path -Algorithm $Algorithm
    if ($actualHash -ne $ExpectedHash.ToLowerInvariant()) {
        throw "$Label hash verification failed"
    }
}

function Assert-TrustedGitHubProxy {
    param(
        [string]$Value,
        [switch]$TrustAcknowledged
    )

    if ([string]::IsNullOrWhiteSpace($Value)) {
        return $null
    }

    if (-not $TrustAcknowledged.IsPresent) {
        throw "Using --install-ghproxy requires --install-ghproxy-trusted. Only organization-controlled trusted HTTPS proxies are supported."
    }

    $uri = $null
    if (-not [System.Uri]::TryCreate($Value, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "--install-ghproxy must be an absolute https:// URL."
    }

    if ($uri.Scheme -ne 'https') {
        throw "--install-ghproxy must use https://."
    }

    if (-not [string]::IsNullOrWhiteSpace($uri.UserInfo)) {
        throw "--install-ghproxy must not include embedded credentials."
    }

    if (-not [string]::IsNullOrWhiteSpace($uri.Query) -or -not [string]::IsNullOrWhiteSpace($uri.Fragment)) {
        throw "--install-ghproxy must not include query strings or fragments."
    }

    return $uri.GetLeftPart([System.UriPartial]::Path).TrimEnd('/')
}

# Default parameters
$InstallDir = Join-Path $Env:ProgramFiles "Komari"
$ServiceName = "komari-agent"
$GitHubProxy = ""
$GitHubProxyTrusted = $false
$KomariArgs = @()
$TokenValue = ""
$HasExplicitConfig = $false
$InstallVersion = ""

# Parse script arguments
for ($i = 0; $i -lt $args.Count; $i++) {
    switch ($args[$i]) {
        "--install-dir" { $InstallDir = $args[$i + 1]; $i++; continue }
        "--install-service-name" { $ServiceName = $args[$i + 1]; $i++; continue }
        "--install-ghproxy" {
            if ($i + 1 -ge $args.Count) {
                Log-Error "Missing value for --install-ghproxy"
                exit 1
            }
            $GitHubProxy = $args[$i + 1]
            $i++
            continue
        }
        "--install-ghproxy-trusted" { $GitHubProxyTrusted = $true; continue }
        "--install-version" { $InstallVersion = $args[$i + 1]; $i++; continue }
        "--token" {
            if ($i + 1 -ge $args.Count) {
                Log-Error "Missing value for --token"
                exit 1
            }
            $TokenValue = $args[$i + 1]
            $i++
            continue
        }
        "-t" {
            if ($i + 1 -ge $args.Count) {
                Log-Error "Missing value for -t"
                exit 1
            }
            $TokenValue = $args[$i + 1]
            $i++
            continue
        }
        "--config" {
            if ($i + 1 -ge $args.Count) {
                Log-Error "Missing value for --config"
                exit 1
            }
            $HasExplicitConfig = $true
            $KomariArgs += $args[$i]
            $KomariArgs += $args[$i + 1]
            $i++
            continue
        }
        Default {
            if ($args[$i] -like '--token=*') {
                $TokenValue = $args[$i].Substring('--token='.Length)
                continue
            }
            if ($args[$i] -like '-t=*') {
                $TokenValue = $args[$i].Substring('-t='.Length)
                continue
            }
            if ($args[$i] -like '--config=*') {
                $HasExplicitConfig = $true
            }
            $KomariArgs += $args[$i]
        }
    }
}

if (-not [string]::IsNullOrWhiteSpace($TokenValue) -and $HasExplicitConfig) {
    Log-Error "Cannot combine --token with an explicit --config. Remove --config and let the installer generate a protected config file."
    exit 1
}

if (-not [string]::IsNullOrWhiteSpace($GitHubProxy)) {
    try {
        $GitHubProxy = Assert-TrustedGitHubProxy -Value $GitHubProxy -TrustAcknowledged:$GitHubProxyTrusted
    }
    catch {
        Log-Error $_.Exception.Message
        exit 1
    }
    Log-Warning "Using --install-ghproxy only with an organization-controlled HTTPS proxy that mirrors GitHub release binaries and .sha256 assets without modification."
}

# Ensure running as Administrator
if (-not ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()
    ).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)) {
    Log-Error "Please run this script as Administrator."
    exit 1
}

# Prepare GitHub proxy display
if ($GitHubProxy -ne '') { $ProxyDisplay = Format-KomariUrlForLog -Value $GitHubProxy } else { $ProxyDisplay = '(direct)' }
$ConfigFile = Join-Path $InstallDir 'komari-agent.json'
$LegacyTokenFile = Join-Path $InstallDir 'komari-agent.token'
if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
    $KomariArgs += "--config=`"$ConfigFile`""
}
$RedactedArgString = Format-KomariArgsForLog -Arguments $KomariArgs

# Detect architecture early for constructing binary name
switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    'x86' { $arch = '386' }
    Default { Log-Error "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE"; exit 1 }
}

# Ensure installation directory exists for nssm and agent
Log-Step "Ensuring installation directory exists: $InstallDir"
New-Item -ItemType Directory -Path $InstallDir -Force -ErrorAction SilentlyContinue | Out-Null # Ensure $InstallDir exists
$NssmReleaseSha1 = 'be7b3577c6e3a280e5106a9e9db5b3775931cefc'

# Check for nssm and download if not present
$nssmExeToUse = Join-Path $InstallDir "nssm.exe"

# First, check if nssm is in PATH and is functional
$nssmCmd = Get-Command nssm -ErrorAction SilentlyContinue
if ($nssmCmd) {
    Log-Info "nssm found in PATH at $($nssmCmd.Source)."
    try {
        $nssmVersionOutput = nssm version 2>&1
        Log-Info "Detected nssm version: $nssmVersionOutput"
    }
    catch {
        Log-Warning "nssm found in PATH failed to execute 'nssm version'. Will attempt to use/download local copy. Error: $_"
        $nssmCmd = $null # Force re-evaluation for local copy or download
    }
}

# If nssm not found in PATH or the one in PATH failed, check local $InstallDir
if (-not $nssmCmd) {
    if (Test-Path $nssmExeToUse) {
        Log-Info "nssm found at $nssmExeToUse. Attempting to use it by adding $InstallDir to PATH."
        $env:Path = "$($InstallDir);$($env:Path)"
        $nssmCmd = Get-Command nssm -ErrorAction SilentlyContinue
        if ($nssmCmd) {
            try {
                $nssmVersionOutput = nssm version 2>&1
            }
            catch {
                Log-Warning "nssm from $InstallDir failed to execute 'nssm version'. Error: $_"
                $nssmCmd = $null # Mark as unusable
            }
        }
        else {
            Log-Warning "Failed to make nssm from $nssmExeToUse available via PATH. Will attempt download."
        }
    }
}

# If still no usable nssm command, proceed to download
if (-not $nssmCmd) {
    Log-Info "nssm not found or not usable. Attempting to download to $InstallDir..."
    $NssmVersion = "2.24"
    $NssmZipUrl = "https://nssm.cc/release/nssm-$NssmVersion.zip"
    $TempNssmZipPath = Join-Path $env:TEMP "nssm-$NssmVersion-$PID.zip"
    $TempExtractDir = Join-Path $env:TEMP "nssm_extract_temp_$PID"

    try {
        Log-Info "Downloading nssm from $NssmZipUrl..."
        Invoke-WebRequest -Uri $NssmZipUrl -OutFile $TempNssmZipPath -UseBasicParsing

        Log-Step "Verifying nssm archive hash..."
        Assert-KomariFileHash -Path $TempNssmZipPath -Algorithm SHA1 -ExpectedHash $NssmReleaseSha1 -Label "nssm-$NssmVersion.zip"

        if (Test-Path $TempExtractDir) { Remove-Item -Recurse -Force $TempExtractDir }
        New-Item -ItemType Directory -Path $TempExtractDir -Force | Out-Null
        Expand-Archive -Path $TempNssmZipPath -DestinationPath $TempExtractDir -Force
        
        $NssmSourceDirInsideZip = "nssm-$NssmVersion" # Used for Get-ChildItem search path
        # The path part within the extracted nssm folder, e.g., "nssm-2.24\win32"
        # 'win32' nssm is used for both 'amd64' and 'arm64' PowerShell architectures.
        $NssmArchSubDir = Join-Path "nssm-$NssmVersion" "win32"
        $NssmSourceExePath = Join-Path (Join-Path $TempExtractDir $NssmArchSubDir) "nssm.exe"

        if (-not (Test-Path $NssmSourceExePath)) {
            Log-Error "Could not find nssm.exe at expected path: $NssmSourceExePath after extraction."
            # Fallback search for nssm.exe within the extracted directory
            $foundNssmFallback = Get-ChildItem -Path $TempExtractDir -Recurse -Filter "nssm.exe" | 
            Where-Object { $_.FullName -like "*$NssmArchSubDir\nssm.exe" } | 
            Select-Object -First 1
            if ($foundNssmFallback) {
                Log-Warning "Found nssm.exe at $($foundNssmFallback.FullName) using fallback search. Using this."
                $NssmSourceExePath = $foundNssmFallback.FullName
            }
            else {
                Log-Error "nssm.exe ($NssmArchSubDir) still not found in $TempExtractDir. Please install nssm manually (from https://nssm.cc) and ensure it's in your PATH."
                exit 1
            }
        }
        
        Copy-Item -Path $NssmSourceExePath -Destination $nssmExeToUse -Force

        $env:Path = "$($InstallDir);$($env:Path)"
        $nssmCmd = Get-Command nssm -ErrorAction SilentlyContinue # Re-check after adding to PATH
        if ($nssmCmd) {
            Log-Success "Downloaded nssm is now configured and available in PATH."
        }
        else {
            Log-Error "Failed to configure downloaded nssm in PATH from $nssmExeToUse. Please ensure $InstallDir is in your system PATH or nssm is installed globally."
            exit 1
        }
    }
    catch {
        Log-Error "Failed to download or configure nssm: $_"
        Log-Error "Please install nssm manually from https://nssm.cc and ensure nssm.exe is in your PATH."
        exit 1
    }
    finally {
        if (Test-Path $TempNssmZipPath) { Remove-Item $TempNssmZipPath -Force -ErrorAction SilentlyContinue }
        if (Test-Path $TempExtractDir) { Remove-Item $TempExtractDir -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

# Final check that nssm is operational
try {
    $nssmVersionOutput = nssm version 2>&1
}
catch {
    Log-Error "nssm command failed to execute even after setup attempts. Please check the nssm installation and PATH. Error: $_"
    exit 1
}

Log-Step "Installation configuration:"
Log-Config "Service name: $ServiceName"
Log-Config "Install directory: $InstallDir"
Log-Config "GitHub proxy: $ProxyDisplay"
Log-Config "Agent arguments: $RedactedArgString"
if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
    Log-Config "Config file: $ConfigFile"
}
if ($InstallVersion -ne "") {
    Log-Config "Specified agent version: $InstallVersion"
} else {
    Log-Config "Agent version: Latest"
}

# Paths
$BinaryName = "komari-agent-windows-$arch.exe"
$AgentPath = Join-Path $InstallDir "komari-agent.exe"

# Uninstall previous service and binary
function Uninstall-Previous {
    Log-Step "Checking for existing service..."
    # Check if service exists using nssm status, as Get-Service might not work for nssm services if not properly registered
    $serviceStatus = nssm status $ServiceName 2>&1
    if ($serviceStatus -notmatch "SERVICE_STOPPED" -and $serviceStatus -notmatch "does not exist") {
        Log-Info "Stopping service $ServiceName..."
        nssm stop $ServiceName 2>&1 | Out-Null
    }
    # Attempt to remove the service using nssm
    # We check if it exists first by trying to get its status.
    # nssm remove will succeed if the service exists, and fail otherwise.
    # We add confirm to avoid interactive prompts.
    $removeOutput = nssm remove $ServiceName confirm 2>&1
    if ($LASTEXITCODE -eq 0) {
    }
    elseif ($removeOutput -match "Can't open service! (The specified service does not exist as an installed service.)" -or $removeOutput -match "No such service" -or $removeOutput -match "does not exist") {
        Log-Info "Service $ServiceName does not exist or was already removed."
    }
    else {
        # If nssm remove fails for other reasons, try sc.exe delete as a fallback for older installations
        $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($svc) {
            Stop-Service $ServiceName -Force -ErrorAction SilentlyContinue
            sc.exe delete $ServiceName | Out-Null
        }
    }

    if (Test-Path $AgentPath) {
        Log-Warning "Existing binary will be replaced after checksum verification."
    }

    if (Test-Path $ConfigFile) {
        Log-Warning "Removing old config file..."
        Remove-Item $ConfigFile -Force
    }

    if (Test-Path $LegacyTokenFile) {
        Log-Warning "Removing old token file..."
        Remove-Item $LegacyTokenFile -Force
    }
}
Uninstall-Previous

$versionToInstall = ""
if ($InstallVersion -ne "") {
    Log-Info "Attempting to install specified version: $InstallVersion"
    $versionToInstall = $InstallVersion
}
else {
    $ApiUrl = "https://api.github.com/repos/shaolonger/komari-agent/releases/latest"
    try {
        Log-Step "Fetching latest release version from GitHub API..."
        $release = Invoke-RestMethod -Uri $ApiUrl -UseBasicParsing
        $versionToInstall = $release.tag_name
        Log-Success "Latest version fetched: $versionToInstall"
    }
    catch {
        Log-Error "Failed to fetch latest version: $_"
        exit 1
    }
}
Log-Success "Installing Komari Agent version: $versionToInstall"

# Construct download URL
$BinaryName = "komari-agent-windows-$arch.exe"
$DownloadUrl = if ($GitHubProxy) { "$GitHubProxy/https://github.com/shaolonger/komari-agent/releases/download/$versionToInstall/$BinaryName" } else { "https://github.com/shaolonger/komari-agent/releases/download/$versionToInstall/$BinaryName" }
$ChecksumUrl = "$DownloadUrl.sha256"
$AgentDownloadTempPath = Join-Path $InstallDir "komari-agent.exe.download.$PID"
$AgentChecksumTempPath = "$AgentDownloadTempPath.sha256"

# Download and install
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
Log-Info "URL: $(Format-KomariUrlForLog -Value $DownloadUrl)"
try {
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $AgentDownloadTempPath -UseBasicParsing

    Log-Info "Checksum URL: $(Format-KomariUrlForLog -Value $ChecksumUrl)"
    Invoke-WebRequest -Uri $ChecksumUrl -OutFile $AgentChecksumTempPath -UseBasicParsing

    Log-Step "Verifying agent SHA256 checksum..."
    $expectedAgentHash = Get-KomariChecksumValue -Path $AgentChecksumTempPath
    Assert-KomariFileHash -Path $AgentDownloadTempPath -Algorithm SHA256 -ExpectedHash $expectedAgentHash -Label $BinaryName

    Move-Item -Path $AgentDownloadTempPath -Destination $AgentPath -Force
}
catch {
    Log-Error "Download or verification failed: $_"
    exit 1
}
finally {
    if (Test-Path $AgentDownloadTempPath) { Remove-Item $AgentDownloadTempPath -Force -ErrorAction SilentlyContinue }
    if (Test-Path $AgentChecksumTempPath) { Remove-Item $AgentChecksumTempPath -Force -ErrorAction SilentlyContinue }
}
Log-Success "Downloaded and saved to $AgentPath"

if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
    Log-Step "Writing service config file..."
    try {
        Write-KomariConfigFile -Path $ConfigFile -Token $TokenValue
    }
    catch {
        Log-Error "Failed to write config file: $_"
        exit 1
    }
    Log-Success "Service config stored at $ConfigFile"
}

# Register and start service
Log-Step "Configuring Windows service with nssm..."
$argString = $KomariArgs -join ' '
# Ensure InstallDir and AgentPath are quoted if they contain spaces
$quotedAgentPath = "`"$AgentPath`""
nssm install $ServiceName $quotedAgentPath $argString
# Set display name and startup type using nssm
nssm set $ServiceName DisplayName "Komari Agent Service"
nssm set $ServiceName Start SERVICE_AUTO_START
nssm set $ServiceName AppExit Default Restart
nssm set $ServiceName AppRestartDelay 5000
# Start the service using nssm
nssm start $ServiceName
Log-Success "Service $ServiceName installed and started using nssm."

Log-Success "Komari Agent installation completed!"
Log-Config "Service name: $ServiceName"
Log-Config "Arguments: $RedactedArgString"