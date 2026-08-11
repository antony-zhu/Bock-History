[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$deploy = Join-Path $PSScriptRoot "deploy-hmi.ps1"
$result = & $deploy -Version "dry-run-test" -DeviceAddress "192.0.2.10" `
    -SiteId "site-lab" -BlockId "block-001" -DeviceId "device-001" -Build -DryRun

if ($result.Mode -ne "dry-run") {
    throw "Dry run returned mode $($result.Mode), want dry-run."
}
$plan = [string]::Join([Environment]::NewLine, @($result.Plan))
foreach ($required in @(
    "tools/build-block.ps1",
    "WSL tar",
    "five-minute release SSH certificate",
    "sudo -n true",
    "known_hosts",
    "candidate install.sh",
    "verify-install.sh"
)) {
    if (-not $plan.Contains($required)) {
        throw "Dry-run plan is missing $required."
    }
}
if ($plan -match '(?i)(--insecure|(^|\s)-k(\s|$)|passwordauthentication=yes|stricthostkeychecking=no)') {
    throw "Dry-run plan includes an insecure transport fallback."
}
Write-Output "deploy-hmi dry-run plan passed."
