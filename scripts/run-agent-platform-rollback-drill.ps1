[CmdletBinding()]
param(
    [string]$BaselineRef = "pre-agent-convergence-20260904",
    [string]$ReportPath = "",
    [switch]$KeepWorkDirectory
)

$ErrorActionPreference = "Stop"
$projectRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
. (Join-Path $PSScriptRoot 'agent-platform-release-evidence.ps1')
$go = Join-Path $projectRoot ".tools\go-sdk\go\bin\go.exe"
$python = Join-Path $projectRoot ".tools\openai-agents-sidecar-venv\Scripts\python.exe"
$runID = [DateTimeOffset]::UtcNow.ToString("yyyyMMddTHHmmssZ") + "-" + [Guid]::NewGuid().ToString("N").Substring(0, 8)
$workRoot = Join-Path $projectRoot ".tmp\agent-platform-rollback-drill\$runID"
$baselineRoot = Join-Path $workRoot "baseline"
$binRoot = Join-Path $workRoot "bin"
$dataRoot = Join-Path $workRoot "data"
$restoredDataRoot = Join-Path $workRoot 'restored-data'
$businessManifest = Join-Path $workRoot 'business-manifest.json'
$businessHelper = Join-Path $projectRoot 'scripts/agent_platform_rollback_data.py'
$logRoot = Join-Path $workRoot "logs"
$baselineExecutable = Join-Path $binRoot "content-agent-baseline.exe"
$currentExecutable = Join-Path $binRoot "content-agent-current.exe"
$databasePath = Join-Path $dataRoot "content_agent.db"
$restoredDatabasePath = Join-Path $restoredDataRoot 'content_agent.db'

if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    $ReportPath = Join-Path $projectRoot "docs\evidence\agent-platform-rollback-drill-local.json"
}
elseif (-not [IO.Path]::IsPathRooted($ReportPath)) {
    $ReportPath = Join-Path $projectRoot $ReportPath
}
$ReportPath = [IO.Path]::GetFullPath($ReportPath)

function Assert-WorkspaceChildPath {
    param([Parameter(Mandatory)][string]$Path)
    return Resolve-AgentReleaseChildPath -Root $projectRoot -Path $Path
}

function Get-RuntimeSchemaVersion {
    param([Parameter(Mandatory)][string]$SourceRoot)
    $storePath = Join-Path $SourceRoot "backend\internal\runtime\store.go"
    $match = Select-String -LiteralPath $storePath -Pattern '^\s*const schemaVersion = (?<version>\d+)\s*$' |
        Select-Object -First 1
    if ($null -eq $match) {
        throw "Runtime schema version not found: $storePath"
    }
    return [int]$match.Matches[0].Groups["version"].Value
}

function Get-DatabaseSchemaVersion {
    param([Parameter(Mandatory)][string]$Database)
    $probe = @(& $python -c @"
import sqlite3
import sys
from pathlib import Path

uri = Path(sys.argv[1]).resolve().as_uri() + "?mode=ro"
connection = sqlite3.connect(uri, uri=True)
try:
    print(connection.execute("PRAGMA user_version").fetchone()[0])
finally:
    connection.close()
"@ $Database)
    if ($LASTEXITCODE -ne 0 -or $probe.Count -eq 0) {
        throw "Unable to inspect SQLite schema version: $Database"
    }
    return [int]$probe[-1]
}

function Invoke-RollbackBusinessCheck {
    param(
        [Parameter(Mandatory)][string]$Operation,
        [string]$Database = $databasePath,
        [int]$Port = 0
    )
    $report.business_phase = $Operation
    Write-AgentReleaseEvidence -Root $projectRoot -Path $ReportPath -Report $report
    $arguments = @($businessHelper, $Operation, '--work-root', $workRoot,
        '--database', $Database, '--manifest', $businessManifest)
    if ($Operation -in @('select-backup', 'restore')) { $arguments += @('--target-version', [string]$targetSchemaVersion) }
    if ($Operation -eq 'restore') { $arguments += @('--destination', $restoredDatabasePath) }
    if ($Operation -eq 'verify-http') { $arguments += @('--port', [string]$Port) }
    $output = @(& $python @arguments)
    if ($LASTEXITCODE -ne 0 -or $output.Count -ne 1) { throw "Rollback business check failed: $Operation" }
    return ($output[0] | ConvertFrom-Json)
}

function Get-FreeLoopbackPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        return ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}

