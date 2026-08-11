[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "build-state.ps1")

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$cacheRoot = ConvertTo-BlockBuildCanonicalPath (Join-Path $repoRoot ".cache")
$firstVersion = "release-state-root-a-20260811"
$secondVersion = "release-state-root-b-20260811"
$first = Get-BlockBuildReleaseStateRoot -RepoRoot $repoRoot -Version $firstVersion
$second = Get-BlockBuildReleaseStateRoot -RepoRoot $repoRoot -Version $secondVersion

$fixtureID = [Guid]::NewGuid().ToString("N")
$emptyFixture = Join-Path $cacheRoot ("state-root-empty-fixture-" + $fixtureID)
$nonEmptyFixture = Join-Path $cacheRoot ("state-root-nonempty-fixture-" + $fixtureID)
try {
    New-Item -ItemType Directory -Path $emptyFixture -Force | Out-Null
    $claimedEmptyFixture = Resolve-BlockBuildStateRoot -RepoRoot $repoRoot -StateRoot $emptyFixture `
        -Owner "block-build-tools" -FreshState
    if (-not (Test-BlockBuildStateOwner -StateRoot $claimedEmptyFixture -Owner "block-build-tools" -RepoRoot $repoRoot)) {
        throw "-FreshState must claim an empty unowned StateRoot by writing this tool's owner marker."
    }
    $claimedEntries = @(Get-ChildItem -LiteralPath $claimedEmptyFixture -Force)
    if ($claimedEntries.Count -ne 1 -or $claimedEntries[0].Name -ne ".block-build-state.json") {
        throw "Claiming an empty StateRoot must leave only the owner marker."
    }

    New-Item -ItemType Directory -Path $nonEmptyFixture -Force | Out-Null
    New-Item -ItemType File -Path (Join-Path $nonEmptyFixture "interrupted-data") -Force | Out-Null
    $nonEmptyRejected = $false
    try {
        Resolve-BlockBuildStateRoot -RepoRoot $repoRoot -StateRoot $nonEmptyFixture `
            -Owner "block-build-tools" -FreshState | Out-Null
    } catch {
        $nonEmptyRejected = $true
    }
    if (-not $nonEmptyRejected) {
        throw "-FreshState must reject a non-empty unowned StateRoot."
    }
} finally {
    foreach ($fixture in @($emptyFixture, $nonEmptyFixture)) {
        if (Test-Path -LiteralPath $fixture) {
            Assert-BlockBuildNoReparseDescendants -Path $fixture -Description "StateRoot policy test fixture"
            Remove-Item -LiteralPath $fixture -Recurse -Force
        }
    }
}

