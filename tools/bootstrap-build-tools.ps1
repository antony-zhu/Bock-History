[CmdletBinding()]
param(
    [string]$StateRoot = "",
    [switch]$FreshState,
    [switch]$PrepareGoModules,
    [switch]$PrepareTypeScript,
    [string]$GoProxy = "https://goproxy.cn|https://proxy.golang.org|direct"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "build-state.ps1")

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($StateRoot)) {
    $StateRoot = Join-Path $repoRoot ".cache\block-build"
}
if ([string]::IsNullOrWhiteSpace($GoProxy) -or
    $GoProxy -notmatch '^https://[^|,\s]+(\|https://[^|,\s]+)*\|direct$') {
    throw "GoProxy must be a pipe-fallback HTTPS chain ending in direct, for example https://goproxy.cn|https://proxy.golang.org|direct."
}
$StateRoot = Resolve-BlockBuildStateRoot -RepoRoot $repoRoot `
    -StateRoot $StateRoot -Owner "block-build-tools" -FreshState:$FreshState

function Get-StateChildPath([string]$RelativePath, [string]$Description, [switch]$AllowLeaf) {
    return Get-BlockBuildStateChildPath -StateRoot $StateRoot -RelativePath $RelativePath -Description $Description -AllowLeaf:$AllowLeaf
}

function New-StateDirectory([string]$RelativePath, [string]$Description) {
    return New-BlockBuildStateDirectory -StateRoot $StateRoot -RelativePath $RelativePath -Description $Description
}

function Get-SHA256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-VerifiedArchive([string]$FileName, [string]$Url, [string]$ExpectedSHA256) {
    $downloads = New-StateDirectory "downloads" "Tool download directory"
    $archive = Get-StateChildPath (Join-Path "downloads" $FileName) "Tool archive" -AllowLeaf
    if (Test-Path -LiteralPath $archive -PathType Leaf) {
        $actual = Get-SHA256 $archive
        if (-not $actual.Equals($ExpectedSHA256, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Cached tool archive has an unexpected SHA-256: $archive. Expected $ExpectedSHA256, got $actual."
        }
        return $archive
    }

    $partial = Get-StateChildPath (Join-Path "downloads" ("{0}.{1}.partial" -f $FileName, $PID)) "Temporary tool download" -AllowLeaf
    Write-Host "Downloading $FileName from $Url"
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $partial
        $actual = Get-SHA256 $partial
        if (-not $actual.Equals($ExpectedSHA256, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Downloaded tool archive has an unexpected SHA-256. Expected $ExpectedSHA256, got $actual."
        }
        Move-Item -LiteralPath $partial -Destination $archive
    } finally {
        if (Test-Path -LiteralPath $partial -PathType Leaf) {
            Remove-Item -LiteralPath $partial -Force
        }
    }
    return $archive
}

function Invoke-Checked([string]$Executable, [string[]]$Arguments, [string]$Description) {
    & $Executable @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
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

function Set-EnvironmentValues([System.Collections.IDictionary]$Values) {
    foreach ($name in $Values.Keys) {
        [Environment]::SetEnvironmentVariable($name, $Values[$name], "Process")
    }
}

function Assert-GoEnvironment([string]$GoBinary, [System.Collections.IDictionary]$Expected, [string]$Description) {
    foreach ($name in $Expected.Keys) {
        # Go 1.26 deliberately reports an empty `go env GOENV` when GOENV=off,
        # even though the process setting is active. Check that one controller
        # directly and use `go env` for the tool-resolved values below.
        if ($name -eq "GOENV") {
            $actual = [Environment]::GetEnvironmentVariable($name, "Process")
            $actualSource = "process environment"
        } else {
            $actual = [string](& $GoBinary env $name)
            $actual = $actual.Trim()
            $actualSource = "Go reported"
        }
        if ($actual -ne [string]$Expected[$name]) {
            throw "$Description requires $name=$($Expected[$name]); $actualSource $name=$actual."
        }
    }
}

function Assert-EnvironmentUnset([string[]]$Names, [string]$Description) {
    foreach ($name in $Names) {
        $value = [Environment]::GetEnvironmentVariable($name, "Process")
        if (-not [string]::IsNullOrEmpty($value)) {
            throw "$Description requires $name to be unset."
        }
    }
}

$tempDirectory = New-StateDirectory "tmp" "Temporary directory"
$goTempDirectory = New-StateDirectory "gotmp" "Go temporary directory"
$goCacheDirectory = New-StateDirectory "gocache" "Go build cache"
$goModuleCacheDirectory = New-StateDirectory "gomodcache" "Go module cache"
$goPathDirectory = New-StateDirectory "gopath" "Go path directory"
$npmCacheDirectory = New-StateDirectory "npm-cache" "npm cache"
$toolchainDirectory = New-StateDirectory "toolchains" "Toolchain directory"
$npmUserConfig = Get-StateChildPath "npm-userconfig" "npm user configuration" -AllowLeaf
$npmGlobalConfig = Get-StateChildPath "npm-globalconfig" "npm global configuration" -AllowLeaf

$goVersion = "1.26.5"
$goArchive = Get-VerifiedArchive `
    "go1.26.5.windows-amd64.zip" `
    "https://go.dev/dl/go1.26.5.windows-amd64.zip" `
    "97e6b2a833b6d89f9ff17d25419ac0a7e3b482a044e9ab18cdef834bd834fd38"
