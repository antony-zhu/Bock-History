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

function Invoke-AgentJSON([string]$BaseUrl, [string]$Method, [string]$Path, [object]$Body) {
    $request = [System.Net.HttpWebRequest]::Create($BaseUrl + $Path)
    $request.Method = $Method
    $request.Accept = "application/json"
    if ($null -ne $Body) {
        $payload = $Body | ConvertTo-Json -Compress
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($payload)
        $request.ContentType = "application/json"
        $request.ContentLength = $bytes.Length
        $stream = $request.GetRequestStream()
        try {
            $stream.Write($bytes, 0, $bytes.Length)
        } finally {
            $stream.Dispose()
        }
    }

    try {
        $response = [System.Net.HttpWebResponse]$request.GetResponse()
    } catch [System.Net.WebException] {
        if ($null -eq $_.Exception.Response) {
            throw
        }
        $response = [System.Net.HttpWebResponse]$_.Exception.Response
    }
    try {
        $reader = [System.IO.StreamReader]::new($response.GetResponseStream())
        try {
            $text = $reader.ReadToEnd()
        } finally {
            $reader.Dispose()
        }
        $json = $null
        if ($response.ContentType -like "application/json*" -and -not [string]::IsNullOrWhiteSpace($text)) {
            $json = $text | ConvertFrom-Json
        }
        return [pscustomobject]@{
            StatusCode = [int]$response.StatusCode
            Json       = $json
            SetCookie  = $response.Headers["Set-Cookie"]
        }
    } finally {
        $response.Dispose()
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
$baseUrl = "http://127.0.0.1:$port"
$username = "integration-admin"
$password = "integration-auth-password"
$stopped = $false

try {
    if (Test-Path -LiteralPath $testRoot) {
        Remove-Item -LiteralPath $testRoot -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $testRoot | Out-Null

    & $startScript -FreshAuth -Port $port -DataDirectory $stateDirectory

    $response = Invoke-AgentJSON $baseUrl "GET" "/api/v2/auth/initial-admin" $null
    Assert-Status $response 200 "Fresh bootstrap status"
    if ($null -eq $response.Json -or $response.Json.bootstrapRequired -ne $true) {
        throw "Fresh database did not report bootstrapRequired=true."
    }

    $response = Invoke-AgentJSON $baseUrl "POST" "/api/v2/auth/initial-admin" @{
        username = $username; password = $password; confirmPassword = $password
    }
    Assert-Status $response 201 "Initial administrator creation"
    Assert-NoCookie $response "Initial administrator creation"

    $response = Invoke-AgentJSON $baseUrl "POST" "/api/v2/auth/login" @{
        username = $username; password = "$password-wrong"
    }
    Assert-Status $response 401 "Wrong password login"
    Assert-NoCookie $response "Wrong password login"

    $response = Invoke-AgentJSON $baseUrl "POST" "/api/v2/auth/login" @{
        username = $username; password = $password
    }
    Assert-Status $response 200 "Correct password login"
    Assert-NoCookie $response "Correct password login"

    $response = Invoke-AgentJSON $baseUrl "PUT" "/api/v2/config/session" @{ idleTimeoutSeconds = 180 }
    Assert-Status $response 200 "Idle timeout update"
    if ($null -eq $response.Json -or $response.Json.idleTimeoutSeconds -ne 180) {
        throw "Idle timeout update did not return 180 seconds."
    }

    $response = Invoke-AgentJSON $baseUrl "GET" "/api/v2/config/session" $null
    Assert-Status $response 200 "Idle timeout read"
    if ($null -eq $response.Json -or $response.Json.idleTimeoutSeconds -ne 180) {
        throw "Idle timeout read did not return the persisted 180 seconds."
    }

    foreach ($retiredPath in @("/api/v2/auth/activity", "/api/v2/auth/logout")) {
        $response = Invoke-AgentJSON $baseUrl "POST" $retiredPath @{}
        Assert-Status $response 404 "Retired endpoint $retiredPath"
    }

    & $startScript -Stop -Port $port -DataDirectory $stateDirectory

    & $startScript -Port $port -DataDirectory $stateDirectory

    $response = Invoke-AgentJSON $baseUrl "GET" "/api/v2/auth/initial-admin" $null
    Assert-Status $response 200 "Restart bootstrap status"
    if ($null -eq $response.Json -or $response.Json.bootstrapRequired -ne $false) {
        throw "Restarted database did not preserve bootstrapRequired=false."
    }

    $response = Invoke-AgentJSON $baseUrl "POST" "/api/v2/auth/login" @{
        username = $username; password = $password
    }
    Assert-Status $response 200 "Restarted correct password login"
    Assert-NoCookie $response "Restarted correct password login"

    $response = Invoke-AgentJSON $baseUrl "GET" "/api/v2/config/session" $null
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
