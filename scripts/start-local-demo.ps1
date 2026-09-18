param(
    [string]$BackendListen = "127.0.0.1:8850",
    [int]$FrontendPort = 8860,
    [int]$SidecarPort = 8871
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$backendRoot = Join-Path $projectRoot "backend"
$frontendRoot = Join-Path $projectRoot "frontend"
$sidecarRoot = Join-Path $projectRoot "experiments\openai-agents-sidecar"
$go = Join-Path $projectRoot ".tools\go-sdk\go\bin\go.exe"
$binary = Join-Path $projectRoot ".tmp\content-agent-server-demo.exe"
$pidFile = Join-Path $projectRoot ".tmp\local-demo-processes.json"
$logRoot = Join-Path $projectRoot "logs"
$node = (Get-Command node -ErrorAction Stop).Source
$python = Join-Path $projectRoot ".tools\openai-agents-sidecar-venv\Scripts\python.exe"
if (-not (Test-Path -LiteralPath $python)) {
    throw "Pinned Sidecar Python runtime is missing: $python"
}
$envFile = Join-Path $backendRoot ".env.local"

if (Test-Path -LiteralPath $envFile) {
    foreach ($line in Get-Content -LiteralPath $envFile -Encoding utf8) {
        if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
        $name, $value = $line -split '=', 2
        $name = $name.Trim()
        if ($name -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
            throw "Invalid environment variable name in .env.local: $name"
        }
        [Environment]::SetEnvironmentVariable($name, $value.Trim(), "Process")
    }
}

if (Test-Path -LiteralPath $pidFile) {
    throw "Local demo PID file already exists. Run scripts\stop-local-demo.ps1 first."
}

$backendPort = [int]($BackendListen -split ':')[-1]
foreach ($port in @($backendPort, $FrontendPort, $SidecarPort)) {
    if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) {
        throw "Port $port is already in use. Stop the existing local service before starting the demo."
    }
}

New-Item -ItemType Directory -Force -Path (Split-Path $binary), $logRoot | Out-Null
$env:GOCACHE = Join-Path $projectRoot ".cache\go-build"
$env:GOTMPDIR = Join-Path $projectRoot ".tmp\go"
$env:GOTELEMETRY = "off"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

Push-Location $backendRoot
try {
    & $go build -buildvcs=false -o $binary ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw "Backend build failed." }
}
finally {
    Pop-Location
}