if ($first.Equals($second, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Different release versions must not share a default StateRoot."
}
foreach ($candidate in @($first, $second)) {
    if (-not (Test-BlockBuildChildPath $candidate $cacheRoot)) {
        throw "Release StateRoot must remain below the repository cache root: $candidate"
    }
    if (-not (ConvertTo-BlockBuildCanonicalPath (Split-Path -Parent $candidate)).Equals($cacheRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Release StateRoot must be a direct repository cache child: $candidate"
    }
}
if ($first -ne (Join-Path $cacheRoot ("block-release-" + $firstVersion)) -or
    $second -ne (Join-Path $cacheRoot ("block-release-" + $secondVersion))) {
    throw "Release StateRoot does not use the versioned block-release naming policy."
}

$invalidVersionRejected = $false
try {
    Get-BlockBuildReleaseStateRoot -RepoRoot $repoRoot -Version "../escape" | Out-Null
} catch {
    $invalidVersionRejected = $true
}
if (-not $invalidVersionRejected) {
    throw "Release StateRoot must reject a version that is not a safe file name."
}

$gitConfigTestNames = @(
    "GIT_CONFIG_BLOCK_RELEASE_TEST_ARBITRARY",
    "GIT_CONFIG_BLOCK_RELEASE_TEST_9"
)
$proxyTestValues = [ordered]@{
    HTTP_PROXY = "http://127.0.0.1:9"
    HTTPS_PROXY = "http://127.0.0.1:9"
}
$environmentNames = @(
    Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'
) + $gitConfigTestNames + @($proxyTestValues.Keys)
$environmentSnapshot = @{}
foreach ($name in ($environmentNames | Select-Object -Unique)) {
    $environmentSnapshot[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}
try {
    foreach ($name in $gitConfigTestNames) {
        [Environment]::SetEnvironmentVariable($name, "injected-but-not-recorded", "Process")
    }
    foreach ($name in $proxyTestValues.Keys) {
        [Environment]::SetEnvironmentVariable($name, $proxyTestValues[$name], "Process")
    }

    $matchedGitConfigNames = @(Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_')
    foreach ($name in $gitConfigTestNames) {
        if ($matchedGitConfigNames -notcontains $name) {
            throw "Git configuration isolation must find every inherited GIT_CONFIG_* variable."
        }
    }
    foreach ($name in $proxyTestValues.Keys) {
        if ($matchedGitConfigNames -contains $name) {
            throw "Git configuration isolation must not clear HTTP(S) proxy environment variables."
        }
    }

    $clearGitConfig = [ordered]@{}
    foreach ($name in $matchedGitConfigNames) {
        $clearGitConfig[$name] = $null
    }
    foreach ($name in $clearGitConfig.Keys) {
        [Environment]::SetEnvironmentVariable($name, $null, "Process")
    }
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "Git configuration isolation behavior test"
    foreach ($name in $proxyTestValues.Keys) {
        if ([Environment]::GetEnvironmentVariable($name, "Process") -ne $proxyTestValues[$name]) {
            throw "Git configuration isolation must preserve HTTP(S) proxy environment variables."
        }
    }
} finally {
    foreach ($name in ($environmentNames | Select-Object -Unique)) {
        [Environment]::SetEnvironmentVariable($name, $environmentSnapshot[$name], "Process")
    }
}

$releaseSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot "build-release.ps1") -Raw -Encoding utf8
foreach ($required in @(
    'Get-BlockBuildReleaseStateRoot -RepoRoot $repoRoot -Version $Version',
    '"vet", "-buildvcs=false", "-mod=readonly", "./..."',
    '$env:BLOCK_NODE_BIN = (& $cygpath -u $tools.NodeBinary).Trim()',
    '[string]$GitBash = ""',
    '$env:BLOCK_GIT_BASH',
    'Get-Command git.exe -CommandType Application',
    "must not use a WSL or Windows system bash.exe",
    "all inherited GIT_CONFIG_* variables cleared",
    'go111module = "on"',
    'goroot = "cleared"',
    'transportProxyPolicy = '
)) {
    if (-not $releaseSource.Contains($required)) {
        throw "build-release.ps1 is missing required release policy: $required"
    }
}

$buildStateSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot "build-state.ps1") -Raw -Encoding utf8
foreach ($scriptPath in @(
    (Join-Path $PSScriptRoot "bootstrap-build-tools.ps1"),
    (Join-Path $PSScriptRoot "build-release.ps1"),
    (Join-Path $PSScriptRoot "start-block-hmi-auth-demo.ps1"),
    (Join-Path $PSScriptRoot "cleanup-build-artifacts.ps1"),
    (Join-Path $PSScriptRoot "build-state.ps1")
)) {
    $source = Get-Content -LiteralPath $scriptPath -Raw -Encoding utf8
    if ($source.Contains('Join-Path $repoRoot "..\.."') -or $source.Contains('WorkspaceRoot')) {
        throw "$(Split-Path -Leaf $scriptPath) must derive StateRoot from the repository root, not a parent workspace."
    }
}
if (-not $buildStateSource.Contains('Join-Path $canonicalRepoRoot ".cache"')) {
    throw "build-state.ps1 must derive the cache root from RepoRoot."
}

& {
    $tokens = $null
    $parseErrors = $null
    $releaseAst = [System.Management.Automation.Language.Parser]::ParseFile(
        (Join-Path $PSScriptRoot "build-release.ps1"),
        [ref]$tokens,
        [ref]$parseErrors
    )
    if ($parseErrors.Count -ne 0) {
        throw "build-release.ps1 could not be parsed for Git for Windows discovery behavior."
    }
    $functionAsts = @($releaseAst.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -in @("Resolve-GitForWindowsBash", "Get-GitBash")
    }, $true))
    foreach ($functionName in @("Resolve-GitForWindowsBash", "Get-GitBash")) {
        $definition = $functionAsts | Where-Object Name -eq $functionName | Select-Object -First 1
        if ($null -eq $definition) {
            throw "build-release.ps1 is missing Git for Windows function: $functionName"
        }
        . ([scriptblock]::Create($definition.Extent.Text))
    }

    $discovered = Get-GitBash -ExplicitPath ""
    if (-not (Test-Path -LiteralPath $discovered.Bash -PathType Leaf) -or
        -not (Test-Path -LiteralPath $discovered.Cygpath -PathType Leaf)) {
        throw "Git for Windows discovery did not return executable bash.exe and cygpath.exe."
    }
    $originalGitBashOverride = [Environment]::GetEnvironmentVariable("BLOCK_GIT_BASH", "Process")
    try {
        [Environment]::SetEnvironmentVariable("BLOCK_GIT_BASH", $discovered.Bash, "Process")
        $fromEnvironment = Get-GitBash -ExplicitPath ""
        if (-not $fromEnvironment.Bash.Equals($discovered.Bash, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "BLOCK_GIT_BASH must override automatic Git for Windows discovery."
        }
        $fromParameter = Get-GitBash -ExplicitPath $discovered.Bash
        if (-not $fromParameter.Cygpath.Equals($discovered.Cygpath, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "-GitBash must resolve cygpath.exe from the same Git for Windows installation."
        }

        $systemBash = Join-Path $env:WINDIR "System32\bash.exe"
        if (Test-Path -LiteralPath $systemBash -PathType Leaf) {
            $systemBashRejected = $false
            try {
                Get-GitBash -ExplicitPath $systemBash | Out-Null
            } catch {
                $systemBashRejected = $true
            }
            if (-not $systemBashRejected) {
                throw "Git for Windows discovery must reject the WSL/System32 bash.exe."
            }
        }
    } finally {
        [Environment]::SetEnvironmentVariable("BLOCK_GIT_BASH", $originalGitBashOverride, "Process")
    }
}

$bootstrapSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot "bootstrap-build-tools.ps1") -Raw -Encoding utf8
foreach ($required in @(
    'Join-Path $repoRoot ".cache\block-build"',
    'GO111MODULE             = "on"',
    'GOROOT                  = $null',
    'NODE_TLS_REJECT_UNAUTHORIZED = $null',
    'NODE_EXTRA_CA_CERTS     = $null',
    "Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'",
    "Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_'"
)) {
    if (-not $bootstrapSource.Contains($required)) {
        throw "bootstrap-build-tools.ps1 is missing required environment policy: $required"
    }
}
foreach ($required in @(
    '$toolProbeEnvironment = [ordered]@{',
    'NODE_OPTIONS                = $null',
    'GOROOT                      = $null',
    'if ($name -eq "GOENV")',
    '$actualSource = "process environment"'
)) {
    if (-not $bootstrapSource.Contains($required)) {
        throw "bootstrap-build-tools.ps1 is missing required portable-tool probe behavior: $required"
    }
}
if ($bootstrapSource.Contains('GIT_CONFIG_(KEY|VALUE)_') -or
    $bootstrapSource.Contains('"GIT_CONFIG_PARAMETERS"')) {
    throw "bootstrap-build-tools.ps1 must clear every GIT_CONFIG_* name by prefix, not a fixed subset."
}

$authDemoSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot "start-block-hmi-auth-demo.ps1") -Raw -Encoding utf8
foreach ($required in @(
    'Get-EnvironmentSnapshot @($tools.Environment.Keys + $clearEnvironmentNames)',
    '$tools.ClearEnvironmentNames +',
    "Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'",
    'Set-EnvironmentValues $clearInheritedEnvironment',
    'Assert-BlockBuildEnvironmentPatternUnset -Pattern ''^GIT_CONFIG_'' -Description "Block HMI auth demo build"',
    'Restore-Environment $environmentSnapshot'
)) {
    if (-not $authDemoSource.Contains($required)) {
        throw "start-block-hmi-auth-demo.ps1 is missing required bootstrap environment isolation: $required"
    }
}
if ($authDemoSource.Contains('$environmentNames = @(')) {
    throw "start-block-hmi-auth-demo.ps1 must snapshot the bootstrap-returned environment rather than a stale fixed variable list."
}
if (-not $authDemoSource.Contains('if ($Fresh -and $entries.Count -gt 0)')) {
    throw "start-block-hmi-auth-demo.ps1 must allow a first FreshAuth initialization while retaining the non-empty owner-marker gate."
}

$authPersistenceSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot "test-block-hmi-auth-persistence.ps1") -Raw -Encoding utf8
foreach ($required in @(
    '-StateRoot $testRoot -Port $port -DataDirectory $stateDirectory',
    'Resolve-BlockBuildStateRoot -RepoRoot $repoRoot -StateRoot $testRoot',
    '-Owner "block-build-tools" -FreshState'
)) {
    if (-not $authPersistenceSource.Contains($required)) {
        throw "test-block-hmi-auth-persistence.ps1 is not using the same managed StateRoot contract as the demo launcher: $required"
    }
}

$cleanupSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot "cleanup-build-artifacts.ps1") -Raw -Encoding utf8
if (-not $cleanupSource.Contains('RelativePath = "artifact"; AllowLeaf = $false; Kind = "release artifact directory"') -or
    $cleanupSource.Contains('RelativePath = "artifact\bin"')) {
    throw "cleanup-build-artifacts.ps1 must remove the complete managed release artifact directory, not only artifact/bin."
}
foreach ($required in @(
    'RelativePath = "npm-userconfig"; AllowLeaf = $true',
    'RelativePath = "npm-globalconfig"; AllowLeaf = $true'
)) {
    if (-not $cleanupSource.Contains($required)) {
        throw "cleanup-build-artifacts.ps1 must remove bootstrap-generated npm configuration: $required"
    }
}

$sshVerifySource = Get-Content -LiteralPath (Join-Path $repoRoot "tests\ssh-bootstrap\verify.ps1") -Raw -Encoding utf8
if (-not $sshVerifySource.Contains('"vet", "-buildvcs=false", "-mod=readonly", "./..."')) {
    throw "SSH verification must run go vet with -buildvcs=false."
}
foreach ($required in @(
    "Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'",
    "Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_'"
)) {
    if (-not $sshVerifySource.Contains($required)) {
        throw "SSH verification is missing required Git configuration isolation: $required"
    }
}

Write-Output "Release StateRoot policy test passed: $firstVersion and $secondVersion resolve to separate direct children of repository cache $cacheRoot."
