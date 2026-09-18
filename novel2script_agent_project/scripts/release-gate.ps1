$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$goCommand = Get-Command "go" -ErrorAction SilentlyContinue
$go = if ($goCommand) { $goCommand.Source } else { Join-Path $root ".tools\go\bin\go.exe" }
if (-not (Test-Path $go)) { throw "Go executable not found." }

& (Join-Path $PSScriptRoot "test.ps1")
if ($LASTEXITCODE -ne 0) { throw "Test gate failed." }

Push-Location (Join-Path $root "backend")
try {
    $env:GOCACHE = Join-Path $root "backend\.tmp\gocache"
    $env:GOTMPDIR = Join-Path $root "backend\.tmp\gotmp"
    $env:APPDATA = Join-Path $root "backend\.tmp\appdata"
    $env:GOTELEMETRY = "off"
    New-Item -ItemType Directory -Force $env:GOCACHE | Out-Null
    New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null
    New-Item -ItemType Directory -Force $env:APPDATA | Out-Null
    $vetPassed = $false
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        & $go vet ./...
        if ($LASTEXITCODE -eq 0) { $vetPassed = $true; break }
        if ($attempt -lt 3) { Start-Sleep -Seconds 2 }
    }
    if (-not $vetPassed) { throw "Go vet failed after 3 attempts." }
} finally {
    Pop-Location
}

Push-Location (Join-Path $root "frontend")
try {
    npm run gate:editor
    if ($LASTEXITCODE -ne 0) { throw "Editor gate failed." }
} finally {
    Pop-Location
}

& (Join-Path $PSScriptRoot "build.ps1")
if ($LASTEXITCODE -ne 0) { throw "Build gate failed." }

$preview = Join-Path $root "design\preview.html"
if (-not (Test-Path $preview)) { throw "Unified design preview is missing." }
Write-Host "Release gate passed."
