[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$Version,

    [Parameter(Mandatory)]
    [ValidatePattern('^[A-Za-z0-9._:-]+$')]
    [string]$DeviceAddress,

    [Parameter(Mandatory)]
    [ValidatePattern('^[a-z0-9](?:[a-z0-9_-]{0,62})$')]
    [string]$SiteId,

    [Parameter(Mandatory)]
    [ValidatePattern('^[a-z0-9](?:[a-z0-9_-]{0,62})$')]
    [string]$BlockId,

    [Parameter(Mandatory)]
    [ValidatePattern('^[a-z0-9](?:[a-z0-9_-]{0,62})$')]
    [string]$DeviceId,

    [switch]$Build,
    [string]$ArtifactDirectory = "",
    [string]$ArtifactArchive = "",
    [string]$BuildStateRoot = "",
    [switch]$BuildFreshState,
    [string]$WorkspaceRoot = "",
    [switch]$FreshWorkspace,
    [string]$BootstrapStateRoot = "",
    [string]$WslDistribution = "",

    [string]$BootstrapCtl = "",
    [string]$CommonRoot = "",
    [string]$BootstrapEndpoint = "",
    [string]$BootstrapServerName = "",
    [string]$AdminKid = "",
    [string]$AdminKey = "",
    [string]$ManagementCA = "",
    [string]$SessionDirectory = "",
    [switch]$KeepSession,
    [switch]$DryRun
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
. (Join-Path $PSScriptRoot "build-state.ps1")
$script:DiagnosticPath = ""

function Invoke-Checked {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$Description
    )

    & $Executable @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
    }
}

function Get-EnvironmentSnapshot {
    param([string[]]$Names)

    $result = @{}
    foreach ($name in ($Names | Select-Object -Unique)) {
        $result[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    }
    return $result
}

function Restore-Environment {
    param([hashtable]$Snapshot)

    foreach ($name in $Snapshot.Keys) {
        [Environment]::SetEnvironmentVariable($name, $Snapshot[$name], "Process")
    }
}

function Set-EnvironmentValues {
    param([System.Collections.IDictionary]$Values)

    foreach ($name in $Values.Keys) {
        [Environment]::SetEnvironmentVariable($name, $Values[$name], "Process")
    }
}

function Redact-Text {
    param([AllowEmptyString()][string]$Text, [string[]]$Secrets = @())

    $result = $Text
    foreach ($secret in ($Secrets | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Sort-Object Length -Descending -Unique)) {
        $result = $result.Replace($secret, "<protected>")
    }
    return $result
}

function Write-Diagnostic {
    param([Parameter(Mandatory)][string]$Message)

    if ([string]::IsNullOrWhiteSpace($script:DiagnosticPath)) {
        return
    }
    [System.IO.File]::AppendAllText(
        $script:DiagnosticPath,
        "$(Get-Date -Format o) $Message$([Environment]::NewLine)",
        (New-Object System.Text.UTF8Encoding($false))
    )
}

function Invoke-CheckedRedacted {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$Description,
        [string[]]$Redactions = @()
    )

    $output = @(& $Executable @Arguments 2>&1)
    $exitCode = $LASTEXITCODE
    foreach ($line in $output) {
        $safeLine = Redact-Text ([string]$line) $Redactions
        Write-Host $safeLine
        Write-Diagnostic "${Description}: $safeLine"
    }
    if ($exitCode -ne 0) {
        throw "$Description failed with exit code $exitCode."
    }
}

function ConvertTo-BashLiteral {
    param([Parameter(Mandatory)][string]$Value)

    $replacement = "'" + [string][char]34 + "'" + [string][char]34 + "'"
    return "'" + $Value.Replace("'", $replacement) + "'"
}

function Get-WslExecutable {
    $command = Get-Command wsl.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $command) {
        throw "WSL is required to package and validate the HMI release archive."
    }
    return $command.Source
}

