param(
    [string]$OutputDirectory = "",
    [switch]$SkipGate
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$releaseRoot = [IO.Path]::GetFullPath((Join-Path $root "release"))
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $releaseRoot "Novel2ScriptAgent" }
$output = [IO.Path]::GetFullPath($OutputDirectory)
if (-not $output.StartsWith($releaseRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "The package output must be inside $releaseRoot."
}
if (-not $SkipGate) { & (Join-Path $PSScriptRoot "release-gate.ps1") }

if (Test-Path -LiteralPath $output) { Remove-Item -LiteralPath $output -Recurse -Force }
New-Item -ItemType Directory -Force $output | Out-Null

$oldApiBase = $env:VITE_API_BASE
try {
    $env:VITE_API_BASE = "."
    Push-Location (Join-Path $root "frontend")
    try {
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "Frontend package build failed." }
    } finally { Pop-Location }
} finally {
    $env:VITE_API_BASE = $oldApiBase
}

$goCommand = Get-Command "go" -ErrorAction SilentlyContinue
$go = if ($goCommand) { $goCommand.Source } else { Join-Path $root ".tools\go\bin\go.exe" }
Push-Location (Join-Path $root "backend")
try {
    $env:GOCACHE = Join-Path $root "backend\.tmp\gocache"
    $env:GOTMPDIR = Join-Path $root "backend\.tmp\gotmp"
    $env:APPDATA = Join-Path $root "backend\.tmp\appdata"
    $env:GOTELEMETRY = "off"
    New-Item -ItemType Directory -Force $env:GOCACHE | Out-Null
    New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null
    New-Item -ItemType Directory -Force $env:APPDATA | Out-Null
    & $go build -trimpath -o (Join-Path $output "Novel2ScriptAgent.exe") ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw "Backend package build failed." }
} finally { Pop-Location }

Copy-Item -LiteralPath (Join-Path $root "frontend\dist") -Destination (Join-Path $output "web") -Recurse
Copy-Item -LiteralPath (Join-Path $root "backend\.env.example") -Destination (Join-Path $output ".env.example")
Copy-Item -LiteralPath (Join-Path $PSScriptRoot "release-files\Start-Novel2ScriptAgent.ps1") -Destination $output
Copy-Item -LiteralPath (Join-Path $PSScriptRoot "release-files\Stop-Novel2ScriptAgent.ps1") -Destination $output
Copy-Item -LiteralPath (Join-Path $PSScriptRoot "release-files\README.txt") -Destination $output
$tools = Join-Path $output "tools"
New-Item -ItemType Directory -Force $tools | Out-Null
foreach ($name in @("backup-data.ps1", "restore-data.ps1", "view-logs.ps1")) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination $tools
}
New-Item -ItemType Directory -Force (Join-Path $output "data") | Out-Null
Write-Host "Release package created: $output"
