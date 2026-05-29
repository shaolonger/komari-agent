# Windows PowerShell installation script for Komari Agent

param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$InstallerArgs
)

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
$EffectiveKomariArgs = @()
$TokenValue = ""
$HasExplicitConfig = $false
$ExplicitConfigPath = ""
$InstallVersion = ""
$Operation = ""
$AssumeYes = $false
$PurgeConfig = $false
$ProxyDisplay = '(direct)'
$ConfigFile = ''
$LegacyTokenFile = ''
$AgentPath = ''
$AgentLogPath = ''
$RedactedArgString = ''
$ServiceArgString = ''
$BinaryName = ''
$DownloadUrl = ''
$ChecksumUrl = ''
$AgentDownloadTempPath = ''
$AgentChecksumTempPath = ''
$VersionToInstall = ''
$ReleaseRepository = 'shaolonger/komari-agent'
$OriginalArgs = @($InstallerArgs)

function Show-Banner {
    Write-Host "===========================================" -ForegroundColor White
    Write-Host "     Komari Agent Management Script      " -ForegroundColor White
    Write-Host "===========================================" -ForegroundColor White
    Write-Host ""
}

function Show-Usage {
    Show-Banner
    @"
用法:
  .\install.ps1                           打开交互菜单
  .\install.ps1 --install [agent flags]   首次安装 Agent
  .\install.ps1 --upgrade                 升级 Agent 二进制并重启服务
  .\install.ps1 --reconfigure [flags]     重建 Agent 配置与服务定义
  .\install.ps1 --uninstall               卸载 Agent 服务与二进制
  .\install.ps1 --status                  查看 Agent 服务状态
  .\install.ps1 --logs                    查看 Agent 服务日志
  .\install.ps1 --restart                 重启 Agent 服务
  .\install.ps1 --stop                    停止 Agent 服务

常用安装参数:
  --install-dir PATH
  --install-service-name NAME
  --install-version TAG
  --install-ghproxy URL --install-ghproxy-trusted
  --purge-config        卸载时额外删除配置文件
  --yes                 跳过卸载确认

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
"@ | Write-Host
}

function ConvertTo-PlainText {
    param([Security.SecureString]$SecureString)

    if (-not $SecureString) {
        return ''
    }

    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($SecureString)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    }
    finally {
        if ($bstr -ne [IntPtr]::Zero) {
            [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
        }
    }
}

function Prompt-YesNo {
    param(
        [string]$Prompt,
        [bool]$Default = $false
    )

    while ($true) {
        $suffix = if ($Default) { '[Y/n]' } else { '[y/N]' }
        $answer = Read-Host "$Prompt $suffix"
        if ([string]::IsNullOrWhiteSpace($answer)) {
            return $Default
        }

        switch ($answer.ToLowerInvariant()) {
            'y' { return $true }
            'yes' { return $true }
            'n' { return $false }
            'no' { return $false }
            default { Log-Error '请输入 y 或 n。' }
        }
    }
}

function Prompt-WithDefault {
    param(
        [string]$Prompt,
        [string]$DefaultValue
    )

    $answer = Read-Host "$Prompt [$DefaultValue]"
    if ([string]::IsNullOrWhiteSpace($answer)) {
        return $DefaultValue
    }

    return $answer
}

function Convert-ArgsToServiceString {
    param([string[]]$Arguments)

    $escaped = foreach ($arg in $Arguments) {
        if ($arg -match '[\s"]') {
            '"' + ($arg -replace '"', '\"') + '"'
        }
        else {
            $arg
        }
    }

    return ($escaped -join ' ')
}

function Update-DerivedValues {
    $script:ConfigFile = Join-Path $InstallDir 'komari-agent.json'
    $script:LegacyTokenFile = Join-Path $InstallDir 'komari-agent.token'
    $script:AgentPath = Join-Path $InstallDir 'komari-agent.exe'
    $script:AgentLogPath = Join-Path $InstallDir 'komari-agent.log'
    $script:ProxyDisplay = if ([string]::IsNullOrWhiteSpace($GitHubProxy)) { '(direct)' } else { Format-KomariUrlForLog -Value $GitHubProxy }

    $script:EffectiveKomariArgs = @($KomariArgs)
    if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
        $script:EffectiveKomariArgs += '--config'
        $script:EffectiveKomariArgs += $script:ConfigFile
    }

    $script:RedactedArgString = Format-KomariArgsForLog -Arguments $script:EffectiveKomariArgs
    $script:ServiceArgString = Convert-ArgsToServiceString -Arguments $script:EffectiveKomariArgs
}

