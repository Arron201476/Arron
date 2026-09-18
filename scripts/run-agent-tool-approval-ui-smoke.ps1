[CmdletBinding()]
param([string]$BuildDirectory = '')

$ErrorActionPreference = "Stop"
$projectRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$frontendRoot = Join-Path $projectRoot "frontend"
. (Join-Path $PSScriptRoot 'agent-platform-release-evidence.ps1')
if ([string]::IsNullOrWhiteSpace($BuildDirectory)) { $BuildDirectory = Join-Path $frontendRoot 'dist' }
$BuildDirectory = Resolve-AgentReleaseChildPath -Root $projectRoot -Path $BuildDirectory
$vite = Join-Path $frontendRoot "node_modules\vite\bin\vite.js"
$smoke = Join-Path $frontendRoot "scripts\agent-tool-approval-ui-smoke.mjs"
$node = (Get-Command node -ErrorAction Stop).Source
$logRoot = Join-Path $projectRoot ".tmp\agent-tool-approval-ui"
$stdout = Join-Path $logRoot "preview.stdout.log"
$stderr = Join-Path $logRoot "preview.stderr.log"

function Get-FreeLoopbackPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}

function Wait-PreviewReady {
    param(
        [Parameter(Mandatory)][Diagnostics.Process]$Process,
        [Parameter(Mandatory)][string]$URL
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds(20)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $Process.Refresh()
        if ($Process.HasExited) {
            throw "Frontend preview exited before the visual smoke test"
        }
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $URL -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                return
            }
        }
        catch {
            Start-Sleep -Milliseconds 200
        }
    }
    throw "Frontend preview did not become ready"
}

if (-not (Test-Path -LiteralPath $vite -PathType Leaf)) {
    throw "Vite is not installed: $vite"
}
if (-not (Test-Path -LiteralPath (Join-Path $BuildDirectory 'index.html') -PathType Leaf)) {
    throw "Frontend production build is missing; run npm run build first"
}

New-Item -ItemType Directory -Force -Path $logRoot | Out-Null
$port = Get-FreeLoopbackPort
$url = "http://127.0.0.1:$port"
$preview = $null
$previousURL = [Environment]::GetEnvironmentVariable("CONTENT_AGENT_WEB_URL", "Process")

try {
    $preview = Start-Process -FilePath $node -ArgumentList @(
        ('"' + $vite + '"'), "preview", "--configLoader", "native", "--host", "127.0.0.1",
        "--port", [string]$port, "--strictPort", '--outDir', ('"' + $BuildDirectory + '"')
    ) -WorkingDirectory $frontendRoot -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    Wait-PreviewReady -Process $preview -URL $url
    [Environment]::SetEnvironmentVariable("CONTENT_AGENT_WEB_URL", $url, "Process")
    & $node $smoke
    if ($LASTEXITCODE -ne 0) {
        throw "Agent tool approval browser smoke failed with exit code $LASTEXITCODE"
    }
}
finally {
    [Environment]::SetEnvironmentVariable("CONTENT_AGENT_WEB_URL", $previousURL, "Process")
    if ($null -ne $preview) {
        $preview.Refresh()
        if (-not $preview.HasExited) {
            Stop-Process -Id $preview.Id -Force
            if (-not $preview.WaitForExit(5000)) { throw 'Frontend preview termination was not confirmed' }
        }
    }
}

Write-Output "AGENT_TOOL_APPROVAL_UI_SMOKE_PASSED"