function Get-WslArguments {
    param([Parameter(Mandatory)][string[]]$Command)

    $arguments = @()
    if (-not [string]::IsNullOrWhiteSpace($WslDistribution)) {
        $arguments += @("--distribution", $WslDistribution)
    }
    $arguments += "--"
    $arguments += $Command
    return $arguments
}

function ConvertTo-WslPath {
    param([Parameter(Mandatory)][string]$Wsl, [Parameter(Mandatory)][string]$Path)

    $arguments = Get-WslArguments @("wslpath", "-a", $Path)
    $output = @(& $Wsl @arguments)
    if ($LASTEXITCODE -ne 0 -or $output.Count -ne 1 -or [string]::IsNullOrWhiteSpace($output[0])) {
        throw "WSL could not convert a local release path."
    }
    return ([string]$output[0]).Trim()
}

function Invoke-WslBash {
    param(
        [Parameter(Mandatory)][string]$Wsl,
        [Parameter(Mandatory)][string]$Script,
        [Parameter(Mandatory)][string]$Description
    )

    Invoke-Checked $Wsl (Get-WslArguments @("bash", "-lc", $Script)) $Description
}

function Assert-ArtifactDirectory {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "ArtifactDirectory does not exist: $Path"
    }
    $artifact = (Resolve-Path -LiteralPath $Path).Path
    foreach ($relative in @(
        "bin\block-agent",
        "web\index.html",
        "web\assets\points.json",
        "deploy\install.sh",
        "deploy\verify-install.sh",
        "VERSION"
    )) {
        if (-not (Test-Path -LiteralPath (Join-Path $artifact $relative) -PathType Leaf)) {
            throw "ArtifactDirectory is missing $relative."
        }
    }
    $actualVersion = (Get-Content -LiteralPath (Join-Path $artifact "VERSION") -Raw -Encoding utf8).Trim()
    if ($actualVersion -ne $Version) {
        throw "Artifact VERSION $actualVersion does not match -Version $Version."
    }
    return $artifact
}

function New-ReleaseArchive {
    param(
        [Parameter(Mandatory)][string]$Artifact,
        [Parameter(Mandatory)][string]$StateRoot
    )

    $payloadName = "payload-" + [guid]::NewGuid().ToString("N")
    $payload = New-BlockBuildStateDirectory $StateRoot $payloadName "HMI deployment payload"
    $archive = Get-BlockBuildStateChildPath $StateRoot "$payloadName\artifact.tar.gz" "HMI deployment archive" -AllowLeaf
    $wsl = Get-WslExecutable
    $sourceWsl = ConvertTo-WslPath $wsl $Artifact
    $payloadWsl = ConvertTo-WslPath $wsl $payload
    $archiveWsl = ConvertTo-WslPath $wsl $archive
    $script = @(
        "set -euo pipefail",
        "SOURCE=$(ConvertTo-BashLiteral $sourceWsl)",
        "PAYLOAD=$(ConvertTo-BashLiteral $payloadWsl)",
        "ARCHIVE=$(ConvertTo-BashLiteral $archiveWsl)",
        'mkdir -p "$PAYLOAD/artifact"',
        'cp -a "$SOURCE"/. "$PAYLOAD/artifact"/',
        'test -x "$PAYLOAD/artifact/deploy/install.sh"',
        'test -x "$PAYLOAD/artifact/deploy/verify-install.sh"',
        '(cd "$PAYLOAD/artifact"; find . -type f -print0 | sort -z | xargs -0 sha256sum > "$PAYLOAD/artifact.sha256")',
        'tar --format=posix -czf "$ARCHIVE" -C "$PAYLOAD" artifact artifact.sha256',
        'tar -tzf "$ARCHIVE" >/dev/null'
    ) -join [Environment]::NewLine
    Invoke-WslBash $wsl $script "HMI artifact package"
    return $archive
}

