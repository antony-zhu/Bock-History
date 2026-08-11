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

$buildRelease = Join-Path $PSScriptRoot "build-release.ps1"
if (-not (Test-Path -LiteralPath $buildRelease -PathType Leaf)) {
    throw "The formal build entry is missing: $buildRelease"
}

$result = & $buildRelease `
    -Version $Version `
    -StateRoot $StateRoot `
    -FreshState:$FreshState `
    -AllowDirty:$AllowDirty `
    -PLCProfile $PLCProfile `
    -GoProxy $GoProxy `
    -GitBash $GitBash

if ($null -eq $result) {
    throw "The formal build did not return release metadata."
}
if ($result -is [System.Array]) {
    throw "The formal build returned more than one metadata object."
}

Write-Host "Block release build completed."
Write-Host "Artifact: $($result.ArtifactDirectory)"
Write-Host "Metadata: $($result.MetadataPath)"

$result