function Parse-InstallerArguments {
    for ($i = 0; $i -lt $InstallerArgs.Count; $i++) {
        switch ($InstallerArgs[$i]) {
            '--install' { $script:Operation = 'install'; continue }
            '--upgrade' { $script:Operation = 'upgrade'; continue }
            '--reconfigure' { $script:Operation = 'reconfigure'; continue }
            '--uninstall' { $script:Operation = 'uninstall'; continue }
            '--status' { $script:Operation = 'status'; continue }
            '--logs' { $script:Operation = 'logs'; continue }
            '--restart' { $script:Operation = 'restart'; continue }
            '--stop' { $script:Operation = 'stop'; continue }
            '--menu' { $script:Operation = 'menu'; continue }
            '--help' { $script:Operation = 'help'; continue }
            '-h' { $script:Operation = 'help'; continue }
            '--yes' { $script:AssumeYes = $true; continue }
            '-y' { $script:AssumeYes = $true; continue }
            '--purge-config' { $script:PurgeConfig = $true; continue }
            '--install-dir' { $script:InstallDir = $InstallerArgs[$i + 1]; $i++; continue }
            '--install-service-name' { $script:ServiceName = $InstallerArgs[$i + 1]; $i++; continue }
            '--install-ghproxy' {
                if ($i + 1 -ge $InstallerArgs.Count) {
                    Log-Error 'Missing value for --install-ghproxy'
                    exit 1
                }
                $script:GitHubProxy = $InstallerArgs[$i + 1]
                $i++
                continue
            }
            '--install-ghproxy-trusted' { $script:GitHubProxyTrusted = $true; continue }
            '--install-version' { $script:InstallVersion = $InstallerArgs[$i + 1]; $i++; continue }
            '--token' {
                if ($i + 1 -ge $InstallerArgs.Count) {
                    Log-Error 'Missing value for --token'
                    exit 1
                }
                $script:TokenValue = $InstallerArgs[$i + 1]
                $i++
                continue
            }
            '-t' {
                if ($i + 1 -ge $InstallerArgs.Count) {
                    Log-Error 'Missing value for -t'
                    exit 1
                }
                $script:TokenValue = $InstallerArgs[$i + 1]
                $i++
                continue
            }
            '--config' {
                if ($i + 1 -ge $InstallerArgs.Count) {
                    Log-Error 'Missing value for --config'
                    exit 1
                }
                $script:HasExplicitConfig = $true
                $script:ExplicitConfigPath = $InstallerArgs[$i + 1]
                $script:KomariArgs += $InstallerArgs[$i]
                $script:KomariArgs += $InstallerArgs[$i + 1]
                $i++
                continue
            }
            default {
                if ($InstallerArgs[$i] -like '--token=*') {
                    $script:TokenValue = $InstallerArgs[$i].Substring('--token='.Length)
                    continue
                }
                if ($InstallerArgs[$i] -like '-t=*') {
                    $script:TokenValue = $InstallerArgs[$i].Substring('-t='.Length)
                    continue
                }
                if ($InstallerArgs[$i] -like '--config=*') {
                    $script:HasExplicitConfig = $true
                    $script:ExplicitConfigPath = $InstallerArgs[$i].Substring('--config='.Length)
                }
                $script:KomariArgs += $InstallerArgs[$i]
            }
        }
    }

    if ([string]::IsNullOrWhiteSpace($Operation)) {
        if ($OriginalArgs.Count -eq 0) {
            $script:Operation = 'menu'
        }
        else {
            $script:Operation = 'install'
        }
    }
}

