$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$goCommand = Get-Command "go" -ErrorAction SilentlyContinue
$go = if ($goCommand) { $goCommand.Source } else { Join-Path $root ".tools\go\bin\go.exe" }
if (-not (Test-Path $go)) { throw "Go executable not found. Install Go or provide .tools/go/bin/go.exe." }

Push-Location (Join-Path $root "frontend")
try {
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "Frontend build failed with exit code $LASTEXITCODE." }
} finally {
    Pop-Location
}

Push-Location (Join-Path $root "backend")
try {
    $output = Join-Path $env:TEMP "novel2script-agent-server-check.exe"
    $env:GOCACHE = Join-Path $root "backend\.tmp\gocache"
    $env:GOTMPDIR = Join-Path $env:TEMP ("novel2script-agent-gotmp-" + $PID)
    $env:APPDATA = Join-Path $root "backend\.tmp\appdata"
    $env:GOTELEMETRY = "off"
    New-Item -ItemType Directory -Force $env:GOCACHE | Out-Null
    New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null
    New-Item -ItemType Directory -Force $env:APPDATA | Out-Null
    & $go build -o $output ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw "Backend build failed with exit code $LASTEXITCODE." }
} finally {
    Pop-Location
}
