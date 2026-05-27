$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

$allowedMatches = @{}
@(
    @{
        Path = 'server/websocket.go'
        Line = 'websocketEndpoint := strings.TrimSuffix(flags.Endpoint, "/") + "/api/clients/report?token=" + flags.Token'
        Reason = 'Primary reporting websocket still uses token query auth until the Phase 1.1 header migration lands.'
    }
    @{
        Path = 'server/websocket.go'
        Line = 'endpoint = strings.TrimSuffix(endpoint, "/") + "/api/clients/terminal?token=" + token + "&id=" + id'
        Reason = 'Terminal websocket still uses token query auth until the Phase 1.1 header migration lands.'
    }
    @{
        Path = 'server/basicInfo.go'
        Line = 'endpoint := strings.TrimSuffix(flags.Endpoint, "/") + "/api/clients/uploadBasicInfo?token=" + flags.Token'
        Reason = 'Basic info upload still uses token query auth until the Phase 1.1 header migration lands.'
    }
    @{
        Path = 'server/task.go'
        Line = 'endpoint := flags.Endpoint + "/api/clients/task/result?token=" + flags.Token'
        Reason = 'Task result upload still uses token query auth until the Phase 1.1 header migration lands.'
    }
) | ForEach-Object {
    $allowedMatches["$($_.Path)|$($_.Line)"] = $_
}

$goFiles = Get-ChildItem -Path $repoRoot -Recurse -Filter '*.go' -File | Where-Object { $_.Name -notlike '*_test.go' }
$matches = @($goFiles | Select-String -Pattern '[?&]token=')
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
    Write-Host 'Unexpected token query authentication usage detected:' -ForegroundColor Red
    $unexpectedMatches | ForEach-Object {
        Write-Host "- $($_.Path):$($_.LineNumber) $($_.Line)" -ForegroundColor Red
    }
    throw 'Unexpected token query authentication usage found outside the approved exception list.'
}

Write-Host "Token query auth guard passed. Approved exception count: $($matches.Count)." -ForegroundColor Green