[CmdletBinding()]
param(
    [switch]$FreshAuth,
    [switch]$Stop,
    [ValidateRange(1024, 65535)]
    [int]$Port = 8444,
    [string]$DataDirectory = "",
    [string]$TLSCertificatePath = "",
    [string]$TLSPrivateKeyPath = "",
    [string]$TLSCAPath = "",
    [string]$StateRoot = "",
    [string]$GoProxy = "https://goproxy.cn|https://proxy.golang.org|direct"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "build-state.ps1")

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($StateRoot)) {
    $StateRoot = Join-Path $repoRoot ".cache\block-hmi-auth-demo"
}
$repoCacheRoot = Resolve-BlockBuildStateRoot -RepoRoot $repoRoot `
    -StateRoot $StateRoot -Owner "block-build-tools"

function Get-NormalizedPath([string]$Path) {
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $repoRoot $Path))
}

function Get-StatePath([string]$Path) {
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $repoCacheRoot $Path))
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

function Initialize-DemoStateDirectory([string]$Path, [switch]$Fresh) {
    $markerPath = Join-Path $Path ".block-hmi-auth-demo-state.json"
    $canonicalRepoRoot = ConvertTo-BlockBuildCanonicalPath $repoRoot
    $entries = @()
    if (Test-Path -LiteralPath $Path) {
        $item = Get-Item -LiteralPath $Path -Force
        if (-not $item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "DataDirectory must be a non-reparse directory: $Path"
        }
        $entries = @(Get-ChildItem -LiteralPath $Path -Force)
        if ($entries.Count -gt 0) {
            if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) {
                throw "DataDirectory is non-empty and is not owned by this HMI demo: $Path"
            }
            try {
                $marker = Get-Content -LiteralPath $markerPath -Raw -Encoding utf8 | ConvertFrom-Json -ErrorAction Stop
            } catch {
                throw "DataDirectory owner marker is invalid: $markerPath"
            }
            if ($marker.format -ne "block-hmi-auth-demo-state-v1" -or
                -not ([string]$marker.repoRoot).Equals($canonicalRepoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
                throw "DataDirectory owner marker does not belong to this HMI demo worktree: $markerPath"
            }
        }
    } else {
        New-Item -ItemType Directory -Force -Path $Path | Out-Null
    }

    # A first run has no data directory to delete. An existing non-empty
    # directory, however, is deleted only after the owner-marker validation
    # above; an unowned directory always fails before reaching this point.
    if ($Fresh -and $entries.Count -gt 0) {
        Assert-BlockBuildNoReparseDescendants -Path $Path -Description "HMI demo DataDirectory"
        Remove-Item -LiteralPath $Path -Recurse -Force
        New-Item -ItemType Directory -Force -Path $Path | Out-Null
    }

    if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) {
        $marker = [ordered]@{
            format = "block-hmi-auth-demo-state-v1"
            repoRoot = $canonicalRepoRoot
        }
        [System.IO.File]::WriteAllText($markerPath, ($marker | ConvertTo-Json -Compress) + [Environment]::NewLine, (New-Object System.Text.UTF8Encoding($false)))
    }
}

function Invoke-QuietNativeCommand([string]$Executable, [string[]]$Arguments) {
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        & $Executable @Arguments 2>$null | Out-Null
        return $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
}

function Set-EnvironmentValues([System.Collections.IDictionary]$Values) {
    foreach ($name in $Values.Keys) {
        $value = $Values[$name]
        if ($null -eq $value) {
            Remove-Item -LiteralPath ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($name, [string]$value, "Process")
        }
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

function Resolve-DemoTLS([string]$DemoRoot, [string]$CertificatePath, [string]$PrivateKeyPath, [string]$CAPath) {
    $provided = @(@($CertificatePath, $PrivateKeyPath, $CAPath) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($provided.Count -gt 0) {
        if ($provided.Count -ne 3) {
            throw "TLSCertificatePath, TLSPrivateKeyPath, and TLSCAPath must be provided together."
        }
        $resolvedCertificate = Get-NormalizedPath $CertificatePath
        $resolvedPrivateKey = Get-NormalizedPath $PrivateKeyPath
        $resolvedCA = Get-NormalizedPath $CAPath
        foreach ($path in @($resolvedCertificate, $resolvedPrivateKey, $resolvedCA)) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
                throw "Required TLS test material is missing: $path"
            }
        }
        return [pscustomobject]@{ CertificatePath = $resolvedCertificate; PrivateKeyPath = $resolvedPrivateKey; CAPath = $resolvedCA }
    }

    $tlsDirectory = Assert-ChildPath (Join-Path $DemoRoot "tls") $repoCacheRoot "TLS test material directory"
    $certificate = Join-Path $tlsDirectory "local-hmi.crt"
    $privateKey = Join-Path $tlsDirectory "local-hmi.key"
    $ca = Join-Path $tlsDirectory "local-hmi-ca.crt"
    $caKey = Join-Path $tlsDirectory "local-hmi-ca.key"
    $config = Join-Path $tlsDirectory "openssl.cnf"
    $csr = Join-Path $tlsDirectory "local-hmi.csr"
    $serial = Join-Path $tlsDirectory "local-hmi-ca.srl"
    $allPresent = @($certificate, $privateKey, $ca, $caKey) | ForEach-Object { Test-Path -LiteralPath $_ -PathType Leaf }
    if ($allPresent -notcontains $false) {
        return [pscustomobject]@{ CertificatePath = $certificate; PrivateKeyPath = $privateKey; CAPath = $ca }
    }
    if ($allPresent -contains $true) {
        throw "TLS test material is incomplete under $tlsDirectory. Remove only that demo TLS directory before retrying."
    }
    $openssl = Get-Command openssl -ErrorAction SilentlyContinue
    $opensslPath = if ($null -ne $openssl) { $openssl.Source } elseif (Test-Path -LiteralPath "C:\Program Files\Git\usr\bin\openssl.exe" -PathType Leaf) { "C:\Program Files\Git\usr\bin\openssl.exe" } else { "" }
    if ([string]::IsNullOrWhiteSpace($opensslPath)) {
        throw "OpenSSL is required to generate runtime-only demo TLS material, or pass explicit TLSCertificatePath/TLSPrivateKeyPath/TLSCAPath values."
    }
    New-Item -ItemType Directory -Force -Path $tlsDirectory | Out-Null
    @"
[req]
distinguished_name = distinguished_name
prompt = no
[distinguished_name]
CN = Block local HMI test CA
[v3_ca]
basicConstraints = critical, CA:true
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
[server]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
"@ | Set-Content -LiteralPath $config -Encoding utf8
    if ((Invoke-QuietNativeCommand $opensslPath @("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "2", "-keyout", $caKey, "-out", $ca, "-config", $config, "-extensions", "v3_ca")) -ne 0) { throw "OpenSSL failed to create the demo public CA." }
    if ((Invoke-QuietNativeCommand $opensslPath @("req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", $privateKey, "-out", $csr, "-subj", "/CN=localhost")) -ne 0) { throw "OpenSSL failed to create the demo server certificate request." }
    if ((Invoke-QuietNativeCommand $opensslPath @("x509", "-req", "-in", $csr, "-CA", $ca, "-CAkey", $caKey, "-CAcreateserial", "-out", $certificate, "-days", "2", "-sha256", "-extfile", $config, "-extensions", "server")) -ne 0) { throw "OpenSSL failed to sign the demo server certificate." }
    Remove-Item -LiteralPath $csr, $serial -Force -ErrorAction SilentlyContinue
    return [pscustomobject]@{ CertificatePath = $certificate; PrivateKeyPath = $privateKey; CAPath = $ca }
}

function Test-StrictHealth([string]$ClientPath, [int]$ListeningPort, [string]$CAPath) {
    return (Invoke-QuietNativeCommand $ClientPath @("-url", "https://127.0.0.1:$ListeningPort/healthz", "-ca", $CAPath, "-method", "GET")) -eq 0
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

if ([string]::IsNullOrWhiteSpace($DataDirectory)) {
    $DataDirectory = Join-Path $repoCacheRoot "state"
}
$stateDirectory = Assert-ChildPath (Get-StatePath $DataDirectory) $repoCacheRoot "DataDirectory"
$demoRoot = [System.IO.Path]::GetFullPath((Split-Path $stateDirectory -Parent)).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
if (-not $demoRoot.Equals($repoCacheRoot, [System.StringComparison]::OrdinalIgnoreCase) -and
    -not (Test-BlockBuildChildPath $demoRoot $repoCacheRoot)) {
    throw "Demo root must remain inside the validated StateRoot: $repoCacheRoot"
}
if (-not (ConvertTo-BlockBuildCanonicalPath (Split-Path -Parent $stateDirectory)).Equals((ConvertTo-BlockBuildCanonicalPath $repoCacheRoot), [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "DataDirectory must be a direct child of the validated StateRoot: $repoCacheRoot"
}
Assert-BlockBuildNoReparseComponents -Path $stateDirectory -StartPath $repoCacheRoot -Description "DataDirectory"
$databasePath = Join-Path $stateDirectory "block-hmi-auth-demo.db"
$pidPath = Assert-ChildPath (Join-Path $demoRoot ("block-agent-{0}.pid.json" -f $Port)) $repoCacheRoot "PID record"
$binaryPath = Assert-ChildPath (Join-Path $demoRoot "bin\block-agent.exe") $repoCacheRoot "Demo binary"
$strictHTTPSClientPath = Assert-ChildPath (Join-Path $demoRoot "bin\strict-local-https-client.exe") $repoCacheRoot "Demo HTTPS verification client"
$logDirectory = Assert-ChildPath (Join-Path $demoRoot "logs") $repoCacheRoot "Demo log directory"
$tempDirectory = Assert-ChildPath (Join-Path $demoRoot "tmp") $repoCacheRoot "Demo temporary directory"
$goCacheDirectory = Assert-ChildPath (Join-Path $demoRoot "gocache") $repoCacheRoot "Demo Go build cache"
$goTempDirectory = Assert-ChildPath (Join-Path $demoRoot "gotmp") $repoCacheRoot "Demo Go temporary directory"
$hmiStaticDirectory = (Resolve-Path (Join-Path $repoRoot "apps\block-hmi")).Path
$agentDirectory = (Resolve-Path (Join-Path $repoRoot "services\block-agent")).Path
$strictHTTPSClientSource = (Resolve-Path (Join-Path $PSScriptRoot "strict-local-https-client.go")).Path
$bootstrapScript = Join-Path $PSScriptRoot "bootstrap-build-tools.ps1"

$environmentSnapshot = $null

try {
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

    Initialize-DemoStateDirectory -Path $stateDirectory -Fresh:$FreshAuth
    New-Item -ItemType Directory -Force -Path (Split-Path $binaryPath -Parent), $logDirectory, $tempDirectory, $goCacheDirectory, $goTempDirectory | Out-Null
    if ($FreshAuth) {
        Write-Host "Removed only the requested, demo-owned auth database directory: $stateDirectory"
    }

    $tls = Resolve-DemoTLS $demoRoot $TLSCertificatePath $TLSPrivateKeyPath $TLSCAPath

    $tools = & $bootstrapScript -StateRoot $repoCacheRoot -PrepareGoModules -GoProxy $GoProxy
    if ($null -eq $tools) {
        throw "Build tool bootstrap did not return a toolchain."
    }

    # Bootstrap restores its own process environment before returning. Snapshot exactly
    # what it is about to override here, including dynamically named GIT_CONFIG_* and
    # inherited NPM_CONFIG_* variables. This keeps the demo build isolated without
    # leaking its private cache/tool settings to the invoking PowerShell session.
    $clearEnvironmentNames = @(
        $tools.ClearEnvironmentNames +
        (Get-BlockBuildEnvironmentNamesMatching -Pattern '^GIT_CONFIG_') |
            Select-Object -Unique
    )
    $environmentSnapshot = Get-EnvironmentSnapshot @($tools.Environment.Keys + $clearEnvironmentNames)
    $clearInheritedEnvironment = [ordered]@{}
    foreach ($name in $clearEnvironmentNames) {
        $clearInheritedEnvironment[$name] = $null
    }
    Set-EnvironmentValues $clearInheritedEnvironment
    Set-EnvironmentValues $tools.Environment
    Assert-BlockBuildEnvironmentPatternUnset -Pattern '^GIT_CONFIG_' -Description "Block HMI auth demo build"
    Set-EnvironmentValues ([ordered]@{
        CGO_ENABLED = "0"
        GOOS        = $null
        GOARCH      = $null
    })
    $goExecutable = $tools.GoBinary

    Push-Location $agentDirectory
    try {
        & $goExecutable build -buildvcs=false -mod=readonly -trimpath -o $binaryPath .\cmd\block-agent
        if ($LASTEXITCODE -ne 0) {
            throw "block-agent build failed with exit code $LASTEXITCODE."
        }
        & $goExecutable build -buildvcs=false -mod=readonly -trimpath -o $strictHTTPSClientPath $strictHTTPSClientSource
        if ($LASTEXITCODE -ne 0) {
            throw "strict local HTTPS client build failed with exit code $LASTEXITCODE."
        }
    } finally {
        Pop-Location
    }

    $standardOutput = Join-Path $logDirectory "block-agent.out.log"
    $standardError = Join-Path $logDirectory "block-agent.err.log"
    $argumentList = @(
        "-local-https-address", "127.0.0.1:$Port",
        "-local-tls-cert", ('"{0}"' -f $tls.CertificatePath),
        "-local-tls-key", ('"{0}"' -f $tls.PrivateKeyPath),
        "-hmi-static-dir", ('"{0}"' -f $hmiStaticDirectory),
        "-state-db", ('"{0}"' -f $databasePath)
    )
    $process = Start-Process -FilePath $binaryPath -ArgumentList $argumentList -WorkingDirectory $repoRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput $standardOutput -RedirectStandardError $standardError
    [pscustomobject]@{
        pid          = $process.Id
        binaryPath   = $binaryPath
        databasePath = $databasePath
        certificatePath = $tls.CertificatePath
        caPath       = $tls.CAPath
        strictHTTPSClientPath = $strictHTTPSClientPath
        startedAt    = (Get-Date).ToString("o")
    } | ConvertTo-Json | Set-Content -LiteralPath $pidPath -Encoding utf8

    $healthy = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if (Test-StrictHealth $strictHTTPSClientPath $Port $tls.CAPath) {
            $healthy = $true
            break
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $healthy) {
        Stop-RecordedAgent $pidPath $binaryPath $databasePath | Out-Null
        throw "Block Agent did not become healthy. Inspect $standardOutput and $standardError."
    }

    Write-Host "Block HMI auth demo is running at https://127.0.0.1:$Port/"
    Write-Host "PID: $($process.Id)"
    Write-Host "Database: $databasePath"
} finally {
    if ($null -ne $environmentSnapshot) {
        Restore-Environment $environmentSnapshot
    }
}