function Start-DrillServer {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string]$ContentRoot,
        [Parameter(Mandatory)][string]$Database,
        [Parameter(Mandatory)][int]$Port,
        [Parameter(Mandatory)][string]$Name
    )
    if ($Port -lt 1 -or $Port -gt 65535 -or $Port -in @(8860, 8880)) { throw 'Drill server requires an isolated loopback port' }
    $stdout = Join-Path $logRoot "$Name.stdout.log"
    $stderr = Join-Path $logRoot "$Name.stderr.log"
    $arguments = @(
        "-listen", "127.0.0.1:$Port",
        "-project-root", ('"' + $ContentRoot + '"'),
        "-database", ('"' + $Database + '"'),
        "-retention-interval", "1h"
    )
    $startParameters = @{
        FilePath = $Executable
        ArgumentList = $arguments
        PassThru = $true
        WindowStyle = "Hidden"
        RedirectStandardOutput = $stdout
        RedirectStandardError = $stderr
    }
    return Start-Process @startParameters
}

function Wait-Healthy {
    param(
        [Parameter(Mandatory)][Diagnostics.Process]$Process,
        [Parameter(Mandatory)][int]$Port,
        [int]$TimeoutSeconds = 20
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        if ($Process.HasExited) {
            throw "Server exited before health check; exit_code=$($Process.ExitCode)"
        }
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$Port/healthz" -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                return
            }
        }
        catch {
            Start-Sleep -Milliseconds 200
        }
    }
    throw "Server did not become healthy within $TimeoutSeconds seconds"
}

function Stop-DrillProcess {
    param([Diagnostics.Process]$Process)
    if ($null -eq $Process) {
        return
    }
    $Process.Refresh()
    if (-not $Process.HasExited) {
        Stop-Process -Id $Process.Id -Force
        if (-not $Process.WaitForExit(5000)) { throw 'Drill server termination was not confirmed' }
    }
}

function Build-Server {
    param(
        [Parameter(Mandatory)][string]$SourceRoot,
        [Parameter(Mandatory)][string]$Output
    )
    Push-Location (Join-Path $SourceRoot "backend")
    try {
        & $go build -trimpath -o $Output ./cmd/server
        if ($LASTEXITCODE -ne 0) {
            throw "Go server build failed for $SourceRoot"
        }
    }
    finally {
        Pop-Location
    }
}

Assert-WorkspaceChildPath -Path $workRoot | Out-Null
Assert-WorkspaceChildPath -Path $ReportPath | Out-Null
$report = [ordered]@{
    schema_version = 'agent_platform_rollback_drill.v2'
    run_id = $runID
    status = 'running'
    started_at = [DateTimeOffset]::UtcNow.ToString('o')
    local_only = $true
    validation_scope = 'baseline_business_database_assets_and_read_api'
    business_data_validation = 'not_run'
    rollback_ready = $false
    cleanup = 'pending'
    remote_operations = @()
}
Write-AgentReleaseEvidence -Root $projectRoot -Path $ReportPath -Report $report

