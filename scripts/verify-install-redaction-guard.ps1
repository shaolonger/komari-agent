param()

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
$installShPath = Join-Path $repoRoot 'install.sh'
$installPs1Path = Join-Path $repoRoot 'install.ps1'

$installSh = Get-Content -Raw -Path $installShPath
$installPs1 = Get-Content -Raw -Path $installPs1Path

if ($installSh -notmatch 'komari_service_args_log="\$\(redact_komari_args "\$komari_service_args"\)"') {
    throw 'install.sh no longer derives a redacted argument string before logging service arguments.'
}
if (
    $installSh -notmatch 'log_config "  (?:Agent|Binary) arguments: \$\{GREEN\}\$komari_service_args_log\$\{NC\}"' -and
    $installSh -notmatch 'log_config "Arguments: \$\{GREEN\}\$komari_service_args_log\$\{NC\}"'
) {
    throw 'install.sh no longer logs redacted service arguments.'
}
if ($installSh -notmatch 'ExecStart = .*komari_service_args_log') {
    throw 'install.sh no longer redacts the NixOS service preview output.'
}
if ($installSh -match 'log_(info|warning|error|success|config)\s+"[^"]*\$komari_service_args(?!_log)') {
    throw 'install.sh appears to log raw service arguments.'
}
if ($installSh -match 'log_(info|warning|error|success|config)\s+"[^"]*\$komari_token') {
    throw 'install.sh appears to log the raw token.'
}

if ($installPs1 -notmatch '\$(?:script:)?RedactedArgString = Format-KomariArgsForLog -Arguments \$(?:script:)?(?:EffectiveKomariArgs|KomariArgs)') {
    throw 'install.ps1 no longer derives a redacted argument string before logging.'
}
if ($installPs1 -notmatch 'Log-Config "Agent arguments: \$RedactedArgString"') {
    throw 'install.ps1 no longer logs redacted agent arguments.'
}
if ($installPs1 -notmatch 'Log-Config "Arguments: \$RedactedArgString"') {
    throw 'install.ps1 no longer logs redacted Windows service arguments.'
}
if ($installPs1 -match 'Log-[A-Za-z]+\s+"[^"]*\$KomariArgs') {
    throw 'install.ps1 appears to log raw argument arrays.'
}
if ($installPs1 -match 'Log-[A-Za-z]+\s+"[^"]*\$argString') {
    throw 'install.ps1 appears to log raw Windows service arguments.'
}
if ($installPs1 -match 'Log-[A-Za-z]+\s+"[^"]*\$TokenValue') {
    throw 'install.ps1 appears to log the raw token.'
}

Write-Host 'Install script redaction guards passed.'