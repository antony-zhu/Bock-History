$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$blockRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$bdmRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..\BDM')).Path
$manifestPath = Join-Path $bdmRoot 'deploy\bdm\config\mqtt-identities.example.yaml'
$aclPath = Join-Path $bdmRoot 'deploy\bdm\mosquitto\acl.d\bdm-v1.acl'
$registrationPath = Join-Path $bdmRoot 'deploy\bdm\postgresql\register-block.example.sql'

$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding utf8
$principalMatch = [regex]::Match(
    $manifest,
    '(?m)^\s*principal:\s*(blk-[0-9a-f]{32})\s*$'
)
if (-not $principalMatch.Success) {
    throw "BDM identity manifest has no single Block opaque principal: $manifestPath"
}
$expectedPrincipal = $principalMatch.Groups[1].Value

foreach ($name in @(
    'block-agent-bdm.example.json',
    'block-agent-simulator-bdm.example.json'
)) {
    $path = Join-Path $blockRoot "deploy\block\config\$name"
    $config = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
    if ($config.bdm.principal -cne $expectedPrincipal) {
        throw "$name principal $($config.bdm.principal) differs from BDM manifest $expectedPrincipal"
    }
    if ($config.siteId -cne 'site-lab' -or
        $config.blockId -cne 'block-001' -or
        $config.deviceId -cne 'device-001') {
        throw "$name does not use the single-device lab identity tuple"
    }
}

$acl = Get-Content -LiteralPath $aclPath -Raw -Encoding utf8
if ($acl -cnotmatch "(?m)^user $([regex]::Escape($expectedPrincipal))$") {
    throw "BDM ACL does not contain the manifest principal: $aclPath"
}
$registration = Get-Content -LiteralPath $registrationPath -Raw -Encoding utf8
if (-not $registration.Contains("'$expectedPrincipal'")) {
    throw "BDM registration SQL does not contain the manifest principal: $registrationPath"
}

Write-Output "OK: Block examples, BDM identity manifest, ACL and registration SQL use $expectedPrincipal"