$environmentOverrides = @{
    CONTENT_AGENT_OPENAI_SIDECAR_URL             = "http://127.0.0.1:9"
    CONTENT_AGENT_OPENAI_SIDECAR_TIMEOUT_SECONDS = "5"
    CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN         = "rollback-drill-token"
    CONTENT_AGENT_SDK_AGENT_ENABLED              = "true"
    CONTENT_AGENT_SDK_CANARY_WORKSPACES          = ""
    CONTENT_AGENT_RELEASE_ID                     = "rollback-drill"
    CONTENT_AGENT_AUTH_CONFIG_PATH               = ""
    CONTENT_AGENT_TOOL_CONFIG_PATH               = ""
    CONTENT_AGENT_SCRIPT_SANDBOX_ADAPTER         = ""
    CONTENT_AGENT_SCRIPT_OCI_COMMAND             = ""
    CONTENT_AGENT_SCRIPT_PYTHON_IMAGE            = ""
    CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED        = "false"
    GOTMPDIR                                    = Join-Path $workRoot 'gotmp'
    GOCACHE                                     = Join-Path $projectRoot '.tmp/go-cache-w9'
    PYTHONUTF8                                  = '1'
}
$previousEnvironment = @{}
foreach ($name in $environmentOverrides.Keys) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$processes = [Collections.Generic.List[Diagnostics.Process]]::new()
$completed = $false
$failure = $null

