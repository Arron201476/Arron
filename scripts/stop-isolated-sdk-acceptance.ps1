[CmdletBinding()]
param([Parameter(Mandatory)][string]$Directory)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$temporaryRoot = [IO.Path]::GetFullPath((Join-Path $root '.tmp')) + [IO.Path]::DirectorySeparatorChar
$directoryPath = [IO.Path]::GetFullPath($Directory)
if (-not $directoryPath.StartsWith($temporaryRoot, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $directoryPath) -notmatch '^sdk-acceptance-\d{8}-\d{6}$') { throw 'Not an isolated acceptance directory.' }
$state = Get-Content -LiteralPath (Join-Path $directoryPath 'processes.json') -Encoding utf8 | ConvertFrom-Json
if ($state.directory -ine $directoryPath -or $state.marker -cne (Split-Path -Leaf $directoryPath)) { throw 'Acceptance directory identity mismatch.' }
$targets = @(
    @{ id = $state.backend_pid; binary = (Join-Path $directoryPath 'backend.exe'); marker = $directoryPath },
    @{ id = $state.sidecar_pid; binary = (Join-Path $root '.tools\openai-agents-sidecar-venv\Scripts\python.exe'); marker = (Join-Path $directoryPath 'sidecar-server.py') },
    @{ id = $state.frontend_pid; binary = (Get-Command node -ErrorAction Stop).Source; marker = $state.marker }
)
$active = @()
foreach ($target in $targets) {
    $processID = [int]$target.id
    if ($processID -le 0) { throw 'Invalid acceptance process ID.' }
    $process = Get-CimInstance Win32_Process -Filter "ProcessId = $processID"
    if (-not $process) { continue }
    if ($process.ExecutablePath -ine $target.binary -or -not $process.CommandLine.Contains($target.marker)) { throw 'Process identity mismatch; nothing was stopped.' }
    $active += $process.ProcessId
}
foreach ($processID in $active) { Stop-Process -Id $processID -Force }
Write-Output "Stopped isolated acceptance processes. Data and logs retained in $directoryPath"
