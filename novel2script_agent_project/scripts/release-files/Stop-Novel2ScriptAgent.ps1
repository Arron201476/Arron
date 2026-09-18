$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$pidFile = Join-Path $root "Novel2ScriptAgent.pid"
if (-not (Test-Path -LiteralPath $pidFile)) { Write-Host "Novel2Script Agent is not running."; return }
$rawState = Get-Content -LiteralPath $pidFile -Raw
try {
    $state = $rawState | ConvertFrom-Json
    $processID = [int]$state.pid
    $address = [string]$state.address
    $shutdownToken = [string]$state.shutdown_token
} catch {
    $processID = [int]$rawState
    $address = "127.0.0.1:8832"
    $shutdownToken = ""
}
$process = Get-Process -Id $processID -ErrorAction SilentlyContinue
if ($null -eq $process) {
    Remove-Item -LiteralPath $pidFile -Force
    Write-Host "Novel2Script Agent is not running."
    return
}
if ($process.ProcessName -ne "Novel2ScriptAgent") { throw "PID file does not belong to Novel2Script Agent. Stop was refused." }
if ($shutdownToken) {
    try {
        Invoke-RestMethod -Method Post -Uri "http://$address/api/system/shutdown" -Headers @{ "X-N2S-Shutdown-Token" = $shutdownToken } -TimeoutSec 5 | Out-Null
    } catch {}
}
for ($attempt = 0; $attempt -lt 60 -and -not $process.HasExited; $attempt++) { Start-Sleep -Milliseconds 250 }
if (-not $process.HasExited) { Stop-Process -Id $processID -Force }
Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
Write-Host "Novel2Script Agent stopped."
