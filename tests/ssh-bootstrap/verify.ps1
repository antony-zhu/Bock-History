[CmdletBinding()]
param(
    [string]$StateRoot = "",
    [switch]$FreshState,
    [string]$CommonRoot = "",
    [switch]$SkipRace,
    [string]$GoProxy = "https://goproxy.cn|https://proxy.golang.org|direct"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "..\..\tools\build-state.ps1")

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
if ([string]::IsNullOrWhiteSpace($StateRoot)) {
    $StateRoot = Join-Path $repoRoot ".cache\ssh-bootstrap-verify"
}
if ([string]::IsNullOrWhiteSpace($CommonRoot)) {
    throw "SSH Bootstrap contract verification is optional and requires an explicit -CommonRoot pointing to a Common checkout that contains the commit in Block/COMMON_BASELINE. Formal build-release.ps1 does not require Common."
}

function Set-EnvironmentValues([System.Collections.IDictionary]$Values) {
    foreach ($name in $Values.Keys) {
        [Environment]::SetEnvironmentVariable($name, $Values[$name], "Process")
    }
}

function Get-EnvironmentSnapshot([string[]]$Names) {
    $snapshot = @{}
    foreach ($name in ($Names | Select-Object -Unique)) {
        $snapshot[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    }
    return $snapshot
}

function Restore-Environment([hashtable]$Snapshot) {
    foreach ($name in $Snapshot.Keys) {
        [Environment]::SetEnvironmentVariable($name, $Snapshot[$name], "Process")
    }
}

function Invoke-Checked([string]$Executable, [string[]]$Arguments, [string]$Description) {
    & $Executable @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
    }
}

function Get-CommonBaselineCommit {
    $baselinePath = Join-Path $repoRoot "COMMON_BASELINE"
    if (-not (Test-Path -LiteralPath $baselinePath -PathType Leaf)) {
        throw "Block COMMON_BASELINE is missing: $baselinePath"
    }
    $match = [regex]::Match((Get-Content -LiteralPath $baselinePath -Raw -Encoding utf8), '(?m)^commit:\s*([0-9a-f]{40})\s*$')
    if (-not $match.Success) {
        throw "Block COMMON_BASELINE does not declare a 40-character commit: $baselinePath"
    }
    return $match.Groups[1].Value
}

function Resolve-CommonRepository([string]$Path, [string]$Commit) {
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "Pinned Common repository is required for SSH contract validation. Clone or provide it with -CommonRoot; expected $Path at commit $Commit. Common current HEAD is never used."
    }
    $resolved = (Resolve-Path -LiteralPath $Path).Path
    $isRepository = (& git -C $resolved rev-parse --is-inside-work-tree 2>$null).Trim()
    if ($LASTEXITCODE -ne 0 -or $isRepository -ne "true") {
        throw "-CommonRoot must be a Git checkout containing the pinned Common commit ${Commit}: $resolved"
    }
    & git -C $resolved cat-file -e ("{0}^{{commit}}" -f $Commit) 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw "Common checkout does not contain the pinned commit $Commit from Block/COMMON_BASELINE. Fetch that commit, then retry; Common current HEAD is not accepted."
    }
    return $resolved
}

function Get-PinnedContractFiles([string]$Repository, [string]$Commit) {
    $prefix = "contracts/ssh-bootstrap/v1"
    $files = @(& git -C $Repository ls-tree -r --name-only $Commit -- $prefix)
    if ($LASTEXITCODE -ne 0 -or $files.Count -eq 0) {
        throw "Pinned Common commit $Commit does not contain $prefix."
    }
    foreach ($file in $files) {
        if (-not $file.StartsWith($prefix + "/", [System.StringComparison]::Ordinal) -or $file.Contains("..")) {
            throw "Pinned Common contract contains an unsafe path: $file"
        }
    }
    return $files
}

function Write-GitBlob([string]$Repository, [string]$ObjectName, [string]$Destination) {
    $escapedRepository = $Repository.Replace('"', '\"')
    $escapedObjectName = $ObjectName.Replace('"', '\"')
    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = "git"
    $startInfo.Arguments = ('-C "{0}" show "{1}"' -f $escapedRepository, $escapedObjectName)
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $startInfo
    [void]$process.Start()
    $destinationStream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try {
        $process.StandardOutput.BaseStream.CopyTo($destinationStream)
    } finally {
        $destinationStream.Dispose()
    }
    $standardError = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) {
        throw "git show failed for pinned Common object ${ObjectName}: $standardError"
    }
}

function Materialize-PinnedCommonContract([string]$Repository, [string]$Commit, [string[]]$Files, [string]$VerifiedStateRoot) {
    $prefix = "contracts/ssh-bootstrap/v1/"
    $contractRoot = New-BlockBuildStateDirectory -StateRoot $VerifiedStateRoot -RelativePath "common-contract" -Description "Pinned Common contract directory"
    foreach ($file in $Files) {
        $relative = $file.Substring($prefix.Length).Replace('/', '\\')
        $destination = Get-BlockBuildStateChildPath -StateRoot $VerifiedStateRoot -RelativePath (Join-Path "common-contract" $relative) -Description "Pinned Common contract file"
        $destinationDirectory = Split-Path -Parent $destination
        New-Item -ItemType Directory -Force -Path $destinationDirectory | Out-Null
        Write-GitBlob -Repository $Repository -ObjectName ("{0}:{1}" -f $Commit, $file) -Destination $destination
    }
    $validator = Join-Path $contractRoot "validate_contract.mjs"
    if (-not (Test-Path -LiteralPath $validator -PathType Leaf)) {
        throw "Pinned Common contract validator was not materialized: $validator"
    }
    return $validator
}

