Set-StrictMode -Version Latest

$script:BlockBuildStateMarkerName = ".block-build-state.json"
$script:BlockBuildStateMarkerFormat = "block-build-state-v1"

function ConvertTo-BlockBuildCanonicalPath {
    param(
        [Parameter(Mandatory)]
        [string]$Path
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $volumeRoot = [System.IO.Path]::GetPathRoot($fullPath)
    if (-not $fullPath.Equals($volumeRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        $fullPath = $fullPath.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    }
    return $fullPath
}

function Test-BlockBuildChildPath {
    param(
        [Parameter(Mandatory)]
        [string]$Path,
        [Parameter(Mandatory)]
        [string]$Parent
    )

    $fullPath = ConvertTo-BlockBuildCanonicalPath $Path
    $fullParent = ConvertTo-BlockBuildCanonicalPath $Parent
    if ($fullPath.Equals($fullParent, [System.StringComparison]::OrdinalIgnoreCase)) {
        return $false
    }
    $separator = [System.IO.Path]::DirectorySeparatorChar
    $prefix = if ($fullParent.EndsWith([string]$separator)) { $fullParent } else { $fullParent + $separator }
    return $fullPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)
}

function Get-BlockBuildEnvironmentNamesMatching {
    param(
        [Parameter(Mandatory)]
        [string]$Pattern
    )

    return @(
        Get-ChildItem Env: |
            Where-Object { $_.Name -match $Pattern } |
            ForEach-Object { $_.Name } |
            Sort-Object -Unique
    )
}

function Assert-BlockBuildEnvironmentPatternUnset {
    param(
        [Parameter(Mandatory)]
        [string]$Pattern,
        [Parameter(Mandatory)]
        [string]$Description
    )

    if (@(Get-BlockBuildEnvironmentNamesMatching -Pattern $Pattern).Count -ne 0) {
        throw "$Description requires all inherited environment variables matching $Pattern to be unset."
    }
}

function Get-BlockBuildReleaseStateRoot {
    param(
        [Parameter(Mandatory)]
        [string]$RepoRoot,
        [Parameter(Mandatory)]
        [ValidatePattern('^[A-Za-z0-9._-]+$')]
        [string]$Version
    )

    $canonicalRepoRoot = ConvertTo-BlockBuildCanonicalPath $RepoRoot
    $cacheRoot = ConvertTo-BlockBuildCanonicalPath (Join-Path $canonicalRepoRoot ".cache")
    $stateRoot = ConvertTo-BlockBuildCanonicalPath (Join-Path $cacheRoot ("block-release-" + $Version))
    if (-not (Test-BlockBuildChildPath $stateRoot $cacheRoot) -or
        -not (ConvertTo-BlockBuildCanonicalPath (Split-Path -Parent $stateRoot)).Equals($cacheRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Release StateRoot must be a dedicated direct child of $cacheRoot."
    }
    return $stateRoot
}

function Assert-BlockBuildNoReparseComponents {
    param(
        [Parameter(Mandatory)]
        [string]$Path,
        [Parameter(Mandatory)]
        [string]$StartPath,
        [Parameter(Mandatory)]
        [string]$Description,
        [switch]$AllowLeaf
    )

    $fullPath = ConvertTo-BlockBuildCanonicalPath $Path
    $fullStart = ConvertTo-BlockBuildCanonicalPath $StartPath
    if (-not $fullPath.Equals($fullStart, [System.StringComparison]::OrdinalIgnoreCase) -and
        -not (Test-BlockBuildChildPath $fullPath $fullStart)) {
        throw "$Description must remain at or below $fullStart."
    }

    $current = $fullStart
    if (Test-Path -LiteralPath $current) {
        $item = Get-Item -LiteralPath $current -Force
        if (-not $item.PSIsContainer) {
            throw "$Description parent is not a directory: $current"
        }
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "$Description may not use a reparse point or junction: $current"
        }
    }

    $relative = $fullPath.Substring($fullStart.Length).TrimStart([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if ([string]::IsNullOrWhiteSpace($relative)) {
        return
    }
    $segments = @($relative -split '[\\/]+' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    for ($index = 0; $index -lt $segments.Count; $index++) {
        $segment = $segments[$index]
        if ([string]::IsNullOrWhiteSpace($segment)) {
            continue
        }
        $current = Join-Path $current $segment
        if (-not (Test-Path -LiteralPath $current)) {
            continue
        }
        $item = Get-Item -LiteralPath $current -Force
        if (-not $item.PSIsContainer -and -not ($AllowLeaf -and $index -eq ($segments.Count - 1))) {
            throw "$Description path component is not a directory: $current"
        }
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "$Description may not use a reparse point or junction: $current"
        }
    }
}

function Assert-BlockBuildNoReparseDescendants {
    param(
        [Parameter(Mandatory)]
        [string]$Path,
        [Parameter(Mandatory)]
        [string]$Description
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        return
    }
    $reparsePoint = Get-ChildItem -LiteralPath $Path -Force -Recurse -Attributes ReparsePoint -ErrorAction Stop |
        Select-Object -First 1
    if ($null -ne $reparsePoint) {
        throw "$Description contains a reparse point or junction and cannot be removed: $($reparsePoint.FullName)"
    }
}

function Get-BlockBuildStateMarkerPath {
    param(
        [Parameter(Mandatory)]
        [string]$StateRoot
    )

    return Join-Path (ConvertTo-BlockBuildCanonicalPath $StateRoot) $script:BlockBuildStateMarkerName
}

function Test-BlockBuildStateOwner {
    param(
        [Parameter(Mandatory)]
        [string]$StateRoot,
        [Parameter(Mandatory)]
        [string]$Owner,
        [Parameter(Mandatory)]
        [string]$RepoRoot
    )

    $markerPath = Get-BlockBuildStateMarkerPath $StateRoot
    if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) {
        return $false
    }
    $markerItem = Get-Item -LiteralPath $markerPath -Force
    if (($markerItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "StateRoot owner marker may not be a reparse point: $markerPath"
    }
    try {
        $marker = Get-Content -LiteralPath $markerPath -Raw -Encoding utf8 | ConvertFrom-Json -ErrorAction Stop
    } catch {
        throw "StateRoot owner marker is invalid: $markerPath"
    }
    $canonicalRepoRoot = ConvertTo-BlockBuildCanonicalPath $RepoRoot
    if ($marker.format -ne $script:BlockBuildStateMarkerFormat -or
        $marker.owner -ne $Owner -or
        -not ([string]$marker.repoRoot).Equals($canonicalRepoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "StateRoot owner marker does not belong to $Owner for this repository: $markerPath"
    }
    return $true
}

function Write-BlockBuildStateOwner {
    param(
        [Parameter(Mandatory)]
        [string]$StateRoot,
        [Parameter(Mandatory)]
        [string]$Owner,
        [Parameter(Mandatory)]
        [string]$RepoRoot
    )

    $marker = [ordered]@{
        format = $script:BlockBuildStateMarkerFormat
        owner = $Owner
        repoRoot = ConvertTo-BlockBuildCanonicalPath $RepoRoot
    }
    $markerPath = Get-BlockBuildStateMarkerPath $StateRoot
    $json = $marker | ConvertTo-Json -Compress
    [System.IO.File]::WriteAllText($markerPath, $json + [Environment]::NewLine, (New-Object System.Text.UTF8Encoding($false)))
}

function Resolve-BlockBuildStateRoot {
    param(
        [Parameter(Mandatory)]
        [string]$RepoRoot,
        [Parameter(Mandatory)]
        [string]$StateRoot,
        [Parameter(Mandatory)]
        [string]$Owner,
        [switch]$FreshState
    )

    $canonicalRepoRoot = ConvertTo-BlockBuildCanonicalPath $RepoRoot
    $cacheRoot = ConvertTo-BlockBuildCanonicalPath (Join-Path $canonicalRepoRoot ".cache")
    $candidate = if ([System.IO.Path]::IsPathRooted($StateRoot)) {
        ConvertTo-BlockBuildCanonicalPath $StateRoot
    } else {
        ConvertTo-BlockBuildCanonicalPath (Join-Path $canonicalRepoRoot $StateRoot)
    }
    $volumeRoot = ConvertTo-BlockBuildCanonicalPath ([System.IO.Path]::GetPathRoot($candidate))

    if ($candidate.Equals($volumeRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
        $candidate.Equals($canonicalRepoRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
        $candidate.Equals($cacheRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "StateRoot must be a dedicated child of $cacheRoot, not a volume, repository root, or cache root."
    }
    if (-not (Test-BlockBuildChildPath $candidate $cacheRoot)) {
        throw "StateRoot must be a dedicated child of the repository cache root: $cacheRoot"
    }
    if (-not (ConvertTo-BlockBuildCanonicalPath (Split-Path -Parent $candidate)).Equals($cacheRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "StateRoot must be a direct child of the repository cache root: $cacheRoot"
    }

    Assert-BlockBuildNoReparseComponents -Path $canonicalRepoRoot -StartPath $canonicalRepoRoot -Description "Repository root"
    if (-not (Test-Path -LiteralPath $cacheRoot)) {
        New-Item -ItemType Directory -Path $cacheRoot -Force | Out-Null
    }
    Assert-BlockBuildNoReparseComponents -Path $cacheRoot -StartPath $canonicalRepoRoot -Description "Repository cache root"
    Assert-BlockBuildNoReparseComponents -Path $candidate -StartPath $cacheRoot -Description "StateRoot"

    if (Test-Path -LiteralPath $candidate) {
        $candidateItem = Get-Item -LiteralPath $candidate -Force
        if (-not $candidateItem.PSIsContainer) {
            throw "StateRoot must be a directory: $candidate"
        }
        if (($candidateItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "StateRoot may not be a reparse point or junction: $candidate"
        }
        $entries = @(Get-ChildItem -LiteralPath $candidate -Force)
        $hasOwnerMarker = Test-BlockBuildStateOwner -StateRoot $candidate -Owner $Owner -RepoRoot $canonicalRepoRoot
        if ($FreshState) {
            if (-not $hasOwnerMarker) {
                if ($entries.Count -ne 0) {
                    throw "-FreshState refuses to remove a non-empty StateRoot without this tool's owner marker: $candidate"
                }
                # An interrupted first run can leave an empty directory before
                # its owner marker is written. There is nothing to delete, so
                # claiming this exact empty directory is safe.
                Write-BlockBuildStateOwner -StateRoot $candidate -Owner $Owner -RepoRoot $canonicalRepoRoot
                return $candidate
            }
            Assert-BlockBuildNoReparseDescendants -Path $candidate -Description "StateRoot"
            Remove-Item -LiteralPath $candidate -Recurse -Force
            New-Item -ItemType Directory -Path $candidate -Force | Out-Null
            Write-BlockBuildStateOwner -StateRoot $candidate -Owner $Owner -RepoRoot $canonicalRepoRoot
            return $candidate
        }

        if ($entries.Count -gt 0 -and -not $hasOwnerMarker) {
            throw "StateRoot is non-empty and has no owner marker for this tool: $candidate"
        }
        if (-not $hasOwnerMarker) {
            Write-BlockBuildStateOwner -StateRoot $candidate -Owner $Owner -RepoRoot $canonicalRepoRoot
        }
        return $candidate
    }

    New-Item -ItemType Directory -Path $candidate -Force | Out-Null
    Assert-BlockBuildNoReparseComponents -Path $candidate -StartPath $cacheRoot -Description "StateRoot"
    Write-BlockBuildStateOwner -StateRoot $candidate -Owner $Owner -RepoRoot $canonicalRepoRoot
    return $candidate
}

function Get-BlockBuildStateChildPath {
    param(
        [Parameter(Mandatory)]
        [string]$StateRoot,
        [Parameter(Mandatory)]
        [string]$RelativePath,
        [Parameter(Mandatory)]
        [string]$Description,
        [switch]$AllowLeaf
    )

    $canonicalStateRoot = ConvertTo-BlockBuildCanonicalPath $StateRoot
    $candidate = ConvertTo-BlockBuildCanonicalPath (Join-Path $canonicalStateRoot $RelativePath)
    if (-not (Test-BlockBuildChildPath $candidate $canonicalStateRoot)) {
        throw "$Description must remain inside StateRoot: $canonicalStateRoot"
    }
    Assert-BlockBuildNoReparseComponents -Path $candidate -StartPath $canonicalStateRoot -Description $Description -AllowLeaf:$AllowLeaf
    return $candidate
}

function New-BlockBuildStateDirectory {
    param(
        [Parameter(Mandatory)]
        [string]$StateRoot,
        [Parameter(Mandatory)]
        [string]$RelativePath,
        [Parameter(Mandatory)]
        [string]$Description
    )

    $path = Get-BlockBuildStateChildPath -StateRoot $StateRoot -RelativePath $RelativePath -Description $Description
    New-Item -ItemType Directory -Path $path -Force | Out-Null
    Assert-BlockBuildNoReparseComponents -Path $path -StartPath $StateRoot -Description $Description
    return $path
}
