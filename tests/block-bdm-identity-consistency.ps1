$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$blockRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$workspaceRoot = (Resolve-Path (Join-Path $blockRoot '..\..')).Path
$bdmRoot = (Resolve-Path (Join-Path $blockRoot '..\BDM')).Path
$validationRoot = Join-Path $workspaceRoot '.cache\validation\block-upstream-20260729\identity-qa-recovery'
$tempRoot = Join-Path $validationRoot 'tmp'
New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
foreach ($name in @('TEMP', 'TMP', 'TMPDIR', 'GOTMPDIR')) {
    Set-Item -Path "env:$name" -Value $tempRoot
}

$blockPrincipalPattern = '^blk-[0-9a-f]{32}$'
$bdmPrincipalPattern = '^bdm-[0-9a-f]{32}$'
$protocolIdPattern = '^[a-z0-9](?:[a-z0-9_-]{0,62})$'
$uplinkChannels = @(
    'presence',
    'hello',
    'heartbeat',
    'snapshot',
    'event',
    'alarm',
    'replay',
    'sync-status'
)
$blockConfigNames = @(
    'block-agent-bdm.example.json',
    'block-agent-simulator-bdm.example.json'
)
$checkedInBlockConfigs = @(
    $blockConfigNames | ForEach-Object {
        Join-Path $blockRoot "deploy\block\config\$_"
    }
)
$manifestTemplatePath = Join-Path $bdmRoot 'deploy\bdm\config\mqtt-identities.example.yaml'
$aclTemplatePath = Join-Path $bdmRoot 'deploy\bdm\mosquitto\acl.d\bdm-v1.acl'
$registrationTemplatePath = Join-Path $bdmRoot 'deploy\bdm\postgresql\register-block.example.sql'

function Assert-Condition {
    param(
        [Parameter(Mandatory)]
        [bool]$Condition,

        [Parameter(Mandatory)]
        [string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Assert-Rejected {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [scriptblock]$Action
    )

    try {
        & $Action
    }
    catch {
        Write-Output "OK: rejected $Name"
        return
    }
    throw "identity consistency unexpectedly accepted $Name"
}

function Get-RequiredProperty {
    param(
        [Parameter(Mandatory)]
        [object]$Object,

        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [string]$Context
    )

    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) {
        throw "$Context is missing $Name"
    }
    return $property.Value
}

function Read-IdentityMap {
    param(
        [Parameter(Mandatory)]
        [string]$Path
    )

    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path -Encoding utf8) {
        if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith('#')) {
            continue
        }
        $match = [regex]::Match($line, '^([A-Z0-9_]+)=(.+)$')
        if (-not $match.Success) {
            throw "identity mapping contains a malformed row: $Path"
        }
        $key = $match.Groups[1].Value
        if ($values.ContainsKey($key)) {
            throw "identity mapping contains duplicate key $key"
        }
        $values[$key] = $match.Groups[2].Value
    }

    $expectedKeys = @(
        'BDM_MQTT_CLIENT_ID',
        'BLOCK_MQTT_CLIENT_ID',
        'BDM_SITE_ID',
        'BDM_BLOCK_ID',
        'BDM_DEVICE_ID'
    )
    Assert-Condition ($values.Count -eq $expectedKeys.Count) `
        'identity mapping must contain exactly the approved keys'
    foreach ($key in $expectedKeys) {
        Assert-Condition $values.ContainsKey($key) "identity mapping is missing $key"
    }
    Assert-Condition ($values.BLOCK_MQTT_CLIENT_ID -cmatch $blockPrincipalPattern) `
        'identity mapping Block principal is not opaque'
    Assert-Condition ($values.BDM_MQTT_CLIENT_ID -cmatch $bdmPrincipalPattern) `
        'identity mapping BDM principal is not opaque'
    foreach ($key in @('BDM_SITE_ID', 'BDM_BLOCK_ID', 'BDM_DEVICE_ID')) {
        Assert-Condition ($values[$key] -cmatch $protocolIdPattern) `
            "identity mapping $key is not a protocol identifier"
    }
    return $values
}

