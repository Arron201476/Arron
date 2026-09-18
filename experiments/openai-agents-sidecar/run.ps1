param(
    [string]$BackendRoot = (Resolve-Path "$PSScriptRoot\..\..\backend").Path,
    [int]$Port = 8871
)

$envFile = Join-Path $BackendRoot ".env.local"
if (Test-Path -LiteralPath $envFile) {
    foreach ($line in Get-Content -LiteralPath $envFile) {
        if ($line -match '^\s*([^#][^=]*)=(.*)$') {
            [Environment]::SetEnvironmentVariable($matches[1].Trim(), $matches[2].Trim(), "Process")
        }
    }
}

$env:CONTENT_AGENT_SIDECAR_PORT = "$Port"
$existingPythonPath = $env:PYTHONPATH
$env:PYTHONPATH = if ($existingPythonPath) {
    "$PSScriptRoot\src;$existingPythonPath"
} else {
    "$PSScriptRoot\src"
}
python "$PSScriptRoot\scripts\serve.py"