function Validate-InstallerArguments {
    if (-not [string]::IsNullOrWhiteSpace($TokenValue) -and $HasExplicitConfig) {
        Log-Error 'Cannot combine --token with an explicit --config. Remove --config and let the installer generate a protected config file.'
        exit 1
    }

    if (-not [string]::IsNullOrWhiteSpace($GitHubProxy)) {
        try {
            $script:GitHubProxy = Assert-TrustedGitHubProxy -Value $GitHubProxy -TrustAcknowledged:$GitHubProxyTrusted
        }
        catch {
            Log-Error $_.Exception.Message
            exit 1
        }
        Log-Warning 'Using --install-ghproxy only with an organization-controlled HTTPS proxy that mirrors GitHub release binaries and .sha256 assets without modification.'
    }

    Update-DerivedValues
}

function Operation-RequiresAdministrator {
    return $Operation -ne 'help'
}

Parse-InstallerArguments
Validate-InstallerArguments

if (Operation-RequiresAdministrator) {
    if (-not ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)) {
        Log-Error 'Please run this script as Administrator.'
        exit 1
    }
}

function Show-OperationConfiguration {
    param([string]$Heading)

    Show-Banner
    Log-Config $Heading
    Log-Config "Service name: $ServiceName"
    Log-Config "Install directory: $InstallDir"
    Log-Config "GitHub proxy: $ProxyDisplay"
    if (-not [string]::IsNullOrWhiteSpace($RedactedArgString)) {
        Log-Config "Agent arguments: $RedactedArgString"
    }
    if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
        Log-Config "Config file: $ConfigFile"
    }
    elseif ($HasExplicitConfig) {
        Log-Config "Config file: $ExplicitConfigPath"
    }
    Log-Config "Target version: $(if ($InstallVersion) { $InstallVersion } else { 'Latest' })"
    Write-Host ""
}

function Resolve-Architecture {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        'x86' { return '386' }
        default {
            throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE"
        }
    }
}