function Test-IdentityConsistency {
    param(
        [Parameter(Mandatory)]
        [string[]]$BlockConfigPaths,

        [Parameter(Mandatory)]
        [string]$IdentityMapPath,

        [Parameter(Mandatory)]
        [string]$AclPath,

        [Parameter(Mandatory)]
        [string]$RegistrationPath
    )

    $mapping = Read-IdentityMap -Path $IdentityMapPath
    $blockPrincipal = $mapping.BLOCK_MQTT_CLIENT_ID
    $bdmPrincipal = $mapping.BDM_MQTT_CLIENT_ID
    $siteId = $mapping.BDM_SITE_ID
    $blockId = $mapping.BDM_BLOCK_ID
    $deviceId = $mapping.BDM_DEVICE_ID

    foreach ($path in $BlockConfigPaths) {
        $config = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
        $configSiteId = Get-RequiredProperty $config 'siteId' $path
        $configBlockId = Get-RequiredProperty $config 'blockId' $path
        $configDeviceId = Get-RequiredProperty $config 'deviceId' $path
        $bdm = Get-RequiredProperty $config 'bdm' $path
        $configPrincipal = Get-RequiredProperty $bdm 'principal' "$path bdm"
        $endpoint = Get-RequiredProperty $bdm 'endpoint' "$path bdm"

        Assert-Condition ($configPrincipal -cmatch $blockPrincipalPattern) `
            "$path does not contain an opaque Block principal"
        Assert-Condition ($configPrincipal -ceq $blockPrincipal) `
            "$path principal differs from the approved identity mapping"
        Assert-Condition (
            $configSiteId -ceq $siteId -and
            $configBlockId -ceq $blockId -and
            $configDeviceId -ceq $deviceId
        ) "$path identity tuple differs from the approved identity mapping"

        $endpointUri = [uri]$endpoint
        Assert-Condition ($endpointUri.Scheme -ceq 'mqtts') `
            "$path endpoint must use MQTTS"
        Assert-Condition (
            $endpointUri.Host -cne $configSiteId -and
            $endpointUri.Host -cne $configBlockId -and
            $endpointUri.Host -cne $configDeviceId -and
            $endpointUri.Host -cne $configPrincipal
        ) "$path uses its route host as business identity"
    }

    $expectedAcl = [System.Collections.Generic.List[string]]::new()
    $expectedAcl.Add("user $blockPrincipal")
    foreach ($channel in $uplinkChannels) {
        $expectedAcl.Add("topic write bdm/v1/sites/$siteId/blocks/$blockId/up/$channel")
    }
    $expectedAcl.Add("topic read bdm/v1/sites/$siteId/blocks/$blockId/down/sync")
    $expectedAcl.Add("user $bdmPrincipal")
    foreach ($channel in $uplinkChannels) {
        $expectedAcl.Add("topic read bdm/v1/sites/$siteId/blocks/$blockId/up/$channel")
    }
    $expectedAcl.Add("topic write bdm/v1/sites/$siteId/blocks/$blockId/down/sync")

    $actualAcl = @(
        Get-Content -LiteralPath $AclPath -Encoding utf8 |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -and -not $_.StartsWith('#') }
    )
    Assert-Condition ($actualAcl.Count -eq $expectedAcl.Count) `
        'BDM ACL is not the exact single-Block allowlist'
    for ($index = 0; $index -lt $expectedAcl.Count; $index++) {
        Assert-Condition ($actualAcl[$index] -ceq $expectedAcl[$index]) `
            "BDM ACL differs at line $($index + 1)"
    }

    $registration = Get-Content -LiteralPath $RegistrationPath -Raw -Encoding utf8
    foreach ($fragment in @(
        "VALUES ('$blockPrincipal', '$siteId', '$blockId', '$deviceId')",
        "WHERE principal = '$blockPrincipal'",
        "AND site_id = '$siteId'",
        "AND block_id = '$blockId'",
        "AND device_id = '$deviceId'",
        'AND enabled'
    )) {
        Assert-Condition $registration.Contains($fragment) `
            "BDM registration SQL is missing the approved binding fragment: $fragment"
    }
    $registeredPrincipals = @(
        [regex]::Matches($registration, 'blk-[0-9a-f]{32}') |
            ForEach-Object { $_.Value } |
            Sort-Object -Unique
    )
    Assert-Condition (
        $registeredPrincipals.Count -eq 1 -and
        $registeredPrincipals[0] -ceq $blockPrincipal
    ) 'BDM registration SQL contains another Block principal'
}

function Assert-CheckedInTemplatesFailClosed {
    foreach ($path in $checkedInBlockConfigs) {
        $config = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
        $principal = Get-RequiredProperty $config.bdm 'principal' "$path bdm"
        Assert-Condition ($principal -cnotmatch $blockPrincipalPattern) `
            "$path unexpectedly contains a deployable Block principal"
        Assert-Condition $principal.StartsWith('INVALID_') `
            "$path does not carry an explicit fail-closed principal marker"
    }

    $manifest = Get-Content -LiteralPath $manifestTemplatePath -Raw -Encoding utf8
    Assert-Condition $manifest.Contains('INVALID_USE_SECURE_IDENTITY_BUNDLE') `
        'BDM identity manifest lacks its fail-closed marker'
    Assert-Condition (
        $manifest -cnotmatch '(?m)^\s*principal:\s*(?:blk|bdm)-[0-9a-f]{32}\s*$'
    ) 'BDM identity manifest unexpectedly contains a deployable principal'

    $activeAclLines = @(
        Get-Content -LiteralPath $aclTemplatePath -Encoding utf8 |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -and -not $_.StartsWith('#') }
    )
    Assert-Condition ($activeAclLines.Count -eq 0) `
        'checked-in BDM ACL is not empty and fail-closed'

    $registration = Get-Content -LiteralPath $registrationTemplatePath -Raw -Encoding utf8
    Assert-Condition $registration.Contains('RAISE EXCEPTION') `
        'checked-in BDM registration SQL is not guaranteed to fail'
    Assert-Condition ($registration -cnotmatch 'blk-[0-9a-f]{32}') `
        'checked-in BDM registration SQL unexpectedly contains a deployable principal'

    Assert-Rejected 'checked-in fail-closed templates as deployable identity inputs' {
        Test-IdentityConsistency `
            -BlockConfigPaths $checkedInBlockConfigs `
            -IdentityMapPath $manifestTemplatePath `
            -AclPath $aclTemplatePath `
            -RegistrationPath $registrationTemplatePath
    }
    Write-Output 'OK: checked-in Block and BDM identity templates are fail-closed'
}

function New-TestIdentityBundle {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [string]$BlockPrincipal,

        [Parameter(Mandatory)]
        [string]$BdmPrincipal
    )

    $siteId = 'site-lab'
    $blockId = 'block-001'
    $deviceId = 'device-001'
    $root = Join-Path $tempRoot "$Name-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $root | Out-Null

    $configPaths = @()
    $documentationRoutes = @('192.0.2.10', '198.51.100.20')
    for ($index = 0; $index -lt $checkedInBlockConfigs.Count; $index++) {
        $config = Get-Content -LiteralPath $checkedInBlockConfigs[$index] -Raw -Encoding utf8 |
            ConvertFrom-Json
        $config.bdm.principal = $BlockPrincipal
        $config.bdm.endpoint = "mqtts://$($documentationRoutes[$index]):8883"
        $configPath = Join-Path $root $blockConfigNames[$index]
        $config | ConvertTo-Json -Depth 20 |
            Set-Content -LiteralPath $configPath -Encoding utf8
        $configPaths += $configPath
    }

    $identityMapPath = Join-Path $root 'identity-map.env'
    @(
        "BDM_MQTT_CLIENT_ID=$BdmPrincipal",
        "BLOCK_MQTT_CLIENT_ID=$BlockPrincipal",
        "BDM_SITE_ID=$siteId",
        "BDM_BLOCK_ID=$blockId",
        "BDM_DEVICE_ID=$deviceId"
    ) | Set-Content -LiteralPath $identityMapPath -Encoding utf8

    $aclPath = Join-Path $root 'bdm-v1.acl'
    $acl = [System.Collections.Generic.List[string]]::new()
    $acl.Add("user $BlockPrincipal")
    foreach ($channel in $uplinkChannels) {
        $acl.Add("topic write bdm/v1/sites/$siteId/blocks/$blockId/up/$channel")
    }
    $acl.Add("topic read bdm/v1/sites/$siteId/blocks/$blockId/down/sync")
    $acl.Add('')
    $acl.Add("user $BdmPrincipal")
    foreach ($channel in $uplinkChannels) {
        $acl.Add("topic read bdm/v1/sites/$siteId/blocks/$blockId/up/$channel")
    }
    $acl.Add("topic write bdm/v1/sites/$siteId/blocks/$blockId/down/sync")
    $acl | Set-Content -LiteralPath $aclPath -Encoding utf8

    $registrationPath = Join-Path $root 'register-block.sql'
    @(
        'BEGIN;',
        'INSERT INTO bdm_block_principals(principal, site_id, block_id, device_id)',
        "VALUES ('$BlockPrincipal', '$siteId', '$blockId', '$deviceId')",
        'ON CONFLICT DO NOTHING;',
        'DO $registration$',
        'BEGIN',
        '    IF NOT EXISTS (',
        '        SELECT 1 FROM bdm_block_principals',
        "        WHERE principal = '$BlockPrincipal'",
        "          AND site_id = '$siteId'",
        "          AND block_id = '$blockId'",
        "          AND device_id = '$deviceId'",
        '          AND enabled',
        '    ) THEN',
        "        RAISE EXCEPTION 'principal is already bound differently or disabled';",
        '    END IF;',
        'END',
        '$registration$;',
        'COMMIT;'
    ) | Set-Content -LiteralPath $registrationPath -Encoding utf8

    return [pscustomobject]@{
        Root = $root
        BlockConfigPaths = $configPaths
        IdentityMapPath = $identityMapPath
        AclPath = $aclPath
        RegistrationPath = $registrationPath
    }
}