try {
    if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw "Bundled Go toolchain not found: $go" }
    if (-not (Test-Path -LiteralPath $python -PathType Leaf)) { throw "Bundled Python environment not found: $python" }
    if (-not (Test-Path -LiteralPath $businessHelper -PathType Leaf)) { throw 'Rollback business-data helper is missing' }
    $targetSchemaVersion = Get-RuntimeSchemaVersion -SourceRoot $projectRoot
    $report.source_before = Get-AgentReleaseSourceSnapshot -Root $projectRoot
    Write-AgentReleaseEvidence -Root $projectRoot -Path $ReportPath -Report $report
    New-Item -ItemType Directory -Force -Path $baselineRoot, $binRoot, $dataRoot, $logRoot | Out-Null
    foreach ($name in $environmentOverrides.Keys) {
        [Environment]::SetEnvironmentVariable($name, $environmentOverrides[$name], "Process")
    }

    $baselineOutput = @(& git -C $projectRoot rev-parse "$BaselineRef^{commit}")
    if ($LASTEXITCODE -ne 0 -or $baselineOutput.Count -ne 1 -or [string]::IsNullOrWhiteSpace($baselineOutput[0])) {
        throw "Baseline ref cannot be resolved: $BaselineRef"
    }
    $baselineCommit = $baselineOutput[0].Trim()
    $temporaryIndex = Join-Path $workRoot "baseline.index"
    $previousIndex = [Environment]::GetEnvironmentVariable("GIT_INDEX_FILE", "Process")
    try {
        [Environment]::SetEnvironmentVariable("GIT_INDEX_FILE", $temporaryIndex, "Process")
        & git -C $projectRoot read-tree $baselineCommit
        if ($LASTEXITCODE -ne 0) {
            throw "Unable to read baseline tree $BaselineRef"
        }
        $checkoutPrefix = $baselineRoot.Replace('\', '/') + "/"
        & git -C $projectRoot checkout-index --all --force "--prefix=$checkoutPrefix"
        if ($LASTEXITCODE -ne 0) {
            throw "Unable to materialize baseline tree $BaselineRef"
        }
    }
    finally {
        [Environment]::SetEnvironmentVariable("GIT_INDEX_FILE", $previousIndex, "Process")
    }
    $baselineSchemaVersion = Get-RuntimeSchemaVersion -SourceRoot $baselineRoot

    New-Item -ItemType Directory -Force -Path $env:GOTMPDIR, $env:GOCACHE | Out-Null
    Build-Server -SourceRoot $baselineRoot -Output $baselineExecutable
    Build-Server -SourceRoot $projectRoot -Output $currentExecutable

    $baselinePort = Get-FreeLoopbackPort
    $baseline = Start-DrillServer -Executable $baselineExecutable -ContentRoot $baselineRoot -Database $databasePath -Port $baselinePort -Name "baseline-create"
    $processes.Add($baseline)
    Wait-Healthy -Process $baseline -Port $baselinePort
    Stop-DrillProcess $baseline
    $createdBaselineSchemaVersion = Get-DatabaseSchemaVersion -Database $databasePath
    if ($createdBaselineSchemaVersion -ne $baselineSchemaVersion) {
        throw "Baseline database schema is $createdBaselineSchemaVersion, expected $baselineSchemaVersion"
    }
    $report.business_data_validation = 'running'
    Write-AgentReleaseEvidence -Root $projectRoot -Path $ReportPath -Report $report
    $report.fixture_seed = Invoke-RollbackBusinessCheck -Operation 'seed'
    $report.fixture_manifest_sha256 = (Get-FileHash -LiteralPath $businessManifest -Algorithm SHA256).Hash
    $baselineReadPort = Get-FreeLoopbackPort
    $baselineRead = Start-DrillServer -Executable $baselineExecutable -ContentRoot $baselineRoot -Database $databasePath -Port $baselineReadPort -Name 'baseline-business-data'
    $processes.Add($baselineRead)
    Wait-Healthy -Process $baselineRead -Port $baselineReadPort
    $report.baseline_business_api = Invoke-RollbackBusinessCheck -Operation 'verify-http' -Port $baselineReadPort
    Stop-DrillProcess $baselineRead
    $report.baseline_business_snapshot = Invoke-RollbackBusinessCheck -Operation 'verify-exact'
    $preMigrationHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $databasePath).Hash

    $currentPort = Get-FreeLoopbackPort
    $current = Start-DrillServer -Executable $currentExecutable -ContentRoot $baselineRoot -Database $databasePath -Port $currentPort -Name "current-migrate"
    $processes.Add($current)
    Wait-Healthy -Process $current -Port $currentPort
    Stop-DrillProcess $current
    $migratedSchemaVersion = Get-DatabaseSchemaVersion -Database $databasePath
    if ($migratedSchemaVersion -ne $targetSchemaVersion) {
        throw "Migrated database schema is $migratedSchemaVersion, expected $targetSchemaVersion"
    }
    $migratedHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $databasePath).Hash
    $report.migrated_business_data = Invoke-RollbackBusinessCheck -Operation 'verify-migrated'

    $migrationBackup = Invoke-RollbackBusinessCheck -Operation 'select-backup'
    $backupPath = Assert-WorkspaceChildPath -Path $migrationBackup.backup_ref
    $backupHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $backupPath).Hash
    $report.migration_backup = $migrationBackup

    $rejectionPort = Get-FreeLoopbackPort
    $rejection = Start-DrillServer -Executable $baselineExecutable -ContentRoot $baselineRoot -Database $databasePath -Port $rejectionPort -Name "baseline-reject-new-schema"
    $processes.Add($rejection)
    if (-not $rejection.WaitForExit(10000)) {
        Stop-DrillProcess $rejection
        throw "Baseline binary unexpectedly accepted the migrated database"
    }
    if ($rejection.ExitCode -eq 0) {
        throw "Baseline binary unexpectedly exited successfully with the migrated database"
    }
    $rejectionLog = Get-Content -LiteralPath (Join-Path $logRoot "baseline-reject-new-schema.stdout.log") -Raw
    if ($rejectionLog -notmatch "newer than supported") {
        throw "Baseline rejection did not identify the newer schema"
    }

    $report.business_restore = Invoke-RollbackBusinessCheck -Operation 'restore'
    $restoredSchemaVersion = Get-DatabaseSchemaVersion -Database $restoredDatabasePath
    if ($restoredSchemaVersion -ne $baselineSchemaVersion) {
        throw "Restored database schema is $restoredSchemaVersion, expected $baselineSchemaVersion"
    }
    $restoredHashBeforeStart = (Get-FileHash -Algorithm SHA256 -LiteralPath $restoredDatabasePath).Hash
    $rollbackPort = Get-FreeLoopbackPort
    $rolledBack = Start-DrillServer -Executable $baselineExecutable -ContentRoot $baselineRoot -Database $restoredDatabasePath -Port $rollbackPort -Name "baseline-restored"
    $processes.Add($rolledBack)
    Wait-Healthy -Process $rolledBack -Port $rollbackPort
    $report.restored_business_api = Invoke-RollbackBusinessCheck -Operation 'verify-http' -Database $restoredDatabasePath -Port $rollbackPort
    Stop-DrillProcess $rolledBack
    $report.restored_business_snapshot = Invoke-RollbackBusinessCheck -Operation 'verify-exact' -Database $restoredDatabasePath
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $backupPath).Hash -cne $backupHash) { throw 'Migration backup changed during restore' }
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $businessManifest).Hash -cne $report.fixture_manifest_sha256) { throw 'Business fixture manifest changed during drill' }
    $report.business_data_validation = 'passed'

    $observations = [ordered]@{
        executed_at = [DateTimeOffset]::UtcNow.ToString("o")
        local_only = $true
        baseline_ref = $BaselineRef
        baseline_commit = $baselineCommit
        target_schema_version = $targetSchemaVersion
        baseline_schema_version = $baselineSchemaVersion
        observed_schema_versions = [ordered]@{
            baseline_created = $createdBaselineSchemaVersion
            migrated = $migratedSchemaVersion
            restored = $restoredSchemaVersion
        }
        checks = [ordered]@{
            baseline_created_database = "passed"
            baseline_schema_verified = "passed"
            current_migrated_database = "passed"
            migrated_schema_verified = "passed"
            automatic_migration_backup_created = "passed"
            baseline_rejected_newer_schema = "passed"
            exact_migration_receipt_verified = "passed"
            backup_logical_snapshot_verified = "passed"
            independent_asset_backup_restored = "passed"
            restored_business_api_verified = "passed"
            restored_full_database_snapshot_verified = "passed"
            migration_backup_unchanged = "passed"
            restored_schema_verified = "passed"
            baseline_started_after_restore = "passed"
        }
        sha256 = [ordered]@{
            pre_migration_database = $preMigrationHash
            migrated_database = $migratedHash
            migration_backup = $backupHash
            restored_before_start = $restoredHashBeforeStart
        }
        remote_operations = @()
    }
    foreach ($key in $observations.Keys) { $report[$key] = $observations[$key] }
    $report.source_after = Get-AgentReleaseSourceSnapshot -Root $projectRoot
    if ($report.source_before.sha256 -cne $report.source_after.sha256) { throw 'Source changed during rollback drill' }
    $completed = $true
}
catch {
    $failure = $_
    $report.failure_type = $_.Exception.GetType().Name
    if ($report.business_data_validation -eq 'running') { $report.business_data_validation = 'failed' }
}
finally {
    $cleanupFailures = [Collections.Generic.List[string]]::new()
    foreach ($process in $processes) {
        try { Stop-DrillProcess $process }
        catch { $cleanupFailures.Add($_.Exception.GetType().Name) }
    }
    foreach ($name in $environmentOverrides.Keys) {
        try { [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], "Process") }
        catch { $cleanupFailures.Add($_.Exception.GetType().Name) }
    }
    if ($completed -and $cleanupFailures.Count -eq 0 -and -not $KeepWorkDirectory) {
        try {
            $verifiedWorkRoot = Assert-WorkspaceChildPath -Path $workRoot
            if (Test-Path -LiteralPath $verifiedWorkRoot) {
                foreach ($item in Get-ChildItem -LiteralPath $verifiedWorkRoot -Force -Recurse) {
                    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Refusing recursive cleanup through reparse points' }
                }
                Remove-Item -LiteralPath $verifiedWorkRoot -Recurse -Force
            }
        }
        catch { $cleanupFailures.Add($_.Exception.GetType().Name) }
    }
    $report.cleanup = if ($cleanupFailures.Count -eq 0) { 'passed' } else { 'failed' }
    $report.cleanup_failures = @($cleanupFailures.ToArray())
    $report.status = if ($completed -and $cleanupFailures.Count -eq 0) { 'passed' } else { 'failed' }
    $report.finished_at = [DateTimeOffset]::UtcNow.ToString('o')
    Write-AgentReleaseEvidence -Root $projectRoot -Path $ReportPath -Report $report
}
if ($null -ne $failure) { throw $failure }
if ($report.status -ne 'passed') { throw 'Rollback drill cleanup failed; inspect the current report' }
Write-Output "ROLLBACK_BUSINESS_FIXTURE_PASSED rollback_ready=false report=$ReportPath"
