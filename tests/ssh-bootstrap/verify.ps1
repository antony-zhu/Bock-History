param(
    [string]$GoBinary = 'D:\codex\Block-DMP\.tools\go1.26.5\go\bin\go.exe',
    [string]$NodeBinary = 'node'
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$workspaceRoot = (Resolve-Path (Join-Path $repoRoot '..\..')).Path
$cacheRoot = Join-Path $workspaceRoot '.cache\implementation\ssh-bootstrap-v1\block'

$env:TEMP = Join-Path $cacheRoot 'tmp'
$env:TMP = $env:TEMP
$env:TMPDIR = $env:TEMP
$env:GOTMPDIR = Join-Path $cacheRoot 'gotmp'
$env:GOCACHE = Join-Path $cacheRoot 'gocache'
$env:GOMODCACHE = Join-Path $workspaceRoot '.cache\go\gopath\pkg\mod'
New-Item -ItemType Directory -Force -Path $env:TEMP, $env:GOTMPDIR, $env:GOCACHE | Out-Null

$commonContract = Join-Path $workspaceRoot 'repos\Common\contracts\ssh-bootstrap\v1\validate_contract.mjs'
& $NodeBinary $commonContract
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Push-Location (Join-Path $repoRoot 'services\block-agent')
try {
    & $GoBinary test ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & $GoBinary vet ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & $GoBinary test -race -p 1 ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOARCH = 'arm64'
    $binary = Join-Path $cacheRoot 'bin\ssh-bootstrapd'
    New-Item -ItemType Directory -Force -Path (Split-Path $binary) | Out-Null
    & $GoBinary build -trimpath -ldflags '-X main.version=verification' -o $binary ./cmd/ssh-bootstrapd
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
finally {
    Pop-Location
}

Write-Output 'Block SSH Bootstrap v1 contract, Go test/vet/race and linux/arm64 build passed.'
