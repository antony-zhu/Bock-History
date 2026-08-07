[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$repoCacheRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot ".cache"))
$testRoot = [System.IO.Path]::GetFullPath((Join-Path $repoCacheRoot "block-hmi-auth-persistence-test"))
$stateDirectory = Join-Path $testRoot "state"
$startScript = Join-Path $PSScriptRoot "start-block-hmi-auth-demo.ps1"

function Assert-TestPath([string]$Path) {
    $separator = [System.IO.Path]::DirectorySeparatorChar
    $fullPath = [System.IO.Path]::GetFullPath($Path).TrimEnd($separator)
    $fullParent = [System.IO.Path]::GetFullPath($repoCacheRoot).TrimEnd($separator)
    if ($fullPath -eq $fullParent -or -not $fullPath.StartsWith($fullParent + $separator, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "The integration test directory must remain under $fullParent."
    }
    return $fullPath
}

function Get-FreeLoopbackPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

function Invoke-AgentJSON([string]$BaseUrl, [string]$CAPath, [string]$Method, [string]$Path, [object]$Body) {
    $strictHTTPSClient = Join-Path $testRoot "bin\strict-local-https-client.exe"
    if (-not (Test-Path -LiteralPath $strictHTTPSClient -PathType Leaf)) {
        throw "Missing strict HTTPS test client: $strictHTTPSClient"
    }
    $requestID = [Guid]::NewGuid().ToString("N")
    $bodyPath = Join-Path $testRoot "request-$requestID.json"
    try {
        $arguments = @("-url", ($BaseUrl + $Path), "-ca", $CAPath, "-method", $Method)
        if ($null -ne $Body) {
            $payload = $Body | ConvertTo-Json -Compress
            [System.IO.File]::WriteAllText($bodyPath, $payload, [System.Text.UTF8Encoding]::new($false))
            $arguments += @("-body-file", $bodyPath)
        }
        $output = (& $strictHTTPSClient @arguments 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            throw "Strict HTTPS request failed for $Method ${Path}: $output"
        }
        try {
            $response = $output | ConvertFrom-Json
        } catch {
            throw "Strict HTTPS request returned invalid JSON for $Method ${Path}: $output"
        }
        $json = $null
        if ($response.contentType -like "application/json*" -and -not [string]::IsNullOrWhiteSpace($response.body)) {
            $json = $response.body | ConvertFrom-Json
        }
        return [pscustomobject]@{
            StatusCode = [int]$response.statusCode
            Json       = $json
            SetCookie  = [string]$response.setCookie
        }
    } finally {
        Remove-Item -LiteralPath $bodyPath -Force -ErrorAction SilentlyContinue
    }
}

function Assert-NoPlaintextBusinessListener([int]$ListeningPort) {
    foreach ($plainPort in @(8080, 8081)) {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $plainPort -ErrorAction SilentlyContinue)
        if ($listeners.Count -gt 0) {
            throw "Plaintext business port $plainPort is listening."
        }
    }
    # This is an explicit negative probe: a TLS-only listener must not return an HTTP response or redirect.
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $connect = $client.ConnectAsync([System.Net.IPAddress]::Loopback, $ListeningPort)
        if (-not $connect.Wait(1000) -or -not $client.Connected) {
            throw "TLS-only business listener did not accept its expected loopback connection."
        }
        $stream = $client.GetStream()
        $stream.ReadTimeout = 3000
        $payload = [System.Text.Encoding]::ASCII.GetBytes("GET /healthz HTTP/1.1`r`nHost: 127.0.0.1`r`n`r`n")
        $stream.Write($payload, 0, $payload.Length)
        $buffer = New-Object byte[] 1024
        try {
            $read = $stream.Read($buffer, 0, $buffer.Length)
        } catch [System.IO.IOException] {
            return
        }
        if ($read -gt 0) {
            throw "TLS-only business listener returned plaintext data: $([System.Text.Encoding]::ASCII.GetString($buffer, 0, $read))"
        }
    } finally {
        $client.Dispose()
    }
}

function Assert-Status($Response, [int]$Expected, [string]$Description) {
    if ($Response.StatusCode -ne $Expected) {
        throw "$Description returned HTTP $($Response.StatusCode), expected $Expected."
    }
}

function Assert-NoCookie($Response, [string]$Description) {
    if (-not [string]::IsNullOrEmpty($Response.SetCookie)) {
        throw "$Description unexpectedly returned Set-Cookie."
    }
}

if (-not (Test-Path -LiteralPath $startScript -PathType Leaf)) {
    throw "Missing demo launcher: $startScript"
}