function Ensure-NssmAvailable {
    Log-Step "Ensuring installation directory exists: $InstallDir"
    New-Item -ItemType Directory -Path $InstallDir -Force -ErrorAction SilentlyContinue | Out-Null

    $nssmReleaseSha1 = 'be7b3577c6e3a280e5106a9e9db5b3775931cefc'
    $nssmCiVersion = '2.24-101-g897c7ad'
    $nssmCiSha256 = '99f5045fffbffb745d67fe3a065a953c4a3d9c253b868892d9b685b0ee7d07b8'
    $nssmExeToUse = Join-Path $InstallDir 'nssm.exe'
    $nssmCmd = Get-Command nssm -ErrorAction SilentlyContinue

    if ($nssmCmd) {
        try {
            $null = nssm version 2>&1
            return
        }
        catch {
            $nssmCmd = $null
        }
    }

    if (-not $nssmCmd -and (Test-Path $nssmExeToUse)) {
        $env:Path = "$InstallDir;$($env:Path)"
        $nssmCmd = Get-Command nssm -ErrorAction SilentlyContinue
        if ($nssmCmd) {
            try {
                $null = nssm version 2>&1
                return
            }
            catch {
                $nssmCmd = $null
            }
        }
    }

    if (-not $nssmCmd) {
        Log-Info "nssm not found or not usable. Attempting to download to $InstallDir..."
        $nssmVersion = '2.24'
        $nssmZipUrl = "https://nssm.cc/release/nssm-$nssmVersion.zip"
        $nssmCiZipUrl = "https://nssm.cc/ci/nssm-$nssmCiVersion.zip"
        $nssmArchiveRoot = "nssm-$nssmVersion"
        $tempNssmZipPath = Join-Path $env:TEMP "nssm-$nssmVersion-$PID.zip"
        $tempExtractDir = Join-Path $env:TEMP "nssm_extract_temp_$PID"

        try {
            try {
                Invoke-WebRequest -Uri $nssmZipUrl -OutFile $tempNssmZipPath -UseBasicParsing
                Assert-KomariFileHash -Path $tempNssmZipPath -Algorithm SHA1 -ExpectedHash $nssmReleaseSha1 -Label "nssm-$nssmVersion.zip"
            }
            catch {
                if (Test-Path $tempNssmZipPath) { Remove-Item $tempNssmZipPath -Force -ErrorAction SilentlyContinue }
                Log-Warning "Failed to download or verify the stable NSSM release from $nssmZipUrl. Trying the CI build referenced by the winget manifest..."
                Invoke-WebRequest -Uri $nssmCiZipUrl -OutFile $tempNssmZipPath -UseBasicParsing
                Assert-KomariFileHash -Path $tempNssmZipPath -Algorithm SHA256 -ExpectedHash $nssmCiSha256 -Label "nssm-$nssmCiVersion.zip"
                $nssmArchiveRoot = "nssm-$nssmCiVersion"
            }

            if (Test-Path $tempExtractDir) { Remove-Item -Recurse -Force $tempExtractDir }
            New-Item -ItemType Directory -Path $tempExtractDir -Force | Out-Null
            Expand-Archive -Path $tempNssmZipPath -DestinationPath $tempExtractDir -Force

            $nssmSourceExePath = Join-Path (Join-Path $tempExtractDir (Join-Path $nssmArchiveRoot 'win32')) 'nssm.exe'
            if (-not (Test-Path $nssmSourceExePath)) {
                $fallback = Get-ChildItem -Path $tempExtractDir -Recurse -Filter 'nssm.exe' | Select-Object -First 1
                if (-not $fallback) {
                    throw 'nssm.exe was not found after extraction.'
                }
                $nssmSourceExePath = $fallback.FullName
            }

            Copy-Item -Path $nssmSourceExePath -Destination $nssmExeToUse -Force
            $env:Path = "$InstallDir;$($env:Path)"
        }
        catch {
            Log-Error "Failed to download or configure nssm: $_"
            Log-Error 'Please install nssm manually from https://nssm.cc and ensure nssm.exe is in your PATH.'
            exit 1
        }
        finally {
            if (Test-Path $tempNssmZipPath) { Remove-Item $tempNssmZipPath -Force -ErrorAction SilentlyContinue }
            if (Test-Path $tempExtractDir) { Remove-Item $tempExtractDir -Recurse -Force -ErrorAction SilentlyContinue }
        }
    }

    try {
        $null = nssm version 2>&1
    }
    catch {
        Log-Error "nssm command failed to execute even after setup attempts. Please check the nssm installation and PATH. Error: $_"
        exit 1
    }
}

function Resolve-ReleaseVersion {
    if (-not [string]::IsNullOrWhiteSpace($InstallVersion)) {
        $script:VersionToInstall = $InstallVersion
        return
    }

    $apiUrl = "https://api.github.com/repos/$ReleaseRepository/releases/latest"
    try {
        Log-Step 'Fetching latest release version from GitHub API...'
        $release = Invoke-RestMethod -Uri $apiUrl -UseBasicParsing
        $script:VersionToInstall = $release.tag_name
        Log-Success "Latest version fetched: $script:VersionToInstall"
    }
    catch {
        Log-Error "No published GitHub release is available in $ReleaseRepository. Publish a release with binary and .sha256 assets, or rerun with --install-version for an existing release tag."
        exit 1
    }
}

