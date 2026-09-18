[CmdletBinding()]
param(
    [int]$BackendPort = 8881,
    [int]$FrontendPort = 8880,
    [int]$SidecarPort = 8882,
    [string]$ReuseDirectory = ''
)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$ports = @($BackendPort, $FrontendPort, $SidecarPort)
if (($ports | Select-Object -Unique).Count -ne 3) { throw 'Acceptance ports must be distinct.' }
foreach ($port in $ports) {
    if ($port -lt 1024 -or $port -gt 65535) { throw 'Acceptance port is out of range.' }
    if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) { throw "Port $port is in use." }
}
$launch = Get-Date -Format yyyyMMdd-HHmmss
if ($ReuseDirectory) {
    $temporaryRoot = [IO.Path]::GetFullPath((Join-Path $root '.tmp')) + [IO.Path]::DirectorySeparatorChar
    $directory = [IO.Path]::GetFullPath($ReuseDirectory)
    $marker = Split-Path -Leaf $directory
    if (-not $directory.StartsWith($temporaryRoot, [StringComparison]::OrdinalIgnoreCase) -or $marker -notmatch '^sdk-acceptance-\d{8}-\d{6}$') { throw 'Not an isolated acceptance directory.' }
    $state = Get-Content -LiteralPath (Join-Path $directory 'processes.json') -Encoding utf8 | ConvertFrom-Json
    if ($state.directory -ine $directory -or $state.marker -cne $marker) { throw 'Acceptance directory identity mismatch.' }
    if ($state.backend_url -cne "http://127.0.0.1:$BackendPort" -or $state.frontend_url -cne "http://127.0.0.1:$FrontendPort" -or $state.sidecar_url -cne "http://127.0.0.1:$SidecarPort") { throw 'Restart ports must match the original acceptance environment.' }
    foreach ($processID in @($state.backend_pid, $state.sidecar_pid, $state.frontend_pid)) {
        if ([int]$processID -le 0 -or (Get-Process -Id ([int]$processID) -ErrorAction SilentlyContinue)) { throw 'Stop the isolated acceptance processes before reusing their data.' }
    }
    Copy-Item -LiteralPath (Join-Path $directory 'processes.json') -Destination (Join-Path $directory "processes-before-$launch.json")
} else {
    $marker = 'sdk-acceptance-' + $launch
    $directory = Join-Path $root ('.tmp\' + $marker)
    if (Test-Path -LiteralPath $directory) { throw 'Acceptance directory already exists.' }
    New-Item -ItemType Directory -Path $directory | Out-Null
}
$python = Join-Path $root '.tools\openai-agents-sidecar-venv\Scripts\python.exe'
$go = Join-Path $root '.tools\go-sdk\go\bin\go.exe'
$node = (Get-Command node -ErrorAction Stop).Source
$sidecarRoot = Join-Path $root 'experiments\openai-agents-sidecar'
$frontendRoot = Join-Path $root 'frontend'
$binary = Join-Path $directory 'backend.exe'
$bootstrap = Join-Path $directory 'sidecar-server.py'
$toolConfig = Join-Path $directory 'agent-tools.json'
$envFile = Join-Path $root 'backend\.env.local'
if (Test-Path -LiteralPath $envFile) {
    foreach ($line in Get-Content -LiteralPath $envFile -Encoding utf8) {
        if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
        $name, $value = $line -split '=', 2
        $name = $name.Trim()
        if ($name -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw 'Invalid local environment entry.' }
        [Environment]::SetEnvironmentVariable($name, $value.Trim(), 'Process')
    }
}
$env:GOCACHE = Join-Path $root '.tmp\go-cache-w9'
$env:GOTMPDIR = Join-Path $directory 'go-build'
New-Item -ItemType Directory -Path $env:GOTMPDIR -Force | Out-Null
Push-Location (Join-Path $root 'backend')
try {
    & $go build -buildvcs=false -o $binary ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'Acceptance backend build failed.' }
} finally { Pop-Location }

