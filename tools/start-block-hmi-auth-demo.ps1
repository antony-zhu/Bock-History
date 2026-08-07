[CmdletBinding()]
param(
    [switch]$FreshAuth,
    [switch]$Stop,
    [ValidateRange(1024, 65535)]
    [int]$Port = 4173,
    [string]$DataDirectory = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$workspaceRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot "..\..\.."))
$repoCacheRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot ".cache"))

function Get-NormalizedPath([string]$Path) {
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $repoRoot $Path))
}

function Assert-ChildPath([string]$Path, [string]$Parent, [string]$Description) {
    $separator = [System.IO.Path]::DirectorySeparatorChar
    $fullPath = [System.IO.Path]::GetFullPath($Path).TrimEnd($separator)
    $fullParent = [System.IO.Path]::GetFullPath($Parent).TrimEnd($separator)
    $prefix = $fullParent + $separator
    if ($fullPath -eq $fullParent -or -not $fullPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Description must remain inside $fullParent."
    }
    return $fullPath
}

function Get-ProcessInfo([int]$ProcessId) {
    return Get-CimInstance Win32_Process -Filter ("ProcessId = {0}" -f $ProcessId) -ErrorAction SilentlyContinue
}

function Get-Listeners([int]$ListeningPort) {
    if ($null -eq (Get-Command Get-NetTCPConnection -ErrorAction SilentlyContinue)) {
        throw "Get-NetTCPConnection is required to inspect the local demo port safely."
    }
    return @(Get-NetTCPConnection -State Listen -LocalPort $ListeningPort -ErrorAction SilentlyContinue)
}

function Read-AgentRecord([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $null
    }
    try {
        return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    } catch {
        throw "The demo PID record is invalid: $Path"
    }
}

function Get-RecordedAgent([string]$PidPath, [string]$ExpectedBinary, [string]$ExpectedDatabase) {
    $record = Read-AgentRecord $PidPath
    if ($null -eq $record -or $null -eq $record.pid) {
        return $null
    }
    $processId = [int]$record.pid
    if ($processId -le 0 -or $null -eq $record.binaryPath -or $null -eq $record.databasePath) {
        return $null
    }
    if (-not ([string]$record.binaryPath).Equals($ExpectedBinary, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ([string]$record.databasePath).Equals($ExpectedDatabase, [System.StringComparison]::OrdinalIgnoreCase)) {
        return $null
    }
    $process = Get-ProcessInfo $processId
    if ($null -eq $process) {
        return $null
    }
    $commandLine = [string]$process.CommandLine
    if ($commandLine.IndexOf($ExpectedBinary, [System.StringComparison]::OrdinalIgnoreCase) -lt 0 -or
        $commandLine.IndexOf($ExpectedDatabase, [System.StringComparison]::OrdinalIgnoreCase) -lt 0) {
        return $null
    }
    return $process
}

function Stop-RecordedAgent([string]$PidPath, [string]$ExpectedBinary, [string]$ExpectedDatabase) {
    $record = Read-AgentRecord $PidPath
    if ($null -eq $record) {
        return $false
    }
    $process = Get-RecordedAgent $PidPath $ExpectedBinary $ExpectedDatabase
    if ($null -eq $process) {
        if ($null -ne $record.pid -and $null -ne (Get-Process -Id ([int]$record.pid) -ErrorAction SilentlyContinue)) {
            throw "Refusing to stop PID $($record.pid): its command line does not match this worktree demo record."
        }
        Remove-Item -LiteralPath $PidPath -Force
        return $false
    }
    Stop-Process -Id ([int]$process.ProcessId) -ErrorAction Stop
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        if ($null -eq (Get-ProcessInfo ([int]$process.ProcessId))) {
            Remove-Item -LiteralPath $PidPath -Force
            return $true
        }
        Start-Sleep -Milliseconds 250
    }
    throw "Recorded Block Agent PID $($process.ProcessId) did not stop."
}