function Prepare-DownloadContext {
    Ensure-NssmAvailable
    $script:arch = Resolve-Architecture
    Log-Info "Detected architecture: $script:arch"
    Resolve-ReleaseVersion
    $script:BinaryName = "komari-agent-windows-$script:arch.exe"
    $script:DownloadUrl = if ($GitHubProxy) { "$GitHubProxy/https://github.com/$ReleaseRepository/releases/download/$script:VersionToInstall/$script:BinaryName" } else { "https://github.com/$ReleaseRepository/releases/download/$script:VersionToInstall/$script:BinaryName" }
    $script:ChecksumUrl = "$script:DownloadUrl.sha256"
    $script:AgentDownloadTempPath = Join-Path $InstallDir "komari-agent.exe.download.$PID"
    $script:AgentChecksumTempPath = "$script:AgentDownloadTempPath.sha256"
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

function Download-AgentBinary {
    Log-Info "URL: $(Format-KomariUrlForLog -Value $DownloadUrl)"
    try {
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $AgentDownloadTempPath -UseBasicParsing
        Log-Info "Checksum URL: $(Format-KomariUrlForLog -Value $ChecksumUrl)"
        Invoke-WebRequest -Uri $ChecksumUrl -OutFile $AgentChecksumTempPath -UseBasicParsing

        $expectedAgentHash = Get-KomariChecksumValue -Path $AgentChecksumTempPath
        Assert-KomariFileHash -Path $AgentDownloadTempPath -Algorithm SHA256 -ExpectedHash $expectedAgentHash -Label $BinaryName
    }
    catch {
        if (Test-Path $AgentDownloadTempPath) { Remove-Item $AgentDownloadTempPath -Force -ErrorAction SilentlyContinue }
        if (Test-Path $AgentChecksumTempPath) { Remove-Item $AgentChecksumTempPath -Force -ErrorAction SilentlyContinue }
        Log-Error "Download or verification failed. Ensure $ReleaseRepository release $VersionToInstall includes $BinaryName and $BinaryName.sha256."
        throw
    }
}

function Replace-DownloadedBinary {
    try {
        Move-Item -Path $AgentDownloadTempPath -Destination $AgentPath -Force
    }
    finally {
        if (Test-Path $AgentDownloadTempPath) { Remove-Item $AgentDownloadTempPath -Force -ErrorAction SilentlyContinue }
        if (Test-Path $AgentChecksumTempPath) { Remove-Item $AgentChecksumTempPath -Force -ErrorAction SilentlyContinue }
    }
    Log-Success "Downloaded and saved to $AgentPath"
}

function Test-ArgsContainEndpoint {
    foreach ($arg in $KomariArgs) {
        if ($arg -eq '--endpoint' -or $arg -eq '-e' -or $arg -like '--endpoint=*' -or $arg -like '-e=*') {
            return $true
        }
    }
    return $false
}

function Test-ConfigHasEndpoint {
    param([string]$Path)
    return [bool](Select-String -Path $Path -Pattern '"endpoint"\s*:' -Quiet -ErrorAction SilentlyContinue)
}

function Ensure-InstallInputs {
    if (-not [string]::IsNullOrWhiteSpace($TokenValue) -and -not (Test-ArgsContainEndpoint)) {
        Log-Error 'When generating a config from --token, you must also provide --endpoint.'
        exit 1
    }

    if ($HasExplicitConfig) {
        if (-not (Test-Path -LiteralPath $ExplicitConfigPath -PathType Leaf)) {
            Log-Error "The specified config file does not exist: $ExplicitConfigPath"
            exit 1
        }
        if (-not (Test-ArgsContainEndpoint) -and -not (Test-ConfigHasEndpoint -Path $ExplicitConfigPath)) {
            Log-Error 'The selected config file does not contain an endpoint, and no --endpoint flag was provided.'
            exit 1
        }
        return
    }

    if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
        return
    }

    if (Test-Path -LiteralPath $ConfigFile -PathType Leaf) {
        $script:HasExplicitConfig = $true
        $script:ExplicitConfigPath = $ConfigFile
        $script:KomariArgs += '--config'
        $script:KomariArgs += $ConfigFile
        Update-DerivedValues
        if (-not (Test-ArgsContainEndpoint) -and -not (Test-ConfigHasEndpoint -Path $ConfigFile)) {
            Log-Error 'The default config file does not contain an endpoint, and no --endpoint flag was provided.'
            exit 1
        }
        return
    }

    Log-Error 'No usable agent config was found. Provide --config, or provide --endpoint with --token, or use the interactive install menu.'
    exit 1
}

function Write-GeneratedConfigIfNeeded {
    if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
        Log-Step 'Writing service config file...'
        try {
            Write-KomariConfigFile -Path $ConfigFile -Token $TokenValue
        }
        catch {
            Log-Error "Failed to write config file: $_"
            exit 1
        }
        Log-Success "Service config stored at $ConfigFile"
    }
}

function Service-Exists {
    return $null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)
}

function Stop-RegisteredService {
    if (Service-Exists) {
        nssm stop $ServiceName 2>&1 | Out-Null
    }
}

