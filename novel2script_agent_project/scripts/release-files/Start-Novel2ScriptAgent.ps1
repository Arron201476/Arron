param([switch]$NoBrowser)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$envFile = Join-Path $root ".env"
if (-not (Test-Path -LiteralPath $envFile)) {
    Copy-Item -LiteralPath (Join-Path $root ".env.example") -Destination $envFile
    throw "First run setup: add the model API key to .env, then run this file again."
}

$address = if ($env:N2S_ADDR) { $env:N2S_ADDR } else { "127.0.0.1:8832" }
$baseUrl = "http://$address"
try {
    $health = Invoke-RestMethod "$baseUrl/healthz" -TimeoutSec 2
    if ($health.ok) {
        if (-not $NoBrowser) { Start-Process "$baseUrl/" }
        return
    }
} catch {}

$env:N2S_ADDR = $address
$env:N2S_WEB_DIR = Join-Path $root "web"
$env:N2S_TRACE_DIR = Join-Path $root "data"
$env:N2S_RUNTIME = "eino"
$env:N2S_SHUTDOWN_TOKEN = [guid]::NewGuid().ToString("N")
$executable = Join-Path $root "Novel2ScriptAgent.exe"
$process = Start-Process -FilePath $executable -WorkingDirectory $root -WindowStyle Hidden -PassThru
[ordered]@{ pid = $process.Id; address = $address; shutdown_token = $env:N2S_SHUTDOWN_TOKEN } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $root "Novel2ScriptAgent.pid") -Encoding ASCII

for ($attempt = 0; $attempt -lt 40; $attempt++) {
    Start-Sleep -Milliseconds 250
    if ($process.HasExited) { throw "Novel2Script Agent failed to start." }
    try {
        $health = Invoke-RestMethod "$baseUrl/healthz" -TimeoutSec 2
        if ($health.ok) {
            if (-not $NoBrowser) { Start-Process "$baseUrl/" }
            return
        }
    } catch {}
}
Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
throw "Novel2Script Agent startup timed out."
