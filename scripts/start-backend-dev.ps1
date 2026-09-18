param(
    [string]$Listen = "127.0.0.1:8850",
    [switch]$DemoExecution = $true
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$backendRoot = Join-Path $projectRoot "backend"
$go = Join-Path $projectRoot ".tools\go-sdk\go\bin\go.exe"
$ffmpeg = Join-Path $projectRoot ".tools\ffmpeg-package\ffmpeg-8.1.2-essentials_build\bin\ffmpeg.exe"
$ffprobe = Join-Path $projectRoot ".tools\ffmpeg-package\ffmpeg-8.1.2-essentials_build\bin\ffprobe.exe"
$envFile = Join-Path $backendRoot ".env.local"

foreach ($required in @($go, $ffmpeg, $ffprobe)) {
    if (-not (Test-Path -LiteralPath $required)) {
        throw "Required local runtime is missing: $required"
    }
}

if (Test-Path -LiteralPath $envFile) {
    foreach ($line in Get-Content -LiteralPath $envFile -Encoding utf8) {
        if ($line -match '^\s*#' -or $line -notmatch '=') {
            continue
        }
        $name, $value = $line -split '=', 2
        $name = $name.Trim()
        if ($name -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
            throw "Invalid environment variable name in .env.local: $name"
        }
        [Environment]::SetEnvironmentVariable($name, $value.Trim(), "Process")
    }
}

$env:GOCACHE = Join-Path $projectRoot ".cache\go-build"
$env:GOTMPDIR = Join-Path $projectRoot ".tmp\go"
$env:GOTELEMETRY = "off"
$env:CONTENT_AGENT_FFMPEG_COMMAND = $ffmpeg
$env:CONTENT_AGENT_FFPROBE_COMMAND = $ffprobe
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

Push-Location $backendRoot
try {
    $arguments = @("run", "-buildvcs=false", "./cmd/server", "-project-root", $projectRoot, "-listen", $Listen)
    if ($DemoExecution) {
        $arguments += @("-demo-execution", "-demo-runtime-url", "http://$Listen")
    }
    & $go @arguments
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
