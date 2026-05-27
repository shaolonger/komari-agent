param(
    [switch]$SkipLiveReleaseCheck
)

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
Push-Location $repoRoot

try {
    if (-not $SkipLiveReleaseCheck) {
        $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/komari-monitor/komari-agent/releases/latest' -UseBasicParsing
        $assetNames = @($release.assets | Select-Object -ExpandProperty name)
        $binaryAssets = @($assetNames | Where-Object { $_ -notlike '*.sha256' })
        $missingChecksums = @($binaryAssets | Where-Object { "$_.sha256" -notin $assetNames })

        if ($missingChecksums.Count -gt 0) {
            throw "Latest release $($release.tag_name) is missing checksum assets for: $($missingChecksums -join ', ')"
        }

        Write-Host "Latest release $($release.tag_name) includes checksum assets for all published binaries."
    }

    & go test ./update
    if ($LASTEXITCODE -ne 0) {
        throw 'go test ./update failed'
    }

    & (Join-Path $PSScriptRoot 'verify-install-ps1-integrity.ps1')
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-install-ps1-integrity.ps1 failed'
    }

    $bashCommand = Get-Command bash -ErrorAction SilentlyContinue
    if (-not $bashCommand) {
        throw 'bash is required to run scripts/verify-install-sh-integrity.sh'
    }

    & $bashCommand.Source './scripts/verify-install-sh-integrity.sh'
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-install-sh-integrity.sh failed'
    }

    Write-Host 'Supply-chain stage validation completed.'
}
finally {
    Pop-Location
}