$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
$installerPath = Join-Path $repoRoot 'install.ps1'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($installerPath, [ref]$tokens, [ref]$errors)
if ($errors -and $errors.Count -gt 0) {
    throw ($errors | ForEach-Object { $_.Message } | Out-String)
}

$functionNames = @(
    'Get-KomariFileHash',
    'Get-KomariChecksumValue',
    'Assert-KomariFileHash',
    'Assert-TrustedGitHubProxy'
)

foreach ($functionName in $functionNames) {
    $functionAst = $ast.Find({
            param($node)
            $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $functionName
        }, $true)
    if (-not $functionAst) {
        throw "Function $functionName not found in install.ps1"
    }

    . ([scriptblock]::Create($functionAst.Extent.Text))
}

$tempRoot = Join-Path $env:TEMP "komari-install-ps1-integrity-$PID"
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null

try {
    $payloadPath = Join-Path $tempRoot 'agent.exe'
    $checksumPath = Join-Path $tempRoot 'agent.exe.sha256'

    [System.IO.File]::WriteAllText($payloadPath, 'trusted payload', [System.Text.UTF8Encoding]::new($false))
    $expectedHash = (Get-FileHash -Path $payloadPath -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText($checksumPath, "$expectedHash  agent.exe`n", [System.Text.UTF8Encoding]::new($false))

    $parsedHash = Get-KomariChecksumValue -Path $checksumPath
    Assert-KomariFileHash -Path $payloadPath -Algorithm SHA256 -ExpectedHash $parsedHash -Label 'agent.exe'

    [System.IO.File]::WriteAllText($payloadPath, 'tampered payload', [System.Text.UTF8Encoding]::new($false))
    $tamperRejected = $false
    try {
        Assert-KomariFileHash -Path $payloadPath -Algorithm SHA256 -ExpectedHash $parsedHash -Label 'agent.exe'
    }
    catch {
        $tamperRejected = $true
    }

    if (-not $tamperRejected) {
        throw 'Expected tampered payload verification to fail.'
    }

    $normalizedProxy = Assert-TrustedGitHubProxy -Value 'https://mirror.example.com/github-release/' -TrustAcknowledged
    if ($normalizedProxy -ne 'https://mirror.example.com/github-release') {
        throw "Unexpected normalized proxy: $normalizedProxy"
    }

    $invalidProxyCases = @(
        @{ Value = 'https://mirror.example.com/github-release'; TrustAcknowledged = $false },
        @{ Value = 'http://mirror.example.com/github-release'; TrustAcknowledged = $true },
        @{ Value = 'https://user:pass@mirror.example.com/github-release'; TrustAcknowledged = $true },
        @{ Value = 'https://mirror.example.com/github-release?token=123'; TrustAcknowledged = $true }
    )

    foreach ($case in $invalidProxyCases) {
        $rejected = $false
        try {
            Assert-TrustedGitHubProxy -Value $case.Value -TrustAcknowledged:$case.TrustAcknowledged | Out-Null
        }
        catch {
            $rejected = $true
        }

        if (-not $rejected) {
            throw "Expected proxy case '$($case.Value)' to be rejected."
        }
    }

    Write-Host 'install.ps1 integrity checks passed'
}
finally {
    if (Test-Path $tempRoot) {
        Remove-Item -Path $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}