function Start-RegisteredService {
    if (Service-Exists) {
        nssm start $ServiceName 2>&1 | Out-Null
    }
}

function Restart-RegisteredService {
    if (-not (Service-Exists)) {
        throw "Service $ServiceName does not exist."
    }
    nssm restart $ServiceName 2>&1 | Out-Null
}

function Remove-ServiceRegistration {
    if (-not (Service-Exists)) {
        return
    }

    $null = nssm stop $ServiceName 2>&1
    $removeOutput = nssm remove $ServiceName confirm 2>&1
    if ($LASTEXITCODE -ne 0 -and $removeOutput -notmatch 'does not exist') {
        $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($svc) {
            Stop-Service $ServiceName -Force -ErrorAction SilentlyContinue
            sc.exe delete $ServiceName | Out-Null
        }
    }
}

function Configure-Service {
    Log-Step 'Configuring Windows service with nssm...'
    Remove-ServiceRegistration
    nssm install $ServiceName $AgentPath | Out-Null
    nssm set $ServiceName AppParameters $ServiceArgString | Out-Null
    nssm set $ServiceName AppDirectory $InstallDir | Out-Null
    nssm set $ServiceName DisplayName 'Komari Agent Service' | Out-Null
    nssm set $ServiceName Start SERVICE_AUTO_START | Out-Null
    nssm set $ServiceName AppExit Default Restart | Out-Null
    nssm set $ServiceName AppRestartDelay 5000 | Out-Null
    nssm set $ServiceName AppStdout $AgentLogPath | Out-Null
    nssm set $ServiceName AppStderr $AgentLogPath | Out-Null
    nssm set $ServiceName AppRotateFiles 1 | Out-Null
    nssm set $ServiceName AppRotateOnline 1 | Out-Null
    Start-RegisteredService
    Log-Success "Service $ServiceName installed and started using nssm."
}

function Show-ServiceStatus {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) {
        Log-Error "Service $ServiceName was not found."
        exit 1
    }

    $svc | Format-List Name, DisplayName, Status, StartType
    try {
        $appParameters = nssm get $ServiceName AppParameters 2>$null
        if ($appParameters) {
            Log-Info "AppParameters: $appParameters"
        }
    }
    catch {
    }
}

function Show-ServiceLogs {
    if (Test-Path -LiteralPath $AgentLogPath) {
        Get-Content -Path $AgentLogPath -Tail 100 -Wait
        return
    }

    Log-Warning "No redirected service log file was found at $AgentLogPath."
    Log-Info 'If this is an older installation, re-run install.ps1 --reconfigure to register the standard log file path.'
}

