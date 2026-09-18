param(
    [Parameter(Mandatory = $true)][string]$Backup,
    [string]$DataDirectory = "",
    [switch]$ConfirmRestore,
    [string[]]$HealthUrls = @("http://127.0.0.1:8831/healthz", "http://127.0.0.1:8832/healthz")
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not $ConfirmRestore) { throw "Restore replaces current project data. Add -ConfirmRestore to continue." }

function Resolve-DataDirectory {
    if ($DataDirectory) { return [IO.Path]::GetFullPath($DataDirectory) }
    $packageData = Join-Path $root "data"
    if ((Test-Path (Join-Path $root "Novel2ScriptAgent.exe")) -or (Test-Path $packageData)) { return $packageData }
    return (Join-Path $root "runs")
}

function Assert-ServiceStopped {
    foreach ($url in $HealthUrls) {
        try {
            $health = Invoke-RestMethod $url -TimeoutSec 2
            if ($health.ok) { throw "Novel2Script Agent is running. Stop the app before restoring data." }
        } catch {
            if ($_.Exception.Message -like "Novel2Script Agent is running*") { throw }
        }
    }
}

function Assert-SQLiteFile([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 16 -or [Text.Encoding]::ASCII.GetString($bytes, 0, 16) -ne "SQLite format 3`0") {
        throw "Invalid database file in backup: $Path"
    }
}

function Remove-SafeTemporaryDirectory([string]$Path) {
    $resolved = [IO.Path]::GetFullPath($Path)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to clean a path outside the temporary directory: $resolved"
    }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
}

Assert-ServiceStopped
$backupPath = [IO.Path]::GetFullPath($Backup)
if (-not (Test-Path -LiteralPath $backupPath)) { throw "Backup file not found: $backupPath" }
$data = Resolve-DataDirectory
$staging = Join-Path ([IO.Path]::GetTempPath()) ("novel2script-restore-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $staging | Out-Null
try {
    Expand-Archive -LiteralPath $backupPath -DestinationPath $staging
    $manifestPath = Join-Path $staging "manifest.json"
    if (-not (Test-Path -LiteralPath $manifestPath)) { throw "Backup is missing manifest.json." }
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.product -ne "Novel2Script Agent" -or [int]$manifest.format_version -ne 1) { throw "Unsupported backup format." }
    foreach ($name in @("workspace.db", "runtime.db")) {
        $source = Join-Path $staging $name
        if (-not (Test-Path -LiteralPath $source)) { throw "Backup is missing $name." }
        Assert-SQLiteFile $source
    }

    $hasCurrentData = (Test-Path -LiteralPath (Join-Path $data "workspace.db")) -and (Test-Path -LiteralPath (Join-Path $data "runtime.db"))
    if ($hasCurrentData) {
        $rollback = Join-Path (Join-Path $root "backups") ("before-restore-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".zip")
        & (Join-Path $PSScriptRoot "backup-data.ps1") -DataDirectory $data -Destination $rollback -HealthUrls $HealthUrls
        Write-Host "Current data was backed up before restore: $rollback"
    }

    New-Item -ItemType Directory -Force $data | Out-Null
    foreach ($name in @("workspace.db", "workspace.db-wal", "workspace.db-shm", "runtime.db", "runtime.db-wal", "runtime.db-shm")) {
        $target = Join-Path $data $name
        if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Force }
        $source = Join-Path $staging $name
        if (Test-Path -LiteralPath $source) { Copy-Item -LiteralPath $source -Destination $target }
    }
} finally {
    Remove-SafeTemporaryDirectory $staging
}
Write-Host "Restore completed. Start Novel2Script Agent to load the restored projects."