function Set-JsonConfig {
    param(
        [Parameter(Mandatory)]
        [string]$Path,

        [Parameter(Mandatory)]
        [scriptblock]$Mutation
    )

    $config = Get-Content -LiteralPath $Path -Raw -Encoding utf8 | ConvertFrom-Json
    & $Mutation $config
    $config | ConvertTo-Json -Depth 20 |
        Set-Content -LiteralPath $Path -Encoding utf8
}

Assert-CheckedInTemplatesFailClosed

# Build non-secret, non-routable test identities only at runtime so no usable
# principal or real route is checked into either repository.
$testBlockPrincipal = 'blk-' + ('1' * 32)
$testBdmPrincipal = 'bdm-' + ('2' * 32)
$positive = New-TestIdentityBundle `
    -Name 'positive' `
    -BlockPrincipal $testBlockPrincipal `
    -BdmPrincipal $testBdmPrincipal
Test-IdentityConsistency `
    -BlockConfigPaths $positive.BlockConfigPaths `
    -IdentityMapPath $positive.IdentityMapPath `
    -AclPath $positive.AclPath `
    -RegistrationPath $positive.RegistrationPath
Write-Output 'OK: runtime-only Block/BDM identity bundle is consistent across distinct documentation routes'

$crossBlock = New-TestIdentityBundle `
    -Name 'cross-block' `
    -BlockPrincipal $testBlockPrincipal `
    -BdmPrincipal $testBdmPrincipal
