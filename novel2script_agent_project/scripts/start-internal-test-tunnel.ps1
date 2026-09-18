param(
    [string]$TunnelName = "novel2script-demo",
    [string]$Address = "127.0.0.1:8842",
    [string]$PublicOrigin = "https://demo.novel2script.click",
    [switch]$NoBrowser
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$package = Join-Path $root "release\Novel2ScriptAgent"
$cloudflared = Join-Path $root ".tools\cloudflared\cloudflared.exe"
$tunnelStateFile = Join-Path $package "CloudflareTunnel.pid"
$tunnelLog = Join-Path $package "data\cloudflared.log"

if (-not (Test-Path -LiteralPath (Join-Path $package "Novel2ScriptAgent.exe"))) {
    throw "Internal test package is missing. Run scripts/package.ps1 first."
}
if (-not (Test-Path -LiteralPath $cloudflared)) {
    throw "cloudflared.exe is missing from .tools/cloudflared/."
}

$packageEnv = Join-Path $package ".env"
if (-not (Test-Path -LiteralPath $packageEnv)) {
    $developmentEnv = Join-Path $root "backend\.env"
    if (-not (Test-Path -LiteralPath $developmentEnv)) {
        throw "Model configuration is missing. Configure backend/.env first."
    }
    Copy-Item -LiteralPath $developmentEnv -Destination $packageEnv
}

if (Test-Path -LiteralPath $tunnelStateFile) {
    try {
        $existingState = Get-Content -LiteralPath $tunnelStateFile -Raw | ConvertFrom-Json
        $existingProcess = Get-Process -Id ([int]$existingState.pid) -ErrorAction SilentlyContinue
        if ($null -ne $existingProcess -and $existingProcess.ProcessName -eq "cloudflared") {
            throw "The internal test tunnel is already running (PID $($existingProcess.Id))."
        }
    } catch {
        if ($_.Exception.Message -like "The internal test tunnel is already running*") { throw }
    }
    Remove-Item -LiteralPath $tunnelStateFile -Force -ErrorAction SilentlyContinue
}

$oldAddress = $env:N2S_ADDR
$oldAllowedOrigins = $env:N2S_ALLOWED_ORIGINS
try {
    $env:N2S_ADDR = $Address
    $env:N2S_ALLOWED_ORIGINS = $PublicOrigin
    & (Join-Path $package "Start-Novel2ScriptAgent.ps1") -NoBrowser
} finally {
    $env:N2S_ADDR = $oldAddress
    $env:N2S_ALLOWED_ORIGINS = $oldAllowedOrigins
}

$healthUrl = "http://$Address/healthz"
$health = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 5
if (-not $health.ok) { throw "Internal test service health check failed: $healthUrl" }

$arguments = @(
    "tunnel",
    "--logfile", $tunnelLog,
    "run",
    "--url", "http://$Address",
    $TunnelName
)
$tunnelProcess = Start-Process -FilePath $cloudflared -ArgumentList $arguments -WorkingDirectory $package -WindowStyle Hidden -PassThru
[ordered]@{
    pid = $tunnelProcess.Id
    tunnel_name = $TunnelName
    origin = "http://$Address"
    started_at = (Get-Date).ToString("o")
} | ConvertTo-Json | Set-Content -LiteralPath $tunnelStateFile -Encoding ASCII

Start-Sleep -Seconds 3
if ($tunnelProcess.HasExited) {
    Remove-Item -LiteralPath $tunnelStateFile -Force -ErrorAction SilentlyContinue
    throw "Cloudflare Tunnel failed to start. Check $tunnelLog"
}

if (-not $NoBrowser) { Start-Process "http://$Address/" }
Write-Host "Internal test service: http://$Address/"
Write-Host "Cloudflare Tunnel: $TunnelName (PID $($tunnelProcess.Id))"
Write-Host "Tunnel log: $tunnelLog"
