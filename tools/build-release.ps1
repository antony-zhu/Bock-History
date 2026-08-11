[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$Version,
    [string]$StateRoot = "",
    [switch]$FreshState,
    [switch]$AllowDirty,
    [ValidateSet("default", "simulatorFloat32")]
    [string]$PLCProfile = "default",
    [string]$GoProxy = "https://goproxy.cn|https://proxy.golang.org|direct",
    [string]$GitBash = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "build-state.ps1")

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($StateRoot)) {
    $StateRoot = Get-BlockBuildReleaseStateRoot -RepoRoot $repoRoot -Version $Version
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

function Assert-ByteEqual([string]$Expected, [string]$Actual, [string]$Description) {
    if (-not (Test-Path -LiteralPath $Actual -PathType Leaf)) {
        throw "$Description did not produce $Actual."
    }
    $expectedBytes = [System.IO.File]::ReadAllBytes($Expected)
    $actualBytes = [System.IO.File]::ReadAllBytes($Actual)
    if ($expectedBytes.Length -ne $actualBytes.Length) {
        throw "$Description differs from the tracked runtime asset (length $($actualBytes.Length) instead of $($expectedBytes.Length))."
    }
    for ($index = 0; $index -lt $expectedBytes.Length; $index++) {
        if ($expectedBytes[$index] -ne $actualBytes[$index]) {
            throw "$Description differs from the tracked runtime asset at byte $index."
        }
    }
}

function Get-GitValue([string[]]$Arguments, [string]$Description) {
    $value = @(& git -C $repoRoot @Arguments)
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed. Git is required for formal release provenance."
    }
    return ($value -join "`n").Trim()
}

function Get-GitSourceMetadata {
    $status = @(& git -C $repoRoot status --porcelain=v1 --untracked-files=all --ignore-submodules=none)
    if ($LASTEXITCODE -ne 0) {
        throw "Git status failed. Git is required for formal release provenance."
    }
    $isClean = $status.Count -eq 0
    if (-not $isClean -and -not $AllowDirty) {
        throw "Formal release builds require a clean tracked and untracked worktree. Commit or remove the changes, then rerun. -AllowDirty is development-only and must not be used for an archival release hash."
    }
    return [pscustomobject]@{
        Commit = Get-GitValue @("rev-parse", "HEAD") "Git commit lookup"
        Tree = Get-GitValue @("rev-parse", "HEAD^{tree}") "Git tree lookup"
        WorktreeClean = $isClean
        AllowDirty = [bool]$AllowDirty
    }
}

