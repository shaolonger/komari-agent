param(
    [switch]$SkipLiveReleaseCheck
)

$ErrorActionPreference = 'Stop'

function Assert-PowerShellScriptParses {
    param([string]$Path)

    $tokens = $null
    $errors = $null
    [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$errors) | Out-Null
    if ($errors -and $errors.Count -gt 0) {
        throw ($errors | ForEach-Object { "${Path}: $($_.Message)" } | Out-String)
    }
}

function Assert-BashScriptParses {
    param(
        [string]$Path,
        [string]$BashExecutable
    )

    $content = Get-Content -Raw -Path $Path
    $normalizedContent = $content -replace "`r`n", "`n"
    $normalizedContent | & $BashExecutable -n
    if ($LASTEXITCODE -ne 0) {
        throw "bash -n failed for $Path"
    }
}

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
Push-Location $repoRoot

try {
    $bashCommand = Get-Command bash -ErrorAction SilentlyContinue
    if (-not $bashCommand) {
        throw 'bash is required to run shell-based supply-chain validation'
    }

    Assert-PowerShellScriptParses (Join-Path $repoRoot 'install.ps1')
    Assert-PowerShellScriptParses (Join-Path $PSScriptRoot 'verify-install-ps1-integrity.ps1')
    Assert-PowerShellScriptParses (Join-Path $PSScriptRoot 'verify-supply-chain-stage.ps1')
    Assert-BashScriptParses -Path (Join-Path $repoRoot 'install.sh') -BashExecutable $bashCommand.Source
    Assert-BashScriptParses -Path (Join-Path $PSScriptRoot 'verify-install-sh-integrity.sh') -BashExecutable $bashCommand.Source

    if (-not $SkipLiveReleaseCheck) {
        $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/shaolonger/komari-agent/releases/latest' -UseBasicParsing
        $assetNames = @($release.assets | Select-Object -ExpandProperty name)
        $binaryAssets = @($assetNames | Where-Object { $_ -notmatch '\.(sha256|sig|pem)$' })
        $missingChecksums = @($binaryAssets | Where-Object { "$_.sha256" -notin $assetNames })
        $missingChecksumSignatures = @($binaryAssets | Where-Object { "$_.sha256.sig" -notin $assetNames })
        $missingChecksumCertificates = @($binaryAssets | Where-Object { "$_.sha256.pem" -notin $assetNames })

        if ($missingChecksums.Count -gt 0) {
            throw "Latest release $($release.tag_name) is missing checksum assets for: $($missingChecksums -join ', ')"
        }
        if ($missingChecksumSignatures.Count -gt 0) {
            throw "Latest release $($release.tag_name) is missing checksum signatures for: $($missingChecksumSignatures -join ', ')"
        }
        if ($missingChecksumCertificates.Count -gt 0) {
            throw "Latest release $($release.tag_name) is missing checksum signing certificates for: $($missingChecksumCertificates -join ', ')"
        }

        Write-Host "Latest release $($release.tag_name) includes checksum and checksum-signing assets for all published binaries."
    }

    & go test ./update
    if ($LASTEXITCODE -ne 0) {
        throw 'go test ./update failed'
    }

    & (Join-Path $PSScriptRoot 'verify-install-ps1-integrity.ps1')
    if ($LASTEXITCODE -ne 0) {
        throw 'verify-install-ps1-integrity.ps1 failed'
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