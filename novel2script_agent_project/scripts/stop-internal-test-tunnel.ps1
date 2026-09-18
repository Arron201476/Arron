$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$package = Join-Path $root "release\Novel2ScriptAgent"
$tunnelStateFile = Join-Path $package "CloudflareTunnel.pid"

if (Test-Path -LiteralPath $tunnelStateFile) {
    $state = Get-Content -LiteralPath $tunnelStateFile -Raw | ConvertFrom-Json
    $processID = [int]$state.pid
    $process = Get-Process -Id $processID -ErrorAction SilentlyContinue
    if ($null -ne $process) {
        if ($process.ProcessName -ne "cloudflared") {
            throw "Tunnel PID file does not belong to cloudflared. Stop was refused."
        }
        Stop-Process -Id $processID -Force
    }
    Remove-Item -LiteralPath $tunnelStateFile -Force -ErrorAction SilentlyContinue
}

$stopApplication = Join-Path $package "Stop-Novel2ScriptAgent.ps1"
if (Test-Path -LiteralPath $stopApplication) { & $stopApplication }
Write-Host "Internal test service and Cloudflare Tunnel stopped."