function Assert-ReleaseArchive {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "ArtifactArchive does not exist: $Path"
    }
    $archive = (Resolve-Path -LiteralPath $Path).Path
    $wsl = Get-WslExecutable
    $archiveWsl = ConvertTo-WslPath $wsl $archive
    $script = @(
        "set -euo pipefail",
        "ARCHIVE=$(ConvertTo-BashLiteral $archiveWsl)",
        "EXPECTED=$(ConvertTo-BashLiteral $Version)",
        'tar -tzf "$ARCHIVE" >/dev/null',
        'for ENTRY in artifact/VERSION artifact/bin/block-agent artifact/web/index.html artifact/web/assets/points.json artifact/deploy/install.sh artifact/deploy/verify-install.sh artifact.sha256; do',
        '  tar -tzf "$ARCHIVE" | grep -Fx "$ENTRY" >/dev/null',
        'done',
        'test "$(tar -xOzf "$ARCHIVE" artifact/VERSION | tr -d "\r\n")" = "$EXPECTED"'
    ) -join [Environment]::NewLine
    Invoke-WslBash $wsl $script "HMI release archive validation"
    return $archive
}

function Get-CommonBaselineCommit {
    $baseline = Get-Content -LiteralPath (Join-Path $repoRoot "COMMON_BASELINE") -Raw -Encoding utf8
    $match = [regex]::Match($baseline, '(?m)^commit:\s*([0-9a-f]{40})\s*$')
    if (-not $match.Success) {
        throw "COMMON_BASELINE does not declare a valid Common commit."
    }
    return $match.Groups[1].Value
}

function Build-PinnedBootstrapCtl {
    param(
        [Parameter(Mandatory)][string]$Common,
        [Parameter(Mandatory)][string]$Commit,
        [Parameter(Mandatory)][string]$ToolState,
        [Parameter(Mandatory)][string]$DeploymentState
    )

    if (-not (Test-Path -LiteralPath $Common -PathType Container)) {
        throw "-CommonRoot must be a Common checkout containing the pinned commit."
    }
    $common = (Resolve-Path -LiteralPath $Common).Path
    $isRepository = (& git -C $common rev-parse --is-inside-work-tree 2>$null).Trim()
    if ($LASTEXITCODE -ne 0 -or $isRepository -ne "true") {
        throw "-CommonRoot is not a Git checkout."
    }
    & git -C $common cat-file -e ("{0}:tools/ssh-bootstrapctl/go.mod" -f $Commit) 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw "The pinned Common commit does not contain ssh-bootstrapctl."
    }

    $sourceName = "ssh-bootstrapctl-" + [guid]::NewGuid().ToString("N")
    $source = New-BlockBuildStateDirectory $DeploymentState $sourceName "Pinned ssh-bootstrapctl source"
    $sourceZip = Get-BlockBuildStateChildPath $DeploymentState "$sourceName.zip" "Pinned ssh-bootstrapctl source archive" -AllowLeaf
    Invoke-Checked "git" @(
        "-C", $common, "archive", "--format=zip", "--output=$sourceZip",
        ("{0}:tools/ssh-bootstrapctl" -f $Commit)
    ) "Pinned ssh-bootstrapctl source export"
    Expand-Archive -LiteralPath $sourceZip -DestinationPath $source -Force

    $tools = & (Join-Path $PSScriptRoot "bootstrap-build-tools.ps1") -StateRoot $ToolState
    if ($null -eq $tools) {
        throw "Build tool bootstrap did not return a toolchain."
    }
    $names = @($tools.Environment.Keys) + @($tools.ClearEnvironmentNames) + @("GOOS", "GOARCH", "CGO_ENABLED")
    $snapshot = Get-EnvironmentSnapshot $names
    try {
        $clear = [ordered]@{}
        foreach ($name in $tools.ClearEnvironmentNames) {
            $clear[$name] = $null
        }
        Set-EnvironmentValues $clear
        Set-EnvironmentValues $tools.Environment
        Set-EnvironmentValues ([ordered]@{ GOOS = $null; GOARCH = $null; CGO_ENABLED = $null })
        $bin = New-BlockBuildStateDirectory $DeploymentState "bin" "HMI deployment tool directory"
        $output = Join-Path $bin "ssh-bootstrapctl.exe"
        Invoke-Checked $tools.GoBinary @("-C", $source, "build", "-buildvcs=false", "-trimpath", "-o", $output, ".") "Pinned ssh-bootstrapctl build"
        if (-not (Test-Path -LiteralPath $output -PathType Leaf)) {
            throw "Pinned ssh-bootstrapctl build did not produce an executable."
        }
        return $output
    } finally {
        Restore-Environment $snapshot
    }
}