function Collect-InteractiveInstallInputs {
    $endpoint = ''
    while ([string]::IsNullOrWhiteSpace($endpoint)) {
        $endpoint = Read-Host '请输入面板地址 (例如 https://monitor.example.com)'
        if ([string]::IsNullOrWhiteSpace($endpoint)) {
            Log-Error '面板地址不能为空。'
        }
    }

    $script:KomariArgs = @('--endpoint', $endpoint)
    $script:TokenValue = ''
    $script:HasExplicitConfig = $false
    $script:ExplicitConfigPath = ''

    if (Test-Path -LiteralPath $ConfigFile -PathType Leaf) {
        Write-Host '请选择认证材料来源：'
        Write-Host "  1) 复用默认配置文件 $ConfigFile"
        Write-Host '  2) 使用自定义配置文件路径'
        Write-Host '  3) 输入节点 Token，并自动生成默认配置文件'
        $configChoice = Read-Host '输入选项 [1-3]'
    }
    else {
        Write-Host '请选择认证材料来源：'
        Write-Host '  1) 使用自定义配置文件路径'
        Write-Host '  2) 输入节点 Token，并自动生成默认配置文件'
        $configChoice = Read-Host '输入选项 [1-2]'
    }

    switch ($configChoice) {
        '1' {
            if (Test-Path -LiteralPath $ConfigFile -PathType Leaf) {
                $script:HasExplicitConfig = $true
                $script:ExplicitConfigPath = $ConfigFile
                $script:KomariArgs += '--config'
                $script:KomariArgs += $ConfigFile
            }
            else {
                $configPath = Prompt-WithDefault -Prompt '请输入现有配置文件路径' -DefaultValue $ConfigFile
                $script:HasExplicitConfig = $true
                $script:ExplicitConfigPath = $configPath
                $script:KomariArgs += '--config'
                $script:KomariArgs += $configPath
            }
        }
        '2' {
            if (Test-Path -LiteralPath $ConfigFile -PathType Leaf) {
                $configPath = Prompt-WithDefault -Prompt '请输入现有配置文件路径' -DefaultValue $ConfigFile
                $script:HasExplicitConfig = $true
                $script:ExplicitConfigPath = $configPath
                $script:KomariArgs += '--config'
                $script:KomariArgs += $configPath
            }
            else {
                $script:TokenValue = ConvertTo-PlainText -SecureString (Read-Host '请输入节点 Token' -AsSecureString)
            }
        }
        '3' {
            $script:TokenValue = ConvertTo-PlainText -SecureString (Read-Host '请输入节点 Token' -AsSecureString)
        }
        default {
            Log-Error '无效选项'
            exit 1
        }
    }

    if (Prompt-YesNo -Prompt '是否启用远程 Ping / 延迟监测' -Default:$false) {
        $pingConcurrency = Prompt-WithDefault -Prompt '请输入最大并发 Ping 数' -DefaultValue '24'
        $pingMinInterval = Prompt-WithDefault -Prompt '请输入最小 Ping 间隔（毫秒）' -DefaultValue '0'
        $script:KomariArgs += '--enable-ping'
        $script:KomariArgs += '--max-concurrent-pings'
        $script:KomariArgs += $pingConcurrency
        $script:KomariArgs += '--ping-min-interval-millis'
        $script:KomariArgs += $pingMinInterval
    }

    Update-DerivedValues
}

function Install-Agent {
    param([bool]$Interactive)

    if ((Service-Exists) -or (Test-Path -LiteralPath $AgentPath -PathType Leaf)) {
        Log-Warning 'Agent appears to be installed already. Use upgrade or reconfigure instead of install.'
        exit 1
    }

    if ($Interactive) {
        Collect-InteractiveInstallInputs
    }

    Ensure-InstallInputs
    Update-DerivedValues
    Show-OperationConfiguration -Heading 'Installation configuration:'
    Prepare-DownloadContext
    Download-AgentBinary
    Replace-DownloadedBinary
    Write-GeneratedConfigIfNeeded
    Configure-Service
    Log-Success 'Komari Agent installation completed!'
    Log-Config "Service name: $ServiceName"
    Log-Config "Arguments: $RedactedArgString"
}

function Reconfigure-Agent {
    param([bool]$Interactive)

    if ($Interactive) {
        Collect-InteractiveInstallInputs
    }

    Ensure-InstallInputs
    Update-DerivedValues
    Show-OperationConfiguration -Heading 'Reconfiguration:'

    if ((-not (Test-Path -LiteralPath $AgentPath -PathType Leaf)) -or (-not [string]::IsNullOrWhiteSpace($InstallVersion))) {
        Log-Info 'Agent binary is missing or a target version was requested. Downloading binary before reconfiguring...'
        Prepare-DownloadContext
        Download-AgentBinary
        Replace-DownloadedBinary
    }
    else {
        Log-Info "Reusing existing binary at $AgentPath"
    }

    Write-GeneratedConfigIfNeeded
    Configure-Service
    Log-Success 'Komari Agent reconfiguration completed!'
}

