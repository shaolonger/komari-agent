param(
    [switch]$SkipLiveReleaseCheck
)

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
Push-Location $repoRoot

try {
    & (Join-Path $PSScriptRoot 'verify-supply-chain-stage.ps1') -SkipLiveReleaseCheck:$SkipLiveReleaseCheck
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-supply-chain-stage.ps1 failed'
    }

    & (Join-Path $PSScriptRoot 'verify-insecure-skip-verify-guard.ps1')
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-insecure-skip-verify-guard.ps1 failed'
    }

    & (Join-Path $PSScriptRoot 'verify-token-query-guard.ps1')
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-token-query-guard.ps1 failed'
    }

    & (Join-Path $PSScriptRoot 'verify-install-redaction-guard.ps1')
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-install-redaction-guard.ps1 failed'
    }

    Write-Host 'Security regression checks completed.'
}
finally {
    Pop-Location
}