$config = [ordered]@{
    schema_version = '1.0.0'
    mcp_servers = @(@{
        id = 'story-fixture'; description = 'Local acceptance fixture'; transport = 'stdio'
        command = $python; args = @((Join-Path $sidecarRoot 'tests\fixtures\story_mcp_server.py'))
        cwd = $sidecarRoot; enabled = $true; defer_loading = $false
        environment = @{ STORY_MCP_STATE_FILE = 'TEST_STORY_MCP_STATE_FILE' }
        allowed_tools = @(
            @{ name = 'lookup_story_fact'; description = 'Read a synthetic story fact'; access = 'read'; approval = 'never' },
            @{ name = 'save_story_fact'; description = 'Save a synthetic story fact'; access = 'write'; approval = 'always' }
        )
    })
    hosted_tools = @()
    stdio_policy = @{ enabled = $true; allowed_commands = @($python); allowed_cwds = @($sidecarRoot) }
}
if (-not $ReuseDirectory) {
    $config | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $toolConfig -Encoding utf8NoBOM
} elseif (-not (Test-Path -LiteralPath $toolConfig)) {
    throw 'The original isolated tool configuration is missing.'
}
Copy-Item -LiteralPath (Join-Path $sidecarRoot 'scripts\serve.py') -Destination $bootstrap
$env:CONTENT_AGENT_TOOL_CONFIG_PATH = $toolConfig
$env:CONTENT_AGENT_AUTH_CONFIG_PATH = ''
$env:CONTENT_AGENT_OPENAI_SIDECAR_URL = "http://127.0.0.1:$SidecarPort"
$env:CONTENT_AGENT_BACKEND_BASE_URL = "http://127.0.0.1:$BackendPort"
$env:CONTENT_AGENT_BACKEND_URL = $env:CONTENT_AGENT_BACKEND_BASE_URL
$env:CONTENT_AGENT_SIDECAR_PORT = [string]$SidecarPort
$env:CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN = [Guid]::NewGuid().ToString('N') + [Guid]::NewGuid().ToString('N')
$env:CONTENT_AGENT_SDK_AGENT_ENABLED = 'true'
$env:CONTENT_AGENT_SDK_CANARY_WORKSPACES = ''
$env:CONTENT_AGENT_RELEASE_ID = $marker
$env:CONTENT_AGENT_SIDECAR_TASK_WORKER_ENABLED = 'true'
$env:CONTENT_AGENT_SIDECAR_SESSION_DB_PATH = Join-Path $directory 'sdk-sessions.db'
$env:TEST_STORY_MCP_STATE_FILE = Join-Path $directory 'mcp-writes.jsonl'
$env:OPENAI_AGENTS_TRACE_INCLUDE_SENSITIVE_DATA = '0'
$env:OPENAI_AGENTS_DONT_LOG_MODEL_DATA = '1'
$env:OPENAI_AGENTS_DONT_LOG_TOOL_DATA = '1'
$env:PYTHONUTF8 = '1'
$env:PYTHONPATH = Join-Path $sidecarRoot 'src'

$processes = @()
try {
    $sidecar = Start-Process -FilePath $python -ArgumentList @('"' + $bootstrap + '"') -WorkingDirectory $sidecarRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $directory "sidecar-$launch.out.log") -RedirectStandardError (Join-Path $directory "sidecar-$launch.err.log")
    $processes += $sidecar
    $backend = Start-Process -FilePath $binary -ArgumentList @('-project-root', ('"' + $root + '"'), '-database', ('"' + (Join-Path $directory 'data\content_agent.db') + '"'), '-listen', "127.0.0.1:$BackendPort") -WorkingDirectory $root -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $directory "backend-$launch.out.log") -RedirectStandardError (Join-Path $directory "backend-$launch.err.log")
    $processes += $backend
    $frontend = Start-Process -FilePath $node -ArgumentList @(('"' + (Join-Path $frontendRoot 'node_modules\vite\bin\vite.js') + '"'), '--configLoader', 'native', '--host', '127.0.0.1', '--port', [string]$FrontendPort, '--strictPort', '--mode', $marker) -WorkingDirectory $frontendRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $directory "frontend-$launch.out.log") -RedirectStandardError (Join-Path $directory "frontend-$launch.err.log")
    $processes += $frontend
    $healthy = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if ($processes.Where({ $_.HasExited }).Count -gt 0) { throw 'An isolated service exited before readiness.' }
        try {
            $sidecarHealth = Invoke-RestMethod -Uri "http://127.0.0.1:$SidecarPort/healthz" -TimeoutSec 2
            $backendHealth = Invoke-RestMethod -Uri "http://127.0.0.1:$BackendPort/healthz" -TimeoutSec 2
            $frontendHealth = Invoke-WebRequest -Uri "http://127.0.0.1:$FrontendPort/" -TimeoutSec 2 -UseBasicParsing
            if ($sidecarHealth.status -eq 'ok' -and $sidecarHealth.write_tools_enabled -and $backendHealth.status -eq 'ok' -and $frontendHealth.StatusCode -eq 200) { $healthy = $true; break }
        } catch { Start-Sleep -Milliseconds 500 }
    }
    if (-not $healthy) { throw 'Isolated services did not become ready.' }
    [ordered]@{
        directory = $directory; marker = $marker; backend_pid = $backend.Id; sidecar_pid = $sidecar.Id; frontend_pid = $frontend.Id
        backend_binary = $binary; sidecar_binary = $python; sidecar_script = $bootstrap; frontend_binary = $node
        backend_url = "http://127.0.0.1:$BackendPort"; frontend_url = "http://127.0.0.1:$FrontendPort"; sidecar_url = "http://127.0.0.1:$SidecarPort"
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $directory 'processes.json') -Encoding utf8NoBOM
    Write-Output "Isolated acceptance directory: $directory"
    Write-Output "Isolated acceptance UI: http://127.0.0.1:$FrontendPort/"
} catch {
    foreach ($process in $processes) {
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
    }
    throw
}