function Resolve-GitForWindowsBash([string]$Candidate, [string]$Source, [switch]$Required) {
    if ([string]::IsNullOrWhiteSpace($Candidate)) {
        return $null
    }

    $bash = [System.IO.Path]::GetFullPath($Candidate)
    if (-not (Test-Path -LiteralPath $bash -PathType Leaf)) {
        if ($Required) {
            throw "$Source Git Bash path does not exist: $bash"
        }
        return $null
    }
    if (-not [System.IO.Path]::GetFileName($bash).Equals("bash.exe", [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Source must name Git for Windows bash.exe: $bash"
    }

    $windowsDirectory = [System.IO.Path]::GetFullPath($env:WINDIR)
    $windowsPrefix = $windowsDirectory.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if ($bash.StartsWith($windowsPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Source must not use a WSL or Windows system bash.exe: $bash"
    }

    $bashDirectory = Split-Path -Parent $bash
    if (-not (Split-Path -Leaf $bashDirectory).Equals("bin", [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Source is not a Git for Windows bin/bash.exe or usr/bin/bash.exe path: $bash"
    }
    $bashParent = Split-Path -Parent $bashDirectory
    $installRoot = if ((Split-Path -Leaf $bashParent).Equals("usr", [System.StringComparison]::OrdinalIgnoreCase)) {
        Split-Path -Parent $bashParent
    } else {
        $bashParent
    }
    $cygpath = Join-Path $installRoot "usr\bin\cygpath.exe"
    if (-not (Test-Path -LiteralPath $cygpath -PathType Leaf)) {
        throw "$Source is not a usable Git for Windows installation because cygpath.exe is missing: $cygpath"
    }

    $probe = @(& $cygpath -u $installRoot)
    if ($LASTEXITCODE -ne 0 -or $probe.Count -eq 0 -or [string]::IsNullOrWhiteSpace(($probe -join "").Trim())) {
        throw "$Source Git for Windows cygpath.exe is not executable: $cygpath"
    }
    return [pscustomobject]@{
        Bash = $bash
        Cygpath = $cygpath
        InstallRoot = $installRoot
    }
}

function Get-GitBash([string]$ExplicitPath) {
    if (-not [string]::IsNullOrWhiteSpace($ExplicitPath)) {
        return Resolve-GitForWindowsBash -Candidate $ExplicitPath -Source "-GitBash" -Required
    }
    if (-not [string]::IsNullOrWhiteSpace($env:BLOCK_GIT_BASH)) {
        return Resolve-GitForWindowsBash -Candidate $env:BLOCK_GIT_BASH -Source "BLOCK_GIT_BASH" -Required
    }

    $candidates = [System.Collections.Generic.List[string]]::new()
    $gitCommand = Get-Command git.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $gitCommand -and -not [string]::IsNullOrWhiteSpace($gitCommand.Source)) {
        $gitDirectory = Split-Path -Parent ([System.IO.Path]::GetFullPath($gitCommand.Source))
        $gitDirectoryName = Split-Path -Leaf $gitDirectory
        if ($gitDirectoryName.Equals("cmd", [System.StringComparison]::OrdinalIgnoreCase) -or
            $gitDirectoryName.Equals("bin", [System.StringComparison]::OrdinalIgnoreCase)) {
            $gitParent = Split-Path -Parent $gitDirectory
            $installRoot = if ((Split-Path -Leaf $gitParent).Equals("mingw64", [System.StringComparison]::OrdinalIgnoreCase) -or
                (Split-Path -Leaf $gitParent).Equals("usr", [System.StringComparison]::OrdinalIgnoreCase)) {
                Split-Path -Parent $gitParent
            } else {
                $gitParent
            }
            $candidates.Add((Join-Path $installRoot "bin\bash.exe"))
            $candidates.Add((Join-Path $installRoot "usr\bin\bash.exe"))
        }
    }
    $candidates.Add("C:\Program Files\Git\bin\bash.exe")
    $candidates.Add("C:\Program Files\Git\usr\bin\bash.exe")

    foreach ($candidate in ($candidates | Select-Object -Unique)) {
        $resolved = Resolve-GitForWindowsBash -Candidate $candidate -Source "Git for Windows discovery"
        if ($null -ne $resolved) {
            return $resolved
        }
    }
    throw "Git for Windows (bash.exe and executable cygpath.exe) is required to run deploy/block/build.sh from Windows. Install it anywhere on the machine, expose git.exe on PATH, or pass -GitBash / set BLOCK_GIT_BASH."
}

function Write-Utf8NoBom([string]$Path, [string]$Content) {
    [System.IO.File]::WriteAllText($Path, $Content + [Environment]::NewLine, (New-Object System.Text.UTF8Encoding($false)))
}

$inheritedGitConfigNames = Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'
$inheritedGitConfigSnapshot = Get-EnvironmentSnapshot $inheritedGitConfigNames
$clearInheritedGitConfig = [ordered]@{}
foreach ($name in $inheritedGitConfigNames) {
    $clearInheritedGitConfig[$name] = $null
}

try {
    Set-EnvironmentValues $clearInheritedGitConfig
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "Formal release build"

    $gitSource = Get-GitSourceMetadata
    $tools = & (Join-Path $PSScriptRoot "bootstrap-build-tools.ps1") `
        -StateRoot $StateRoot -FreshState:$FreshState -PrepareGoModules -PrepareTypeScript -GoProxy $GoProxy
    if ($null -eq $tools) {
        throw "Build tool bootstrap did not return a toolchain."
    }
    $StateRoot = $tools.StateRoot

    $environmentNames = @($tools.Environment.Keys) + @($tools.ClearEnvironmentNames) + @(
        "CGO_ENABLED", "GOOS", "GOARCH", "BLOCK_GO_BIN", "BLOCK_PLC_PROFILE", "BLOCK_ARTIFACT",
        "BLOCK_STATE_ROOT", "BLOCK_NODE_BIN", "CC", "CXX"
    )
    $environmentSnapshot = Get-EnvironmentSnapshot $environmentNames

    try {
    $clearInheritedNodeEnvironment = [ordered]@{}
    foreach ($name in $tools.ClearEnvironmentNames) {
        $clearInheritedNodeEnvironment[$name] = $null
    }
    Set-EnvironmentValues $clearInheritedNodeEnvironment
    Set-EnvironmentValues $tools.Environment
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "Formal release build"
    Set-EnvironmentValues ([ordered]@{
        CGO_ENABLED = "0"
        GOOS        = $null
        GOARCH      = $null
        CC          = $null
        CXX         = $null
    })

    if ($null -eq $tools.TypeScriptBinary) {
        throw "The locked TypeScript compiler was not prepared."
    }

    $typeScriptRoot = New-BlockBuildStateDirectory -StateRoot $StateRoot -RelativePath "typescript" -Description "TypeScript output directory"
    $hmiOutput = New-BlockBuildStateDirectory -StateRoot $StateRoot -RelativePath "typescript\hmi" -Description "HMI TypeScript output directory"
    $simulatorOutput = New-BlockBuildStateDirectory -StateRoot $StateRoot -RelativePath "typescript\plc-simulator" -Description "PLC simulator TypeScript output directory"

    Invoke-Checked $tools.TypeScriptBinary @("-p", (Join-Path $repoRoot "apps\block-hmi\tsconfig.json"), "--outDir", $hmiOutput) "TypeScript compile for Block HMI"
    Assert-ByteEqual `
        (Join-Path $repoRoot "apps\block-hmi\assets\hmi.mjs") `
        (Join-Path $hmiOutput "hmi.mjs") `
        "Block HMI TypeScript compile"

    Invoke-Checked $tools.TypeScriptBinary @("-p", (Join-Path $repoRoot "tools\plc-simulator\tsconfig.json"), "--outDir", $simulatorOutput) "TypeScript compile for PLC simulator"
    Assert-ByteEqual `
        (Join-Path $repoRoot "tools\plc-simulator\web\app.js") `
        (Join-Path $simulatorOutput "app.js") `
        "PLC simulator TypeScript compile"

    Invoke-Checked $tools.NodeBinary @((Join-Path $repoRoot "apps\block-hmi\assets\hmi.test.mjs")) "Block HMI Node tests"
    foreach ($moduleRelativePath in @("apps\block-hmi", "services\block-agent", "tools\plc-simulator")) {
        $modulePath = Join-Path $repoRoot $moduleRelativePath
        Invoke-Checked $tools.GoBinary @("-C", $modulePath, "test", "-buildvcs=false", "-mod=readonly", "./...") "go test in $moduleRelativePath"
    }
    Invoke-Checked $tools.GoBinary @("-C", (Join-Path $repoRoot "services\block-agent"), "vet", "-buildvcs=false", "-mod=readonly", "./...") "go vet in services/block-agent"

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
    $gitBashTools = Get-GitBash -ExplicitPath $GitBash
    $bash = $gitBashTools.Bash
    $cygpath = $gitBashTools.Cygpath
    $env:BLOCK_GO_BIN = (& $cygpath -u $tools.GoBinary).Trim()
    $env:BLOCK_NODE_BIN = (& $cygpath -u $tools.NodeBinary).Trim()
    $env:BLOCK_STATE_ROOT = (& $cygpath -u $StateRoot).Trim()
    if ($PLCProfile -eq "default") {
        [Environment]::SetEnvironmentVariable("BLOCK_PLC_PROFILE", $null, "Process")
    } else {
        $env:BLOCK_PLC_PROFILE = $PLCProfile
    }

    $artifactDirectory = Get-BlockBuildStateChildPath -StateRoot $StateRoot -RelativePath "artifact" -Description "Release artifact directory"
    $artifactDirectoryForBash = (& $cygpath -u $artifactDirectory).Trim()
    Invoke-Checked $bash @((Join-Path $repoRoot "deploy\block\build.sh"), "--output", $artifactDirectoryForBash, "--version", $Version) "Linux ARM64 release build"
    $env:BLOCK_ARTIFACT = $artifactDirectoryForBash
    Invoke-Checked $bash @("-lc", 'file "$BLOCK_ARTIFACT/bin/block-agent" | grep -Eq "ELF .*ARM aarch64"') "ARM64 ELF verification"

    $hmiHash = (Get-FileHash -LiteralPath (Join-Path $repoRoot "apps\block-hmi\assets\hmi.mjs") -Algorithm SHA256).Hash.ToLowerInvariant()
    $simulatorHash = (Get-FileHash -LiteralPath (Join-Path $repoRoot "tools\plc-simulator\web\app.js") -Algorithm SHA256).Hash.ToLowerInvariant()
    $blockAgentHash = (Get-FileHash -LiteralPath (Join-Path $artifactDirectory "bin\block-agent") -Algorithm SHA256).Hash.ToLowerInvariant()
    $metadataPath = Get-BlockBuildStateChildPath -StateRoot $StateRoot -RelativePath "artifact\build-metadata.json" -Description "Release build metadata"
    $metadata = [ordered]@{
        schema = "block-release-build-metadata-v1"
        version = $Version
        source = [ordered]@{
            commit = $gitSource.Commit
            tree = $gitSource.Tree
            worktreeClean = $gitSource.WorktreeClean
            allowDirty = $gitSource.AllowDirty
        }
        toolchains = [ordered]@{
            go = $tools.GoVersion
            node = $tools.NodeVersion
            typescript = $tools.TypeScriptVersion
        }
        controlledEnvironment = [ordered]@{
            goenv = "off"
            gowork = "off"
            go111module = "on"
            goroot = "cleared"
            gotoolchain = "local"
            goflags = "-mod=readonly"
            goproxy = "off"
            gosumdb = "off"
            goarm64 = "v8.0"
            cgoEnabled = "0"
            goos = "linux"
            goarch = "arm64"
            buildvcs = "false"
            moduleChecksumDatabase = "sum.golang.org"
            nodeTlsOverrides = "cleared"
            gitConfigInjection = "all inherited GIT_CONFIG_* variables cleared"
            transportProxyPolicy = "HTTP(S) proxy and normal Git proxy configuration may be inherited, are not recorded, affect only the transport path, and do not replace tool SHA-256, package-lock, go.mod/go.sum, or sum.golang.org verification."
        }
        runtimeAssets = [ordered]@{
            hmiSha256 = $hmiHash
            simulatorAppSha256 = $simulatorHash
        }
        artifact = [ordered]@{
            blockAgentSha256 = $blockAgentHash
        }
    }
    Write-Utf8NoBom $metadataPath ($metadata | ConvertTo-Json -Depth 6)

    [pscustomobject]@{
        StateRoot           = $StateRoot
        Version             = $Version
        Commit              = $gitSource.Commit
        Tree                = $gitSource.Tree
        WorktreeClean       = $gitSource.WorktreeClean
        AllowDirty          = $gitSource.AllowDirty
        GoVersion           = $tools.GoVersion
        NodeVersion         = $tools.NodeVersion
        TypeScriptVersion   = $tools.TypeScriptVersion
        HMI_SHA256          = $hmiHash
        SimulatorApp_SHA256 = $simulatorHash
        BlockAgent_SHA256   = $blockAgentHash
        ArtifactDirectory   = $artifactDirectory
        MetadataPath        = $metadataPath
    }
    } finally {
        Restore-Environment $environmentSnapshot
    }
} finally {
    Restore-Environment $inheritedGitConfigSnapshot
}