$crossBlockAcl = Get-Content -LiteralPath $crossBlock.AclPath -Raw -Encoding utf8
$crossBlockAcl = $crossBlockAcl.Replace(
    'blocks/block-001/up/snapshot',
    'blocks/block-002/up/snapshot'
)
Set-Content -LiteralPath $crossBlock.AclPath -Value $crossBlockAcl -Encoding utf8
Assert-Rejected 'cross-Block ACL topic' {
    Test-IdentityConsistency `
        -BlockConfigPaths $crossBlock.BlockConfigPaths `
        -IdentityMapPath $crossBlock.IdentityMapPath `
        -AclPath $crossBlock.AclPath `
        -RegistrationPath $crossBlock.RegistrationPath
}

$missingPrincipal = New-TestIdentityBundle `
    -Name 'missing-principal' `
    -BlockPrincipal $testBlockPrincipal `
    -BdmPrincipal $testBdmPrincipal
Set-JsonConfig -Path $missingPrincipal.BlockConfigPaths[0] -Mutation {
    param($config)
    $config.bdm.PSObject.Properties.Remove('principal')
}
Assert-Rejected 'missing Block principal' {
    Test-IdentityConsistency `
        -BlockConfigPaths $missingPrincipal.BlockConfigPaths `
        -IdentityMapPath $missingPrincipal.IdentityMapPath `
        -AclPath $missingPrincipal.AclPath `
        -RegistrationPath $missingPrincipal.RegistrationPath
}

$wrongPrincipal = New-TestIdentityBundle `
    -Name 'wrong-principal' `
    -BlockPrincipal $testBlockPrincipal `
    -BdmPrincipal $testBdmPrincipal
Set-JsonConfig -Path $wrongPrincipal.BlockConfigPaths[1] -Mutation {
    param($config)
    $config.bdm.principal = 'blk-' + ('3' * 32)
}
Assert-Rejected 'wrong but well-formed Block principal' {
    Test-IdentityConsistency `
        -BlockConfigPaths $wrongPrincipal.BlockConfigPaths `
        -IdentityMapPath $wrongPrincipal.IdentityMapPath `
        -AclPath $wrongPrincipal.AclPath `
        -RegistrationPath $wrongPrincipal.RegistrationPath
}

Write-Output 'PASS: fail-closed templates and runtime Block/BDM identity consistency gates'
