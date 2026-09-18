param([switch]$WithGoCoverage)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$goCommand = Get-Command "go" -ErrorAction SilentlyContinue
$go = if ($goCommand) { $goCommand.Source } else { Join-Path $root ".tools\go\bin\go.exe" }
if (-not (Test-Path $go)) { throw "Go executable not found. Install Go or provide .tools/go/bin/go.exe." }
$nodeCommand = Get-Command "node.exe" -ErrorAction Stop
$node = $nodeCommand.Source
$vitest = Join-Path $root "frontend\node_modules\vitest\vitest.mjs"
if (-not (Test-Path $vitest)) { throw "Vitest executable not found. Run npm install in frontend first." }
$vitestArgs = @("run", "--configLoader", "native", "--pool=vmThreads", "--maxWorkers=1", "--no-file-parallelism")

Push-Location (Join-Path $root "frontend")
try {
    & $node $vitest @vitestArgs "--coverage"
    if ($LASTEXITCODE -ne 0) { throw "Frontend tests and coverage failed with exit code $LASTEXITCODE." }
} finally {
    Pop-Location
}

Push-Location (Join-Path $root "backend")
try {
    $env:GOCACHE = Join-Path $root "backend\.tmp\gocache"
    $env:GOTMPDIR = Join-Path $root "backend\.tmp\gotmp"
    $env:APPDATA = Join-Path $root "backend\.tmp\appdata"
    $env:GOTELEMETRY = "off"
    New-Item -ItemType Directory -Force $env:GOCACHE | Out-Null
    New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null
    New-Item -ItemType Directory -Force $env:APPDATA | Out-Null
    if ($WithGoCoverage) {
        $coverage = Join-Path $env:TEMP "novel2script-agent-go-coverage.out"
        $testsPassed = $false
        for ($attempt = 1; $attempt -le 3; $attempt++) {
            & $go test -coverprofile=$coverage ./...
            if ($LASTEXITCODE -eq 0) { $testsPassed = $true; break }
            if ($attempt -lt 3) { Start-Sleep -Seconds 2 }
        }
        if (-not $testsPassed) { throw "Backend tests failed after 3 attempts." }
        & $go tool cover -func=$coverage
        if ($LASTEXITCODE -ne 0) { throw "Backend coverage report failed with exit code $LASTEXITCODE." }
    } else {
        $testsPassed = $false
        for ($attempt = 1; $attempt -le 3; $attempt++) {
            & $go test ./...
            if ($LASTEXITCODE -eq 0) { $testsPassed = $true; break }
            if ($attempt -lt 3) { Start-Sleep -Seconds 2 }
        }
        if (-not $testsPassed) { throw "Backend tests failed after 3 attempts." }
    }
} finally {
    Pop-Location
}