$goDirectory = Get-StateChildPath "toolchains\go1.26.5" "Go toolchain directory"
$goBinary = Join-Path $goDirectory "go\bin\go.exe"
if (Test-Path -LiteralPath $goDirectory -PathType Container) {
    if (-not (Test-Path -LiteralPath $goBinary -PathType Leaf)) {
        throw "StateRoot contains an incomplete Go toolchain. Use a new StateRoot or -FreshState: $goDirectory"
    }
} else {
    New-Item -ItemType Directory -Force -Path $goDirectory | Out-Null
    Expand-Archive -LiteralPath $goArchive -DestinationPath $goDirectory -Force
}
if (-not (Test-Path -LiteralPath $goBinary -PathType Leaf)) {
    throw "The verified Go archive did not provide $goBinary."
}

# A portable tool still reads a few inherited process variables before the
# main build environment is installed below. Isolate its version probe as
# well, so a broken local GOROOT or NODE_OPTIONS cannot make a new clone fail
# before the verified toolchain has even been identified.
$toolProbeEnvironment = [ordered]@{
    GOENV                       = "off"
    GOWORK                      = "off"
    GO111MODULE                 = "on"
    GOROOT                      = $null
    GOTOOLCHAIN                 = "local"
    GOFLAGS                     = $null
    GOPROXY                     = "off"
    GOSUMDB                     = "off"
    GOPRIVATE                   = $null
    GONOPROXY                   = $null
    GONOSUMDB                   = $null
    GOINSECURE                  = $null
    GOVCS                       = "public:git|hg,private:off"
    GOOS                        = $null
    GOARCH                      = $null
    GOARM64                     = "v8.0"
    GOEXPERIMENT                = $null
    CGO_ENABLED                 = $null
    NODE_OPTIONS                = $null
    NODE_PATH                   = $null
    NODE_TLS_REJECT_UNAUTHORIZED = $null
    NODE_EXTRA_CA_CERTS         = $null
    NODE_USE_SYSTEM_CA          = $null
}
$toolProbeSnapshot = Get-EnvironmentSnapshot @($toolProbeEnvironment.Keys)
try {
    Set-EnvironmentValues $toolProbeEnvironment
    $actualGoVersion = (& $goBinary version).Trim()
    if ($actualGoVersion -ne "go version go1.26.5 windows/amd64") {
        throw "Unexpected Go version from ${goBinary}: $actualGoVersion"
    }

    $nodeVersion = "24.14.0"
    $nodeArchive = Get-VerifiedArchive `
        "node-v24.14.0-win-x64.zip" `
        "https://nodejs.org/dist/v24.14.0/node-v24.14.0-win-x64.zip" `
        "313fa40c0d7b18575821de8cb17483031fe07d95de5994f6f435f3b345f85c66"
    $nodeDirectory = Get-StateChildPath "toolchains\node-v24.14.0-win-x64" "Node.js toolchain directory"
    $nodeBinary = Join-Path $nodeDirectory "node.exe"
    $npmBinary = Join-Path $nodeDirectory "npm.cmd"
    if (Test-Path -LiteralPath $nodeDirectory -PathType Container) {
        if (-not (Test-Path -LiteralPath $nodeBinary -PathType Leaf) -or -not (Test-Path -LiteralPath $npmBinary -PathType Leaf)) {
            throw "StateRoot contains an incomplete Node.js toolchain. Use a new StateRoot or -FreshState: $nodeDirectory"
        }
    } else {
        Expand-Archive -LiteralPath $nodeArchive -DestinationPath $toolchainDirectory -Force
    }
    if (-not (Test-Path -LiteralPath $nodeBinary -PathType Leaf) -or -not (Test-Path -LiteralPath $npmBinary -PathType Leaf)) {
        throw "The verified Node.js archive did not provide node.exe and npm.cmd under $nodeDirectory."
    }
    $actualNodeVersion = (& $nodeBinary --version).Trim()
    if ($actualNodeVersion -ne "v24.14.0") {
        throw "Unexpected Node.js version from ${nodeBinary}: $actualNodeVersion"
    }
} finally {
    Restore-Environment $toolProbeSnapshot
}

$inheritedNpmConfigNames = @(
    Get-ChildItem Env: |
        Where-Object { $_.Name -match '^NPM_CONFIG_' } |
        ForEach-Object { $_.Name }
)
$gitConfigIsolationNames = Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_'
$clearEnvironmentNames = @($inheritedNpmConfigNames + $gitConfigIsolationNames | Select-Object -Unique)
$clearInheritedEnvironment = [ordered]@{}
foreach ($name in $clearEnvironmentNames) {
    $clearInheritedEnvironment[$name] = $null
}

$offlineEnvironment = [ordered]@{
    TEMP                    = $tempDirectory
    TMP                     = $tempDirectory
    TMPDIR                  = $tempDirectory
    GOTMPDIR                = $goTempDirectory
    GOCACHE                 = $goCacheDirectory
    GOMODCACHE              = $goModuleCacheDirectory
    GOPATH                  = $goPathDirectory
    GOENV                   = "off"
    GOWORK                  = "off"
    GO111MODULE             = "on"
    GOROOT                  = $null
    GOTOOLCHAIN             = "local"
    GOFLAGS                 = "-mod=readonly"
    GOPROXY                 = "off"
    GOSUMDB                 = "off"
    GOPRIVATE               = $null
    GONOPROXY               = $null
    GONOSUMDB               = $null
    GOINSECURE              = $null
    GOVCS                   = "public:git|hg,private:off"
    GOOS                    = $null
    GOARCH                  = $null
    GOARM64                 = "v8.0"
    GOEXPERIMENT            = $null
    NODE_OPTIONS            = $null
    NODE_PATH               = $null
    NODE_TLS_REJECT_UNAUTHORIZED = $null
    NODE_EXTRA_CA_CERTS     = $null
    NODE_USE_SYSTEM_CA      = $null
    NPM_CONFIG_CACHE        = $npmCacheDirectory
    NPM_CONFIG_USERCONFIG   = $npmUserConfig
    NPM_CONFIG_GLOBALCONFIG = $npmGlobalConfig
    NPM_CONFIG_REGISTRY     = "https://registry.npmjs.org/"
    NPM_CONFIG_IGNORE_SCRIPTS = "true"
    NPM_CONFIG_AUDIT        = "false"
    NPM_CONFIG_FUND         = "false"
    NPM_CONFIG_UPDATE_NOTIFIER = "false"
}

$environmentNames = @($offlineEnvironment.Keys) + $clearEnvironmentNames
$environmentSnapshot = Get-EnvironmentSnapshot $environmentNames
$typeScriptBinary = $null
try {
    Set-EnvironmentValues $clearInheritedEnvironment
    Set-EnvironmentValues $offlineEnvironment
    Assert-EnvironmentUnset @(
        "GOROOT",
        "NODE_OPTIONS",
        "NODE_PATH",
        "NODE_TLS_REJECT_UNAUTHORIZED",
        "NODE_EXTRA_CA_CERTS",
        "NODE_USE_SYSTEM_CA"
    ) "Build tool bootstrap"
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "Build tool bootstrap"

    if ($PrepareGoModules) {
        $downloadEnvironment = [ordered]@{
            GOPROXY = $GoProxy
            GOSUMDB = "sum.golang.org"
        }
        Set-EnvironmentValues $downloadEnvironment
        $expectedDownloadEnvironment = [ordered]@{
            GOENV = "off"
            GOWORK = "off"
            GO111MODULE = "on"
            GOTOOLCHAIN = "local"
            GOFLAGS = "-mod=readonly"
            GOPATH = $goPathDirectory
            GOCACHE = $goCacheDirectory
            GOMODCACHE = $goModuleCacheDirectory
            GOTMPDIR = $goTempDirectory
            GOPROXY = $GoProxy
            GOSUMDB = "sum.golang.org"
            GOARM64 = "v8.0"
            GOVCS = "public:git|hg,private:off"
        }
        Assert-GoEnvironment $goBinary $expectedDownloadEnvironment "Go module preparation"

        foreach ($moduleRelativePath in @("apps\block-hmi", "services\block-agent", "tools\plc-simulator")) {
            $modulePath = Join-Path $repoRoot $moduleRelativePath
            $goModPath = Join-Path $modulePath "go.mod"
            if (-not (Test-Path -LiteralPath $goModPath -PathType Leaf)) {
                throw "Expected Go module is missing go.mod: $modulePath"
            }
            $before = @{}
            foreach ($fileName in @("go.mod", "go.sum")) {
                $path = Join-Path $modulePath $fileName
                $before[$fileName] = if (Test-Path -LiteralPath $path -PathType Leaf) { Get-SHA256 $path } else { $null }
            }
            Invoke-Checked $goBinary @("-C", $modulePath, "mod", "download") "go mod download in $moduleRelativePath"
            Invoke-Checked $goBinary @("-C", $modulePath, "mod", "verify") "go mod verify in $moduleRelativePath"
            foreach ($fileName in $before.Keys) {
                $path = Join-Path $modulePath $fileName
                if ($null -eq $before[$fileName]) {
                    if (Test-Path -LiteralPath $path -PathType Leaf) {
                        throw "Go dependency preparation created untracked $moduleRelativePath/$fileName."
                    }
                    continue
                }
                if ($before[$fileName] -ne (Get-SHA256 $path)) {
                    throw "Go dependency preparation changed tracked $moduleRelativePath/$fileName."
                }
            }
        }
        Set-EnvironmentValues $offlineEnvironment
    }

    if ($PrepareTypeScript) {
        $nodeDependencies = New-StateDirectory "node-deps" "TypeScript dependency directory"
        Copy-Item -LiteralPath (Join-Path $repoRoot "package.json") -Destination (Join-Path $nodeDependencies "package.json") -Force
        Copy-Item -LiteralPath (Join-Path $repoRoot "package-lock.json") -Destination (Join-Path $nodeDependencies "package-lock.json") -Force
        Invoke-Checked $npmBinary @(
            "--prefix", $nodeDependencies,
            "--userconfig", $npmUserConfig,
            "--globalconfig", $npmGlobalConfig,
            "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--registry", "https://registry.npmjs.org/"
        ) "npm ci for locked TypeScript"
        $typeScriptBinary = Join-Path $nodeDependencies "node_modules\.bin\tsc.cmd"
        if (-not (Test-Path -LiteralPath $typeScriptBinary -PathType Leaf)) {
            throw "npm ci did not provide the locked TypeScript compiler."
        }
        $actualTypeScriptVersion = (& $typeScriptBinary --version).Trim()
        if ($actualTypeScriptVersion -ne "Version 5.6.3") {
            throw "Unexpected TypeScript version from ${typeScriptBinary}: $actualTypeScriptVersion"
        }
    }
} finally {
    Restore-Environment $environmentSnapshot
}

[pscustomobject]@{
    RepoRoot                 = $repoRoot
    StateRoot                = $StateRoot
    StateOwnerMarker         = (Get-BlockBuildStateMarkerPath $StateRoot)
    GoBinary                 = $goBinary
    GoBinDirectory           = (Split-Path -Parent $goBinary)
    GoVersion                = $actualGoVersion
    NodeBinary               = $nodeBinary
    NodeVersion              = $actualNodeVersion
    NpmBinary                = $npmBinary
    TypeScriptBinary         = $typeScriptBinary
    TypeScriptVersion        = if ($null -eq $typeScriptBinary) { $null } else { "5.6.3" }
    Environment              = $offlineEnvironment
    ClearEnvironmentNames    = $clearEnvironmentNames
    GoModuleChecksumDatabase = "sum.golang.org"
}