function Resolve-BootstrapCtl {
    param(
        [Parameter(Mandatory)][string]$DeploymentState,
        [string]$ReleaseState = ""
    )

    if (-not [string]::IsNullOrWhiteSpace($BootstrapCtl)) {
        if (-not (Test-Path -LiteralPath $BootstrapCtl -PathType Leaf)) {
            throw "-BootstrapCtl does not exist."
        }
        return (Resolve-Path -LiteralPath $BootstrapCtl).Path
    }
    if ([string]::IsNullOrWhiteSpace($CommonRoot)) {
        throw "Provide -BootstrapCtl or -CommonRoot for the fixed SSH Bootstrap client."
    }
    $toolState = $BootstrapStateRoot
    if ([string]::IsNullOrWhiteSpace($toolState)) {
        $toolState = if ([string]::IsNullOrWhiteSpace($ReleaseState)) {
            ".cache\block-bootstrapctl-$Version"
        } else {
            $ReleaseState
        }
    }
    return Build-PinnedBootstrapCtl $CommonRoot (Get-CommonBaselineCommit) $toolState $DeploymentState
}

function Get-Session {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "SSH session directory does not exist."
    }
    $session = (Resolve-Path -LiteralPath $Path).Path
    foreach ($name in @("id_ed25519", "id_ed25519-cert.pub", "known_hosts", "connection.json")) {
        if (-not (Test-Path -LiteralPath (Join-Path $session $name) -PathType Leaf)) {
            throw "SSH session directory is incomplete."
        }
    }
    try {
        $metadata = Get-Content -LiteralPath (Join-Path $session "connection.json") -Raw -Encoding utf8 | ConvertFrom-Json -ErrorAction Stop
        $validBefore = [DateTimeOffset]::Parse([string]$metadata.validBefore, [System.Globalization.CultureInfo]::InvariantCulture)
    } catch {
        throw "SSH session metadata is invalid."
    }
    if ($metadata.username -ne "release" -or $metadata.host -notmatch '^[A-Za-z0-9._:-]+$' -or
        [int]$metadata.port -lt 1 -or [int]$metadata.port -gt 65535 -or
        $validBefore.UtcDateTime -le [DateTime]::UtcNow.AddSeconds(30)) {
        throw "SSH session is not a live release session."
    }
    return [pscustomobject]@{
        Directory = $session
        Host = [string]$metadata.host
        Port = [int]$metadata.port
        Username = [string]$metadata.username
    }
}

function Invoke-Remote {
    param(
        [Parameter(Mandatory)][string]$Ctl,
        [Parameter(Mandatory)][string]$Session,
        [Parameter(Mandatory)][string]$Command,
        [Parameter(Mandatory)][string]$Description
    )

    $remote = "bash -lc " + (ConvertTo-BashLiteral $Command)
    Invoke-CheckedRedacted $Ctl @("connect", "--output-dir", $Session, "--", $remote) $Description @($Session)
}