function Wait-ForPortToClose([int]$ListeningPort) {
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        if (@(Get-Listeners $ListeningPort).Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 250
    }
    throw "Port $ListeningPort is still listening after the recorded process stopped."
}

function Get-LegacyStaticDemo([int]$ListeningPort) {
    if ($ListeningPort -ne 4173) {
        return $null
    }
    foreach ($listener in (Get-Listeners $ListeningPort)) {
        $process = Get-ProcessInfo ([int]$listener.OwningProcess)
        if ($null -eq $process) {
            continue
        }
        $commandLine = [string]$process.CommandLine
        if ($process.Name -ne "python.exe" -or
            $commandLine.IndexOf("-m http.server 4173 --bind 127.0.0.1", [System.StringComparison]::OrdinalIgnoreCase) -lt 0) {
            continue
        }
        try {
            $response = Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$ListeningPort/" -TimeoutSec 3
            if ($response.StatusCode -eq 200 -and $response.Content.Contains('id="hmi"') -and $response.Content.Contains("demo-shell.html")) {
                return $process
            }
        } catch {
            continue
        }
    }
    return $null
}

function Stop-LegacyStaticDemo([int]$ListeningPort) {
    $process = Get-LegacyStaticDemo $ListeningPort
    if ($null -eq $process) {
        return $false
    }
    Stop-Process -Id ([int]$process.ProcessId) -ErrorAction Stop
    Wait-ForPortToClose $ListeningPort
    Write-Host "Stopped the verified legacy static HMI demo PID $($process.ProcessId)."
    return $true
}

if ([string]::IsNullOrWhiteSpace($DataDirectory)) {
    $DataDirectory = Join-Path $repoCacheRoot "block-hmi-auth-demo\state"
}
$stateDirectory = Assert-ChildPath (Get-NormalizedPath $DataDirectory) $repoCacheRoot "DataDirectory"
$demoRoot = Assert-ChildPath (Split-Path $stateDirectory -Parent) $repoCacheRoot "Demo root"
$databasePath = Join-Path $stateDirectory "block-hmi-auth-demo.db"
$pidPath = Assert-ChildPath (Join-Path $demoRoot ("block-agent-{0}.pid.json" -f $Port)) $repoCacheRoot "PID record"
$binaryPath = Assert-ChildPath (Join-Path $demoRoot "bin\block-agent.exe") $repoCacheRoot "Demo binary"
$logDirectory = Assert-ChildPath (Join-Path $demoRoot "logs") $repoCacheRoot "Demo log directory"
$tempDirectory = Assert-ChildPath (Join-Path $demoRoot "tmp") $repoCacheRoot "Demo temporary directory"
$goCacheDirectory = Assert-ChildPath (Join-Path $demoRoot "gocache") $repoCacheRoot "Demo Go build cache"
$goTempDirectory = Assert-ChildPath (Join-Path $demoRoot "gotmp") $repoCacheRoot "Demo Go temporary directory"
$hmiStaticDirectory = (Resolve-Path (Join-Path $repoRoot "apps\block-hmi")).Path
$agentDirectory = (Resolve-Path (Join-Path $repoRoot "services\block-agent")).Path
$goExecutable = Join-Path $workspaceRoot ".tools\go1.26.5\go\bin\go.exe"
$verifiedRuntimeCache = Join-Path $workspaceRoot ".cache\block-v2-runtime-001"
$verifiedModuleCache = Join-Path $verifiedRuntimeCache "gomodcache"

if (-not (Test-Path -LiteralPath $goExecutable -PathType Leaf)) {
    throw "The workspace Go toolchain is missing: $goExecutable"
}
if (-not (Test-Path -LiteralPath $verifiedModuleCache -PathType Container)) {
    throw "The verified offline Go module cache is missing: $verifiedModuleCache"
}

