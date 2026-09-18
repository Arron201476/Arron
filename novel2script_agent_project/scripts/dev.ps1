$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$goCommand = Get-Command "go" -ErrorAction SilentlyContinue
$go = if ($goCommand) { $goCommand.Source } else { Join-Path $root ".tools\go\bin\go.exe" }
$npmCommand = Get-Command "npm.cmd" -ErrorAction SilentlyContinue
$npm = if ($npmCommand) { $npmCommand.Source } else { "npm" }
if (-not (Test-Path $go)) { throw "Go executable not found. Install Go or provide .tools/go/bin/go.exe." }

function Test-Port([int]$Port) {
    return $null -ne (Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
}

if (-not (Test-Port 8831)) {
    $env:GOCACHE = Join-Path $root "backend\.tmp\gocache"
    $env:GOTMPDIR = Join-Path $env:TEMP ("novel2script-agent-gotmp-" + $PID)
    $env:GOTELEMETRY = "off"
    New-Item -ItemType Directory -Force $env:GOCACHE | Out-Null
    New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null
    Start-Process -FilePath $go -ArgumentList "run", "./cmd/server" -WorkingDirectory (Join-Path $root "backend") -WindowStyle Hidden
}
if (-not (Test-Port 8832)) {
    Start-Process -FilePath $npm -ArgumentList "run", "dev" -WorkingDirectory (Join-Path $root "frontend") -WindowStyle Hidden
}

Write-Host "Novel2Script Agent: http://127.0.0.1:8832"
Write-Host "Backend health: http://127.0.0.1:8831/healthz"
