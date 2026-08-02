$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')

function Assert-Contains {
    param(
        [string]$Path,
        [string]$Pattern,
        [string]$Description
    )

    $content = Get-Content -Raw -Path (Join-Path $repoRoot $Path)
    if ($content -notmatch $Pattern) {
        throw "$Path does not enforce $Description"
    }
}

function Assert-NotContains {
    param(
        [string]$Path,
        [string]$Pattern,
        [string]$Description
    )

    $content = Get-Content -Raw -Path (Join-Path $repoRoot $Path)
    if ($content -match $Pattern) {
        throw "$Path contains forbidden $Description"
    }
}

$goMod = Get-Content -Raw -Path (Join-Path $repoRoot 'go.mod')
if ($goMod -notmatch '(?m)^go 1\.26\.0\r?$' -or $goMod -notmatch '(?m)^toolchain go1\.26\.5\r?$') {
    throw 'go.mod does not pin the approved Go language and toolchain versions'
}

Assert-Contains 'scripts/build-release.sh' '-trimpath' 'trimmed source paths'
Assert-Contains 'scripts/build-release.sh' '-buildvcs=false' 'stable VCS metadata'
Assert-Contains 'scripts/build-release.sh' '-pgo=' 'the reviewed PGO profile'
Assert-Contains 'scripts/build-release.sh' '-s -w -buildid=' 'stripping and deterministic build IDs'
Assert-Contains 'scripts/build-release.sh' 'go1\.26\.5' 'the exact release toolchain'

$buildWorkflows = @(
    '.github/workflows/build.yml',
    '.github/workflows/release.yml',
    '.github/workflows/release-docker.yml'
)
foreach ($workflow in $buildWorkflows) {
    Assert-Contains $workflow 'scripts/build-release\.sh' 'the centralized release builder'
}

foreach ($workflow in @(
    '.github/workflows/build.yml',
    '.github/workflows/security-regression.yml',
    '.github/workflows/network-integration.yml',
    '.github/workflows/release.yml',
    '.github/workflows/release-docker.yml'
)) {
    Assert-Contains $workflow 'go-version: "1\.26\.5"' 'the exact Go toolchain'
    Assert-NotContains $workflow 'go-version: "1\.23' 'an unsupported Go toolchain'
}

Assert-Contains '.github/workflows/release.yml' 'sha256sum -c' 'checksum verification before upload'
Assert-Contains '.github/workflows/release.yml' 'cosign sign-blob' 'keyless checksum signing'
Assert-Contains '.github/workflows/release.yml' 'cosign verify-blob' 'keyless signature verification before upload'

$profile = Join-Path $repoRoot 'default.pgo'
if (-not (Test-Path -Path $profile -PathType Leaf)) {
    throw 'default.pgo is missing'
}
if ((Get-Item $profile).Length -le 0) {
    throw 'default.pgo is empty'
}

Write-Host 'Release build policy validation completed.'