$environmentNames = @("TEMP", "TMP", "TMPDIR", "GOTMPDIR", "GOCACHE", "GOMODCACHE", "GOPROXY")
$originalEnvironment = @{}
foreach ($name in $environmentNames) {
    $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

try {
    New-Item -ItemType Directory -Force -Path $stateDirectory, (Split-Path $binaryPath -Parent), $logDirectory, $tempDirectory, $goCacheDirectory, $goTempDirectory | Out-Null

    if ($Stop) {
        if (Stop-RecordedAgent $pidPath $binaryPath $databasePath) {
            Write-Host "Stopped Block HMI auth demo on port $Port."
        } else {
            Write-Host "No matching Block HMI auth demo is running on port $Port."
        }
        return
    }

    $stoppedRecordedAgent = Stop-RecordedAgent $pidPath $binaryPath $databasePath
    if ($stoppedRecordedAgent) {
        Wait-ForPortToClose $Port
    }

    $listeners = @(Get-Listeners $Port)
    if ($listeners.Count -gt 0) {
        if (-not (Stop-LegacyStaticDemo $Port)) {
            $owners = foreach ($listener in $listeners) {
                $process = Get-ProcessInfo ([int]$listener.OwningProcess)
                if ($null -eq $process) {
                    "PID $($listener.OwningProcess)"
                } else {
                    "PID $($process.ProcessId) $($process.Name): $($process.CommandLine)"
                }
            }
            throw "Port $Port is occupied by an unrecognised process. It was not stopped. $($owners -join '; ')"
        }
    }

    if ($FreshAuth -and (Test-Path -LiteralPath $stateDirectory -PathType Container)) {
        Remove-Item -LiteralPath $stateDirectory -Recurse -Force
        New-Item -ItemType Directory -Force -Path $stateDirectory | Out-Null
        Write-Host "Removed only the requested demo auth database directory: $stateDirectory"
    }

    $env:TEMP = $tempDirectory
    $env:TMP = $tempDirectory
    $env:TMPDIR = $tempDirectory
    $env:GOTMPDIR = $goTempDirectory
    $env:GOCACHE = $goCacheDirectory
    $env:GOMODCACHE = $verifiedModuleCache
    $env:GOPROXY = "off"

    Push-Location $agentDirectory
    try {
        & $goExecutable build -o $binaryPath .\cmd\block-agent
        if ($LASTEXITCODE -ne 0) {
            throw "block-agent build failed with exit code $LASTEXITCODE."
        }
    } finally {
        Pop-Location
    }

    $standardOutput = Join-Path $logDirectory "block-agent.out.log"
    $standardError = Join-Path $logDirectory "block-agent.err.log"
    $argumentList = @(
        "-local-http-address", "127.0.0.1:$Port",
        "-hmi-static-dir", ('"{0}"' -f $hmiStaticDirectory),
        "-state-db", ('"{0}"' -f $databasePath)
    )
    $process = Start-Process -FilePath $binaryPath -ArgumentList $argumentList -WorkingDirectory $repoRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput $standardOutput -RedirectStandardError $standardError
    [pscustomobject]@{
        pid          = $process.Id
        binaryPath   = $binaryPath
        databasePath = $databasePath
        startedAt    = (Get-Date).ToString("o")
    } | ConvertTo-Json | Set-Content -LiteralPath $pidPath -Encoding utf8

    $healthy = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$Port/healthz" -TimeoutSec 1
            if ($response.StatusCode -eq 200) {
                $healthy = $true
                break
            }
        } catch {
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $healthy) {
        Stop-RecordedAgent $pidPath $binaryPath $databasePath | Out-Null
        throw "Block Agent did not become healthy. Inspect $standardOutput and $standardError."
    }

    Write-Host "Block HMI auth demo is running at http://127.0.0.1:$Port/"
    Write-Host "PID: $($process.Id)"
    Write-Host "Database: $databasePath"
} finally {
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $originalEnvironment[$name], "Process")
    }
}