function Upgrade-Agent {
    if (-not (Test-Path -LiteralPath $AgentPath -PathType Leaf)) {
        Log-Error "Agent binary was not found at $AgentPath. Run install first."
        exit 1
    }

    Show-OperationConfiguration -Heading 'Upgrade configuration:'
    Prepare-DownloadContext

    $serviceWasRegistered = Service-Exists
    if ($serviceWasRegistered) {
        Log-Step 'Stopping existing service before upgrade...'
        Stop-RegisteredService
    }

    $backupPath = "$AgentPath.backup.$([DateTime]::Now.ToString('yyyyMMdd_HHmmss'))"
    Copy-Item -Path $AgentPath -Destination $backupPath -Force
    Log-Info "Backed up current binary to $backupPath"

    try {
        Download-AgentBinary
        Replace-DownloadedBinary
    }
    catch {
        if ($serviceWasRegistered) {
            Start-RegisteredService
        }
        exit 1
    }

    if ($serviceWasRegistered) {
        try {
            Start-RegisteredService
        }
        catch {
            Log-Error 'Failed to restart the service after upgrade. Restoring previous binary...'
            Copy-Item -Path $backupPath -Destination $AgentPath -Force
            Start-RegisteredService
            exit 1
        }
    }
    else {
        Log-Warning 'No registered service was found. Binary has been upgraded, but no service restart was performed.'
    }

    Log-Success 'Komari Agent upgrade completed!'
}

function Uninstall-Agent {
    if (-not $AssumeYes) {
        if (-not (Prompt-YesNo -Prompt '这将卸载 Komari Agent。是否继续' -Default:$false)) {
            Log-Info '已取消卸载。'
            return
        }
    }

    Remove-ServiceRegistration

    if (Test-Path -LiteralPath $AgentPath -PathType Leaf) {
        Remove-Item $AgentPath -Force
        Log-Success "Removed binary: $AgentPath"
    }

    if (Test-Path -LiteralPath $LegacyTokenFile -PathType Leaf) {
        Remove-Item $LegacyTokenFile -Force
    }

    if ($PurgeConfig -and (Test-Path -LiteralPath $ConfigFile -PathType Leaf)) {
        Remove-Item $ConfigFile -Force
        Log-Success "Removed config file: $ConfigFile"
    }
    elseif (Test-Path -LiteralPath $ConfigFile -PathType Leaf) {
        Log-Warning "Preserved config file: $ConfigFile"
        Log-Info 'Use --purge-config if you also want to delete the saved config file.'
    }

    Log-Success 'Komari Agent uninstall completed!'
}

function Restart-Agent {
    if (-not (Service-Exists)) {
        Log-Error "Service $ServiceName was not found."
        exit 1
    }
    Restart-RegisteredService
    Log-Success "Service restarted: $ServiceName"
}

function Stop-Agent {
    if (-not (Service-Exists)) {
        Log-Error "Service $ServiceName was not found."
        exit 1
    }
    Stop-RegisteredService
    Log-Success "Service stopped: $ServiceName"
}

function Show-Menu {
    Show-Banner
    Write-Host '请选择操作：'
    Write-Host '  1) 安装 Agent'
    Write-Host '  2) 升级 Agent'
    Write-Host '  3) 重配 Agent'
    Write-Host '  4) 卸载 Agent'
    Write-Host '  5) 查看状态'
    Write-Host '  6) 查看日志'
    Write-Host '  7) 重启服务'
    Write-Host '  8) 停止服务'
    Write-Host '  9) 退出'
    Write-Host ''

    switch (Read-Host '输入选项 [1-9]') {
        '1' { Install-Agent -Interactive:$true }
        '2' { Upgrade-Agent }
        '3' { Reconfigure-Agent -Interactive:$true }
        '4' { Uninstall-Agent }
        '5' { Show-ServiceStatus }
        '6' { Show-ServiceLogs }
        '7' { Restart-Agent }
        '8' { Stop-Agent }
        '9' { return }
        default {
            Log-Error '无效选项'
            exit 1
        }
    }
}

switch ($Operation) {
    'help' { Show-Usage }
    'menu' { Show-Menu }
    'install' { Install-Agent -Interactive:$false }
    'upgrade' { Upgrade-Agent }
    'reconfigure' { Reconfigure-Agent -Interactive:$false }
    'uninstall' { Uninstall-Agent }
    'status' { Show-ServiceStatus }
    'logs' { Show-ServiceLogs }
    'restart' { Restart-Agent }
    'stop' { Stop-Agent }
    default {
        Log-Error "Unsupported operation: $Operation"
        exit 1
    }
}