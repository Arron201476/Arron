$ErrorActionPreference = "Continue"
if (Test-Path variable:PSNativeCommandUseErrorActionPreference) {
  $PSNativeCommandUseErrorActionPreference = $false
}

$ProjectRoot = "C:\Users\egois\Desktop\novel2script_agent_project"
$BackendRoot = Join-Path $ProjectRoot "backend"

$env:N2S_TRACE_DIR = Join-Path $ProjectRoot "runs"
$env:GOTMPDIR = Join-Path $BackendRoot ".tmp\gorun"
$env:GOCACHE = Join-Path $BackendRoot ".tmp\gocache"
$env:APPDATA = Join-Path $BackendRoot ".tmp\appdata"
$env:GOTELEMETRY = "off"

# Codex can provide both Path and PATH. Windows PowerShell Start-Process rejects
# that duplicate environment block, so normalize it before spawning Go.
$ProcessEnvironment = [System.Environment]::GetEnvironmentVariables()
$ProcessPath = $ProcessEnvironment["Path"]
[System.Environment]::SetEnvironmentVariable("PATH", $null, [System.EnvironmentVariableTarget]::Process)
[System.Environment]::SetEnvironmentVariable("Path", $ProcessPath, [System.EnvironmentVariableTarget]::Process)

New-Item -ItemType Directory -Force $env:N2S_TRACE_DIR | Out-Null
New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null
New-Item -ItemType Directory -Force $env:GOCACHE | Out-Null
New-Item -ItemType Directory -Force $env:APPDATA | Out-Null

Set-Location $BackendRoot
$LogBase = Join-Path $env:N2S_TRACE_DIR ("server-" + $PID)
$GoProcess = Start-Process -FilePath (Join-Path $ProjectRoot ".tools\go\bin\go.exe") -ArgumentList @("run", "-buildvcs=false", ".\cmd\server") -WorkingDirectory $BackendRoot -WindowStyle Hidden -RedirectStandardOutput ($LogBase + ".stdout.log") -RedirectStandardError ($LogBase + ".stderr.log") -PassThru
$GoProcess.WaitForExit()
exit $GoProcess.ExitCode