function Get-CCompiler {
    foreach ($name in @("gcc.exe", "clang.exe", "gcc", "clang")) {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace($command.Source)) {
            return $command.Source
        }
    }
    return ""
}

$inheritedGitConfigNames = Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'
$inheritedGitConfigSnapshot = Get-EnvironmentSnapshot $inheritedGitConfigNames
$clearInheritedGitConfig = [ordered]@{}
foreach ($name in $inheritedGitConfigNames) {
    $clearInheritedGitConfig[$name] = $null
}

try {
    Set-EnvironmentValues $clearInheritedGitConfig
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "SSH Bootstrap verification"

    $commonCommit = Get-CommonBaselineCommit
    $commonRepository = Resolve-CommonRepository $CommonRoot $commonCommit
    $pinnedContractFiles = Get-PinnedContractFiles $commonRepository $commonCommit
    $tools = & (Join-Path $repoRoot "tools\bootstrap-build-tools.ps1") `
        -StateRoot $StateRoot -FreshState:$FreshState -PrepareGoModules -GoProxy $GoProxy
    if ($null -eq $tools) {
        throw "Build tool bootstrap did not return a toolchain."
    }
    $StateRoot = $tools.StateRoot

    $environmentNames = @($tools.Environment.Keys) + @($tools.ClearEnvironmentNames) + @(
        "CGO_ENABLED", "GOOS", "GOARCH", "GOARM64", "CC", "CXX"
    )
    $environmentSnapshot = Get-EnvironmentSnapshot $environmentNames

    try {
    $clearInheritedNodeEnvironment = [ordered]@{}
    foreach ($name in $tools.ClearEnvironmentNames) {
        $clearInheritedNodeEnvironment[$name] = $null
    }
    Set-EnvironmentValues $clearInheritedNodeEnvironment
    Set-EnvironmentValues $tools.Environment
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "SSH Bootstrap verification"
    Set-EnvironmentValues ([ordered]@{
        CGO_ENABLED = $null
        GOOS        = $null
        GOARCH      = $null
        CC          = $null
        CXX         = $null
    })

    $commonContract = Materialize-PinnedCommonContract -Repository $commonRepository -Commit $commonCommit `
        -Files $pinnedContractFiles -VerifiedStateRoot $StateRoot
    Invoke-Checked $tools.NodeBinary @($commonContract) "Pinned SSH Bootstrap Common contract validation"

    $agentDirectory = Join-Path $repoRoot "services\block-agent"
    Invoke-Checked $tools.GoBinary @("-C", $agentDirectory, "test", "-buildvcs=false", "-mod=readonly", "./...") "Block Agent tests"
    Invoke-Checked $tools.GoBinary @("-C", $agentDirectory, "vet", "-buildvcs=false", "-mod=readonly", "./...") "Block Agent vet"

    $raceResult = "skipped by explicit -SkipRace (not an SSH race validation)"
    if (-not $SkipRace) {
        $cCompiler = Get-CCompiler
        if ([string]::IsNullOrWhiteSpace($cCompiler)) {
            throw "SSH race validation requires a local gcc or clang C compiler because go test -race enables cgo. Install or expose one, then rerun; -SkipRace is only for ordinary build verification and does not satisfy the SSH race gate."
        }
        Set-EnvironmentValues ([ordered]@{
            CGO_ENABLED = "1"
            CC = $cCompiler
        })
        Invoke-Checked $tools.GoBinary @("-C", $agentDirectory, "test", "-buildvcs=false", "-mod=readonly", "-race", "-p", "1", "./...") "Block Agent race tests"
        $raceResult = "passed with explicit local C compiler prerequisite"
    }

    Set-EnvironmentValues ([ordered]@{
        CGO_ENABLED = "0"
        GOOS        = "linux"
        GOARCH      = "arm64"
        GOARM64     = "v8.0"
        GOTOOLCHAIN = "local"
        GOENV       = "off"
        GO111MODULE = "on"
        GOROOT      = $null
        GOFLAGS     = "-mod=readonly"
        GOPROXY     = "off"
        GOSUMDB     = "off"
    })
    $binaryDirectory = New-BlockBuildStateDirectory -StateRoot $StateRoot -RelativePath "bin" -Description "SSH Bootstrap binary directory"
    $binary = Join-Path $binaryDirectory "ssh-bootstrapd"
    Invoke-Checked $tools.GoBinary @("-C", $agentDirectory, "build", "-buildvcs=false", "-mod=readonly", "-trimpath", "-ldflags", "-X main.version=verification", "-o", $binary, "./cmd/ssh-bootstrapd") "SSH Bootstrap linux/arm64 build"

    Write-Output "Block SSH Bootstrap contract pinned to Common $commonCommit, Go test/vet, $raceResult, and linux/arm64 GOARM64=v8.0 build passed. StateRoot: $StateRoot"
    } finally {
        Restore-Environment $environmentSnapshot
    }
} finally {
    Restore-Environment $inheritedGitConfigSnapshot
}
