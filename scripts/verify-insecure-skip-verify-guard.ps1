$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

$allowedMatches = @{}
@(
    @{
        Path = 'cmd/root.go'
        Line = 'http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}'
        Reason = 'Explicit --ignore-unsafe-cert override for the shared default transport; pending Phase 1.2 redesign.'
    }
    @{
        Path = 'dnsresolver/resolver.go'
        Line = 'InsecureSkipVerify: insecureSkipVerify,'
        Reason = 'Transport builder keeps the toggle injectable; callers must opt in explicitly and update paths already decouple self-update.'
    }
    @{
        Path = 'server/websocket.go'
        Line = 'd.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}'
        Reason = 'WebSocket dialer only enables insecure TLS behind --ignore-unsafe-cert; pending Phase 1.2 redesign.'
    }
) | ForEach-Object {
    $allowedMatches["$($_.Path)|$($_.Line)"] = $_
}

$goFiles = Get-ChildItem -Path $repoRoot -Recurse -Filter '*.go' -File
$matches = @(
    $goFiles | Select-String -Pattern '\bInsecureSkipVerify\b\s*:'
    $goFiles | Select-String -Pattern '\.InsecureSkipVerify\s*='
)
$unexpectedMatches = @()

foreach ($match in $matches) {
    $relativePath = [System.IO.Path]::GetRelativePath($repoRoot, $match.Path).Replace('\', '/')
    $normalizedLine = ($match.Line -replace '\s+', ' ').Trim()
    $key = "$relativePath|$normalizedLine"

    if (-not $allowedMatches.Contains($key)) {
        $unexpectedMatches += [PSCustomObject]@{
            Path = $relativePath
            LineNumber = $match.LineNumber
            Line = $normalizedLine
        }
    }
}

if ($unexpectedMatches.Count -gt 0) {
    Write-Host 'Unexpected InsecureSkipVerify usage detected:' -ForegroundColor Red
    $unexpectedMatches | ForEach-Object {
        Write-Host "- $($_.Path):$($_.LineNumber) $($_.Line)" -ForegroundColor Red
    }
    throw 'Unexpected InsecureSkipVerify usage found outside the approved exception list.'
}

Write-Host "InsecureSkipVerify guard passed. Approved exception count: $($matches.Count)." -ForegroundColor Green