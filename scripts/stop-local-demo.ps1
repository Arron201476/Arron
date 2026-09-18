$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $projectRoot ".tmp\local-demo-processes.json"

if (-not (Test-Path -LiteralPath $pidFile)) {
    Write-Output "No managed local demo is running."
    exit 0
}

$managed = Get-Content -LiteralPath $pidFile -Encoding utf8 | ConvertFrom-Json
foreach ($processID in @($managed.frontend_pid, $managed.backend_pid, $managed.sidecar_pid)) {
    if (-not $processID) { continue }
    $process = Get-Process -Id $processID -ErrorAction SilentlyContinue
    if ($process) {
        Stop-Process -Id $processID
        $process.WaitForExit(5000)
    }
}
Remove-Item -LiteralPath $pidFile
Write-Output "Managed local demo stopped."