$vite = Join-Path $frontendRoot "node_modules\vite\bin\vite.js"
$env:CONTENT_AGENT_OPENAI_SIDECAR_URL = "http://127.0.0.1:$SidecarPort"
$env:CONTENT_AGENT_SDK_AGENT_ENABLED = "true"
$env:CONTENT_AGENT_SDK_CANARY_WORKSPACES = ""
$env:CONTENT_AGENT_RELEASE_ID = "local-demo"
$env:OPENAI_AGENTS_TRACE_INCLUDE_SENSITIVE_DATA = "0"
$env:OPENAI_AGENTS_DONT_LOG_MODEL_DATA = "1"
$env:OPENAI_AGENTS_DONT_LOG_TOOL_DATA = "1"
$env:CONTENT_AGENT_BACKEND_BASE_URL = "http://$BackendListen"
$env:CONTENT_AGENT_BACKEND_URL = "http://$BackendListen"
$env:CONTENT_AGENT_SIDECAR_PORT = [string]$SidecarPort
$env:CONTENT_AGENT_SIDECAR_TASK_WORKER_ENABLED = "true"
$env:CONTENT_AGENT_SIDECAR_SESSION_DB_PATH = Join-Path $projectRoot ".tmp\openai-agents-main-sessions.db"
$env:PYTHONPATH = Join-Path $sidecarRoot "src"
$sidecarProcess = $null
$backendProcess = $null
$frontendProcess = $null
try {
    $sidecarStart = @{
        FilePath = $python
        ArgumentList = @("scripts\serve.py")
        WorkingDirectory = $sidecarRoot
        RedirectStandardOutput = (Join-Path $logRoot "sidecar-demo.out.log")
        RedirectStandardError = (Join-Path $logRoot "sidecar-demo.err.log")
        WindowStyle = "Hidden"
        PassThru = $true
    }
    $sidecarProcess = Start-Process @sidecarStart

    $backendStart = @{
        FilePath = $binary
        ArgumentList = @(
            "-project-root", $projectRoot,
            "-listen", $BackendListen
        )
        WorkingDirectory = $projectRoot
        RedirectStandardOutput = (Join-Path $logRoot "backend-demo.out.log")
        RedirectStandardError = (Join-Path $logRoot "backend-demo.err.log")
        WindowStyle = "Hidden"
        PassThru = $true
    }
    $backendProcess = Start-Process @backendStart

    $frontendStart = @{
        FilePath = $node
        ArgumentList = @(
            $vite,
            "--configLoader", "native",
            "--host", "127.0.0.1",
            "--port", [string]$FrontendPort
        )
        WorkingDirectory = $frontendRoot
        RedirectStandardOutput = (Join-Path $logRoot "frontend-demo.out.log")
        RedirectStandardError = (Join-Path $logRoot "frontend-demo.err.log")
        WindowStyle = "Hidden"
        PassThru = $true
    }
    $frontendProcess = Start-Process @frontendStart

    $managed = [ordered]@{
        sidecar_pid = $sidecarProcess.Id
        backend_pid = $backendProcess.Id
        frontend_pid = $frontendProcess.Id
        backend_binary = $binary
        started_at = (Get-Date).ToUniversalTime().ToString("o")
    }
    $managed | ConvertTo-Json | Set-Content -LiteralPath $pidFile -Encoding utf8
    $managed = Get-Content -LiteralPath $pidFile -Encoding utf8 | ConvertFrom-Json
}
catch {
    if ($frontendProcess -and -not $frontendProcess.HasExited) {
        Stop-Process -Id $frontendProcess.Id -Force -ErrorAction SilentlyContinue
    }
    if ($backendProcess -and -not $backendProcess.HasExited) {
        Stop-Process -Id $backendProcess.Id -Force -ErrorAction SilentlyContinue
    }
    if ($sidecarProcess -and -not $sidecarProcess.HasExited) {
        Stop-Process -Id $sidecarProcess.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
    throw
}

$backendHealthy = $false
$frontendHealthy = $false
$sidecarHealthy = $false
for ($attempt = 0; $attempt -lt 30; $attempt += 1) {
    if (-not $sidecarHealthy) {
        try {
            $response = Invoke-RestMethod -Uri "http://127.0.0.1:$SidecarPort/healthz" -TimeoutSec 2
            if ($response.status -eq "ok" -and $response.write_tools_enabled) { $sidecarHealthy = $true }
        }
        catch {}
    }
    if (-not $backendHealthy) {
        try {
            $response = Invoke-RestMethod -Uri "http://$BackendListen/healthz" -TimeoutSec 2
            if ($response.status -eq "ok") { $backendHealthy = $true }
        }
        catch {}
    }
    if (-not $frontendHealthy) {
        try {
            $response = Invoke-WebRequest -Uri "http://127.0.0.1:$FrontendPort/" -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200) { $frontendHealthy = $true }
        }
        catch {}
    }
    if ($sidecarHealthy -and $backendHealthy -and $frontendHealthy) { break }
    Start-Sleep -Milliseconds 500
}
if (-not $sidecarHealthy -or -not $backendHealthy -or -not $frontendHealthy) {
    if ($frontendProcess -and -not $frontendProcess.HasExited) {
        Stop-Process -Id $frontendProcess.Id -Force -ErrorAction SilentlyContinue
    }
    if ($backendProcess -and -not $backendProcess.HasExited) {
        Stop-Process -Id $backendProcess.Id -Force -ErrorAction SilentlyContinue
    }
    if ($sidecarProcess -and -not $sidecarProcess.HasExited) {
        Stop-Process -Id $sidecarProcess.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
    throw "Local demo did not become healthy. Inspect logs\sidecar-demo.err.log, logs\backend-demo.err.log, and logs\frontend-demo.err.log."
}

Write-Output "Content Agent demo is ready: http://127.0.0.1:$FrontendPort/"
Write-Output "Sidecar PID $($managed.sidecar_pid), backend PID $($managed.backend_pid), frontend PID $($managed.frontend_pid)."