function Copy-ReleaseArchive {
    param(
        [Parameter(Mandatory)][string]$Archive,
        [Parameter(Mandatory)][object]$Session,
        [Parameter(Mandatory)][string]$RemotePath
    )

    $scp = Get-Command scp.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $scp) {
        $scp = Get-Command scp -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    }
    if ($null -eq $scp) {
        throw "OpenSSH scp is required."
    }
    $host = if ($Session.Host.Contains(":")) { "[$($Session.Host)]" } else { $Session.Host }
    $destination = "{0}@{1}:{2}" -f $Session.Username, $host, $RemotePath
    $directory = $Session.Directory
    $arguments = @(
        "-F", "NUL",
        "-i", (Join-Path $directory "id_ed25519"),
        "-o", ("CertificateFile=" + (Join-Path $directory "id_ed25519-cert.pub")),
        "-o", ("UserKnownHostsFile=" + (Join-Path $directory "known_hosts")),
        "-o", "StrictHostKeyChecking=yes",
        "-o", "BatchMode=yes",
        "-o", "PasswordAuthentication=no",
        "-o", "KbdInteractiveAuthentication=no",
        "-o", "PreferredAuthentications=publickey",
        "-o", "IdentitiesOnly=yes",
        "-o", "IdentityAgent=none",
        "-P", [string]$Session.Port,
        $Archive,
        $destination
    )
    Invoke-CheckedRedacted $scp.Source $arguments "Verified release copy" @($directory)
}

function Remove-GeneratedSession {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$StateRoot
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        return
    }
    $fullPath = (Resolve-Path -LiteralPath $Path).Path
    if (-not (Test-BlockBuildChildPath $fullPath $StateRoot)) {
        throw "Refusing to delete a session outside the deployment workspace."
    }
    Assert-BlockBuildNoReparseDescendants $fullPath "Generated SSH session"
    Remove-Item -LiteralPath $fullPath -Recurse -Force
}

$sourceCount = 0
if ($Build) { $sourceCount++ }
if (-not [string]::IsNullOrWhiteSpace($ArtifactDirectory)) { $sourceCount++ }
if (-not [string]::IsNullOrWhiteSpace($ArtifactArchive)) { $sourceCount++ }
if ($sourceCount -ne 1) {
    throw "Choose exactly one artifact source: -Build, -ArtifactDirectory, or -ArtifactArchive."
}

if ([string]::IsNullOrWhiteSpace($BootstrapEndpoint)) {
    $BootstrapEndpoint = if ($DeviceAddress.Contains(":")) {
        "https://[$DeviceAddress]:9443"
    } else {
        "https://{0}:9443" -f $DeviceAddress
    }
}
try {
    $endpointUri = [System.Uri]$BootstrapEndpoint
} catch {
    throw "-BootstrapEndpoint must be an absolute HTTPS URL."
}
if (-not $endpointUri.IsAbsoluteUri -or $endpointUri.Scheme -ne "https" -or
    -not [string]::IsNullOrWhiteSpace($endpointUri.Query) -or
    -not [string]::IsNullOrWhiteSpace($endpointUri.Fragment) -or
    ($endpointUri.AbsolutePath -ne "/" -and $endpointUri.AbsolutePath -ne "")) {
    throw "-BootstrapEndpoint must be an HTTPS origin only."
}
if ([string]::IsNullOrWhiteSpace($BootstrapServerName)) {
    $BootstrapServerName = $DeviceAddress
}

$defaultArtifact = Join-Path (Get-BlockBuildReleaseStateRoot $repoRoot $Version) "artifact"
$stateInput = if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { ".cache\block-hmi-deploy-$Version" } else { $WorkspaceRoot }

