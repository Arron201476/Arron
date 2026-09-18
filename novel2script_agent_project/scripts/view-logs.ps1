param(
    [string]$DataDirectory = "",
    [ValidateRange(1, 500)][int]$Limit = 50
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not $DataDirectory) {
    $packageData = Join-Path $root "data"
    $DataDirectory = if ((Test-Path (Join-Path $root "Novel2ScriptAgent.exe")) -or (Test-Path $packageData)) { $packageData } else { Join-Path $root "runs" }
}
$trace = Join-Path ([IO.Path]::GetFullPath($DataDirectory)) "llm_trace.jsonl"
if (-not (Test-Path -LiteralPath $trace)) { throw "Model trace log not found: $trace" }

$rows = foreach ($line in Get-Content -LiteralPath $trace -Tail $Limit -Encoding UTF8) {
    try {
        $entry = $line | ConvertFrom-Json
        [pscustomobject]@{
            Time = $entry.timestamp
            DurationMs = $entry.duration_ms
            Model = $entry.model
            Component = $entry.trace_context.component
            Operation = $entry.trace_context.operation
            Project = $entry.trace_context.project_id
            Run = $entry.trace_context.run_id
            Artifact = $entry.trace_context.artifact_type
            Episode = $entry.trace_context.episode_id
            ErrorStatus = $entry.error.status_code
        }
    } catch {
        [pscustomobject]@{ Time = "invalid"; DurationMs = 0; Model = ""; Component = ""; Operation = ""; Project = ""; Run = ""; Artifact = ""; Episode = ""; ErrorStatus = "invalid log line" }
    }
}
$rows | Format-Table -AutoSize
