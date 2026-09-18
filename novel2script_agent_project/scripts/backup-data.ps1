param(
    [string]$DataDirectory = "",
    [string]$Destination = "",
    [string[]]$HealthUrls = @("http://127.0.0.1:8831/healthz", "http://127.0.0.1:8832/healthz")
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

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
            if ($health.ok) { throw "Novel2Script Agent is running. Stop the app before creating a backup." }
        } catch {
            if ($_.Exception.Message -like "Novel2Script Agent is running*") { throw }
        }
    }
}

function Assert-SQLiteFile([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 16) { throw "Invalid database file: $Path" }
    $header = [Text.Encoding]::ASCII.GetString($bytes, 0, 16)
    if ($header -ne "SQLite format 3`0") { throw "Invalid SQLite file: $Path" }
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
$data = Resolve-DataDirectory
$workspace = Join-Path $data "workspace.db"
$runtime = Join-Path $data "runtime.db"
if (-not (Test-Path -LiteralPath $workspace) -or -not (Test-Path -LiteralPath $runtime)) {
    throw "Incomplete data directory. workspace.db and runtime.db are both required: $data"
}
Assert-SQLiteFile $workspace
Assert-SQLiteFile $runtime

if (-not $Destination) {
    $backupRoot = Join-Path $root "backups"
    $Destination = Join-Path $backupRoot ("novel2script-backup-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".zip")
}
$destinationPath = [IO.Path]::GetFullPath($Destination)
New-Item -ItemType Directory -Force (Split-Path -Parent $destinationPath) | Out-Null
$staging = Join-Path ([IO.Path]::GetTempPath()) ("novel2script-backup-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $staging | Out-Null
try {
    foreach ($name in @("workspace.db", "workspace.db-wal", "workspace.db-shm", "runtime.db", "runtime.db-wal", "runtime.db-shm")) {
        $source = Join-Path $data $name
        if (Test-Path -LiteralPath $source) { Copy-Item -LiteralPath $source -Destination (Join-Path $staging $name) }
    }
    $manifest = [ordered]@{
        product = "Novel2Script Agent"
        format_version = 1
        created_at = (Get-Date).ToUniversalTime().ToString("o")
        files = @((Get-ChildItem -LiteralPath $staging -File).Name)
    }
    $manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $staging "manifest.json") -Encoding UTF8
    Compress-Archive -Path (Join-Path $staging "*") -DestinationPath $destinationPath -Force
} finally {
    Remove-SafeTemporaryDirectory $staging
}
Write-Host "Backup created: $destinationPath"