$testRoot = Assert-TestPath $testRoot
$port = Get-FreeLoopbackPort
$baseUrl = "https://127.0.0.1:$port"
$username = "integration-admin"
$password = "integration-auth-password"
$stopped = $false

try {
    if (Test-Path -LiteralPath $testRoot) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $testRoot | Out-Null

    & $startScript -FreshAuth -Port $port -DataDirectory $stateDirectory
    $caPath = Join-Path $testRoot "tls\local-hmi-ca.crt"
    if (-not (Test-Path -LiteralPath $caPath -PathType Leaf)) {
        throw "Demo launcher did not create the expected runtime-only public CA: $caPath"
    }
    Assert-NoPlaintextBusinessListener $port

    $response = Invoke-AgentJSON $baseUrl $caPath "GET" "/api/v2/auth/initial-admin" $null
    Assert-Status $response 200 "Fresh bootstrap status"
    if ($null -eq $response.Json -or $response.Json.bootstrapRequired -ne $true) {
        throw "Fresh database did not report bootstrapRequired=true."
    }

    $response = Invoke-AgentJSON $baseUrl $caPath "POST" "/api/v2/auth/initial-admin" @{
        username = $username; password = $password; confirmPassword = $password
    }
    Assert-Status $response 201 "Initial administrator creation"
    Assert-NoCookie $response "Initial administrator creation"

    $response = Invoke-AgentJSON $baseUrl $caPath "POST" "/api/v2/auth/login" @{
        username = $username; password = "$password-wrong"
    }
    Assert-Status $response 401 "Wrong password login"
    Assert-NoCookie $response "Wrong password login"

    $response = Invoke-AgentJSON $baseUrl $caPath "POST" "/api/v2/auth/login" @{
        username = $username; password = $password
    }
    Assert-Status $response 200 "Correct password login"
    Assert-NoCookie $response "Correct password login"

    $response = Invoke-AgentJSON $baseUrl $caPath "PUT" "/api/v2/config/session" @{ idleTimeoutSeconds = 180 }
    Assert-Status $response 200 "Idle timeout update"
    if ($null -eq $response.Json -or $response.Json.idleTimeoutSeconds -ne 180) {
        throw "Idle timeout update did not return 180 seconds."
    }

    $response = Invoke-AgentJSON $baseUrl $caPath "GET" "/api/v2/config/session" $null
    Assert-Status $response 200 "Idle timeout read"
    if ($null -eq $response.Json -or $response.Json.idleTimeoutSeconds -ne 180) {
        throw "Idle timeout read did not return the persisted 180 seconds."
    }

    foreach ($retiredPath in @("/api/v2/auth/activity", "/api/v2/auth/logout")) {
        $response = Invoke-AgentJSON $baseUrl $caPath "POST" $retiredPath @{}
        Assert-Status $response 404 "Retired endpoint $retiredPath"
    }

    & $startScript -Stop -Port $port -DataDirectory $stateDirectory

    & $startScript -Port $port -DataDirectory $stateDirectory

    $response = Invoke-AgentJSON $baseUrl $caPath "GET" "/api/v2/auth/initial-admin" $null
    Assert-Status $response 200 "Restart bootstrap status"
    if ($null -eq $response.Json -or $response.Json.bootstrapRequired -ne $false) {
        throw "Restarted database did not preserve bootstrapRequired=false."
    }

    $response = Invoke-AgentJSON $baseUrl $caPath "POST" "/api/v2/auth/login" @{
        username = $username; password = $password
    }
    Assert-Status $response 200 "Restarted correct password login"
    Assert-NoCookie $response "Restarted correct password login"

    $response = Invoke-AgentJSON $baseUrl $caPath "GET" "/api/v2/config/session" $null
    Assert-Status $response 200 "Restarted idle timeout read"
    if ($null -eq $response.Json -or $response.Json.idleTimeoutSeconds -ne 180) {
        throw "Restarted database did not preserve the 180-second idle timeout."
    }

    & $startScript -Stop -Port $port -DataDirectory $stateDirectory
    $stopped = $true
    Write-Host "Block HMI auth persistence integration test passed."
} finally {
    if (-not $stopped) {
        try {
            & $startScript -Stop -Port $port -DataDirectory $stateDirectory
            $stopped = $true
        } catch {
            Write-Warning "Could not stop the test Block Agent: $($_.Exception.Message)"
        }
    }
    if ($stopped -and (Test-Path -LiteralPath $testRoot -PathType Container)) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force
    }
}
