[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$StateRoot,
    [switch]$Execute
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "build-state.ps1")

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$candidate = if ([System.IO.Path]::IsPathRooted($StateRoot)) {
    ConvertTo-BlockBuildCanonicalPath $StateRoot
} else {
    ConvertTo-BlockBuildCanonicalPath (Join-Path $repoRoot $StateRoot)
}
if (-not (Test-Path -LiteralPath $candidate -PathType Container)) {
    throw "Cleanup only accepts an existing, tool-owned StateRoot: $candidate"
}
$StateRoot = Resolve-BlockBuildStateRoot -RepoRoot $repoRoot `
    -StateRoot $candidate -Owner "block-build-tools"

$targets = @(
    [pscustomobject]@{ RelativePath = "downloads"; AllowLeaf = $false; Kind = "verified tool downloads" },
    [pscustomobject]@{ RelativePath = "toolchains"; AllowLeaf = $false; Kind = "extracted Go and Node toolchains" },
    [pscustomobject]@{ RelativePath = "gopath"; AllowLeaf = $false; Kind = "Go workspace" },
    [pscustomobject]@{ RelativePath = "gocache"; AllowLeaf = $false; Kind = "Go build cache" },
    [pscustomobject]@{ RelativePath = "gomodcache"; AllowLeaf = $false; Kind = "Go module cache" },
    [pscustomobject]@{ RelativePath = "gotmp"; AllowLeaf = $false; Kind = "Go temporary files" },
    [pscustomobject]@{ RelativePath = "tmp"; AllowLeaf = $false; Kind = "temporary files" },
    [pscustomobject]@{ RelativePath = "npm-cache"; AllowLeaf = $false; Kind = "npm cache" },
    [pscustomobject]@{ RelativePath = "npm-userconfig"; AllowLeaf = $true; Kind = "generated npm user configuration" },
    [pscustomobject]@{ RelativePath = "npm-globalconfig"; AllowLeaf = $true; Kind = "generated npm global configuration" },
    [pscustomobject]@{ RelativePath = "node-deps"; AllowLeaf = $false; Kind = "locked TypeScript dependency installation" },
    [pscustomobject]@{ RelativePath = "typescript"; AllowLeaf = $false; Kind = "TypeScript compiler outputs" },
    [pscustomobject]@{ RelativePath = "bin"; AllowLeaf = $false; Kind = "SSH verification binary directory" },
    [pscustomobject]@{ RelativePath = "artifact"; AllowLeaf = $false; Kind = "release artifact directory" },
    [pscustomobject]@{ RelativePath = "artifact.tar.gz"; AllowLeaf = $true; Kind = "release archive" },
    [pscustomobject]@{ RelativePath = "artifact.sha256"; AllowLeaf = $true; Kind = "release manifest" }
)

$existingTargets = @()
foreach ($target in $targets) {
    $path = Get-BlockBuildStateChildPath -StateRoot $StateRoot -RelativePath $target.RelativePath `
        -Description ("Cleanup target: " + $target.Kind) -AllowLeaf:$target.AllowLeaf
    if (Test-Path -LiteralPath $path) {
        $item = Get-Item -LiteralPath $path -Force
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Cleanup target may not be a reparse point or junction: $path"
        }
        if ($item.PSIsContainer) {
            Assert-BlockBuildNoReparseDescendants -Path $path -Description ("Cleanup target: " + $target.Kind)
        }
        $existingTargets += [pscustomobject]@{ Path = $path; Kind = $target.Kind; IsDirectory = $item.PSIsContainer }
    }
}

if ($existingTargets.Count -eq 0) {
    Write-Output "No audited build artifacts are present under managed StateRoot: $StateRoot"
    return
}

foreach ($target in $existingTargets) {
    $action = if ($Execute) { "DELETE" } else { "DRY-RUN" }
    Write-Output "$action $($target.Kind): $($target.Path)"
}

if (-not $Execute) {
    Write-Output "No files were deleted. Re-run in a user terminal with -Execute only after reviewing this exact list."
    return
}

foreach ($target in $existingTargets) {
    if ($target.IsDirectory) {
        Remove-Item -LiteralPath $target.Path -Recurse -Force
    } else {
        Remove-Item -LiteralPath $target.Path -Force
    }
}
Write-Output "Deleted only the audited build-artifact targets listed above. StateRoot metadata and any non-listed files were preserved."