if ($DryRun) {
    $artifactPlan = if ($Build) {
        $defaultArtifact
    } elseif (-not [string]::IsNullOrWhiteSpace($ArtifactDirectory)) {
        [System.IO.Path]::GetFullPath($ArtifactDirectory)
    } else {
        "<archive supplied by -ArtifactArchive>"
    }
    Write-Host "Dry run only. No build, HTTPS request, SSH connection, copy, or device write was performed."
    [pscustomobject]@{
        Mode = "dry-run"
        Version = $Version
        Artifact = $artifactPlan
        Workspace = [System.IO.Path]::GetFullPath($stateInput)
        BootstrapEndpoint = $BootstrapEndpoint
        Plan = @(
            "Build uses tools/build-block.ps1",
            "Package uses WSL tar and writes artifact.sha256",
            "HTTPS issues a five-minute release SSH certificate for BLOCK/$SiteId/$BlockId/$DeviceId",
            "SSH preflight requires sudo -n true; no PLC command is sent",
            "scp reuses the HTTPS-verified key, certificate, and known_hosts",
            "The candidate install.sh runs, followed by verify-install.sh"
        )
    }
    return
}

$deploymentState = Resolve-BlockBuildStateRoot $repoRoot $stateInput "block-hmi-deploy-v1" -FreshState:$FreshWorkspace
$script:DiagnosticPath = Get-BlockBuildStateChildPath $deploymentState "deploy-diagnostics.log" "HMI deployment diagnostics" -AllowLeaf
Write-Diagnostic "HMI deployment started for $Version."

$generatedSession = $false
$generatedSessionPath = ""
try {
    $releaseState = ""
    if ($Build) {
        $buildArguments = @("-Version", $Version)
        if (-not [string]::IsNullOrWhiteSpace($BuildStateRoot)) {
            $buildArguments += @("-StateRoot", $BuildStateRoot)
        }
        if ($BuildFreshState) {
            $buildArguments += "-FreshState"
        }
        $buildResult = & (Join-Path $PSScriptRoot "build-block.ps1") @buildArguments
        if ($null -eq $buildResult -or $buildResult -is [System.Array]) {
            throw "The fixed build entry did not return release metadata."
        }
        $ArtifactDirectory = [string]$buildResult.ArtifactDirectory
        $releaseState = [string]$buildResult.StateRoot
    }

    if (-not [string]::IsNullOrWhiteSpace($ArtifactDirectory)) {
        $archive = New-ReleaseArchive (Assert-ArtifactDirectory $ArtifactDirectory) $deploymentState
    } else {
        $archive = $ArtifactArchive
    }
    $archive = Assert-ReleaseArchive $archive
    Write-Diagnostic "Local release archive passed preflight."

    $ctl = Resolve-BootstrapCtl $deploymentState $releaseState
    if (-not [string]::IsNullOrWhiteSpace($SessionDirectory)) {
        $session = Get-Session $SessionDirectory
    } else {
        if ([string]::IsNullOrWhiteSpace($AdminKid) -or [string]::IsNullOrWhiteSpace($AdminKey) -or [string]::IsNullOrWhiteSpace($ManagementCA)) {
            throw "-AdminKid, -AdminKey, and -ManagementCA are required unless -SessionDirectory is supplied."
        }
        if ($AdminKid -notmatch '^[A-Za-z0-9._-]{1,64}$' -or
            -not (Test-Path -LiteralPath $AdminKey -PathType Leaf) -or
            -not (Test-Path -LiteralPath $ManagementCA -PathType Leaf)) {
            throw "SSH Bootstrap credentials are invalid or unavailable."
        }
        $SessionDirectory = New-BlockBuildStateDirectory $deploymentState ("ssh-session-" + [guid]::NewGuid().ToString("N")) "Generated SSH session"
        $generatedSession = $true
        $generatedSessionPath = $SessionDirectory
        Invoke-CheckedRedacted $ctl @(
            "request",
            "--endpoint", $BootstrapEndpoint,
            "--target", "BLOCK",
            "--site-id", $SiteId,
            "--block-id", $BlockId,
            "--device-id", $DeviceId,
            "--profile", "release",
            "--admin-kid", $AdminKid,
            "--admin-key", $AdminKey,
            "--ca", $ManagementCA,
            "--server-name", $BootstrapServerName,
            "--output-dir", $SessionDirectory
        ) "Verified HTTPS SSH certificate request" @($AdminKey, $ManagementCA, $SessionDirectory)
        $session = Get-Session $SessionDirectory
    }
    Write-Diagnostic "Verified HTTPS SSH session is ready."

    $stamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
    $remoteTemp = "/tmp/block-hmi-$Version-$stamp"
    $remoteArchive = "$remoteTemp/artifact.tar.gz"
    $remoteStage = "/var/backups/block/stage-$Version-$stamp"

    Invoke-Remote $ctl $session.Directory @'
set -euo pipefail
command -v bash >/dev/null
command -v sudo >/dev/null
command -v tar >/dev/null
command -v sha256sum >/dev/null
command -v file >/dev/null
sudo -n true
test -r /etc/block/block.env
'@ "Remote deployment preflight"
    Invoke-Remote $ctl $session.Directory "set -euo pipefail; umask 077; install -d -m 0700 '$remoteTemp'" "Remote staging preparation"
    Copy-ReleaseArchive $archive $session $remoteArchive

    $install = @'
set -euo pipefail
STAGE='__STAGE__'
TEMP='__TEMP__'
ARCHIVE='__ARCHIVE__'
VERSION='__VERSION__'
cleanup() {
  status=$?
  trap - EXIT
  sudo rm -rf -- "$STAGE" >/dev/null 2>&1 || true
  rm -rf -- "$TEMP" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT
sudo install -d -o root -g root -m 0700 "$STAGE"
sudo install -m 0600 -o root -g root "$ARCHIVE" "$STAGE/artifact.tar.gz"
sudo tar -xzf "$STAGE/artifact.tar.gz" -C "$STAGE" --no-same-owner
sudo install -m 0640 -o root -g block /etc/block/block.env "$STAGE/block.env"
sudo bash -lc "cd '$STAGE/artifact' && sha256sum -c ../artifact.sha256"
sudo file "$STAGE/artifact/bin/block-agent" | grep -Eq 'ELF .*ARM aarch64'
sudo test -x "$STAGE/artifact/bin/block-agent"
sudo test -f "$STAGE/artifact/web/index.html"
sudo test -f "$STAGE/artifact/web/assets/points.json"
sudo test -x "$STAGE/artifact/deploy/install.sh"
sudo test -x "$STAGE/artifact/deploy/verify-install.sh"
test "$(sudo cat "$STAGE/artifact/VERSION")" = "$VERSION"
sudo "$STAGE/artifact/deploy/install.sh" --execute --artifact-dir "$STAGE/artifact" --config "$STAGE/block.env" --version "$VERSION"
sudo /opt/block/current/deploy/verify-install.sh --expected-version "$VERSION"
sudo /opt/block/current/deploy/version.sh
'@
    $install = $install.Replace("__STAGE__", $remoteStage).Replace("__TEMP__", $remoteTemp).Replace("__ARCHIVE__", $remoteArchive).Replace("__VERSION__", $Version)
    Invoke-Remote $ctl $session.Directory $install "Candidate HMI application install and verification"
    Write-Diagnostic "HMI deployment completed; remote staging and temporary files were cleaned."

    [pscustomobject]@{
        Mode = "deployed"
        Version = $Version
        ArtifactArchive = $archive
        BootstrapEndpoint = $BootstrapEndpoint
        RemoteStage = "cleaned"
        Diagnostics = $script:DiagnosticPath
    }
} catch {
    $safeMessage = Redact-Text $_.Exception.Message @($AdminKey, $ManagementCA, $SessionDirectory, $generatedSessionPath)
    Write-Diagnostic "FAILED: $safeMessage"
    throw $safeMessage
} finally {
    if ($generatedSession -and -not $KeepSession -and -not [string]::IsNullOrWhiteSpace($generatedSessionPath)) {
        Remove-GeneratedSession $generatedSessionPath $deploymentState
    }
}
