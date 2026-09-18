[CmdletBinding()]
param(
    [string]$MatrixPath = "",
    [string]$ReportPath = ""
)

$ErrorActionPreference = "Stop"
$projectRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
. (Join-Path $PSScriptRoot 'agent-platform-release-evidence.ps1')
if ([string]::IsNullOrWhiteSpace($MatrixPath)) {
    $MatrixPath = Join-Path $projectRoot "acceptance\agent-platform-release-matrix.json"
}
elseif (-not [IO.Path]::IsPathRooted($MatrixPath)) {
    $MatrixPath = Join-Path $projectRoot $MatrixPath
}
if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    $ReportPath = Join-Path $projectRoot "docs\evidence\agent-platform-local-release-gate.json"
}
elseif (-not [IO.Path]::IsPathRooted($ReportPath)) {
    $ReportPath = Join-Path $projectRoot $ReportPath
}
$MatrixPath = [IO.Path]::GetFullPath($MatrixPath)
$ReportPath = [IO.Path]::GetFullPath($ReportPath)
$runID = [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N')
$workRoot = Resolve-AgentReleaseChildPath -Root $projectRoot -Path ".tmp/agent-platform-release-gate/$runID"
$runReportPath = Join-Path $workRoot 'report.json'
$report = [ordered]@{
    schema_version = 'agent_platform_local_release_gate.v2'
    run_id = $runID
    started_at = [DateTimeOffset]::UtcNow.ToString('o')
    local_status = 'running'
    local_release_candidate_ready = $false
    release_ready = $false
    release_status = 'pending_local_gates'
    source_validation = 'pending'
    local_gates = @()
    external_gates = @()
    remote_operations = @()
}
$checkpoint = {
    $report.updated_at = [DateTimeOffset]::UtcNow.ToString('o')
    Write-AgentReleaseEvidence -Root $projectRoot -Path $runReportPath -Report $report
    Write-AgentReleaseEvidence -Root $projectRoot -Path $ReportPath -Report $report
}
& $checkpoint
$previousEnvironment = @{}
foreach ($name in @('GOTMPDIR', 'GOCACHE', 'PYTHONUTF8', 'PYTHONPATH')) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
$phase = 'preflight'
$backendRoot = Join-Path $projectRoot "backend"
$frontendRoot = Join-Path $projectRoot "frontend"
$sidecarRoot = Join-Path $projectRoot "experiments\openai-agents-sidecar"
$go = Join-Path $projectRoot ".tools\go-sdk\go\bin\go.exe"
$python = Join-Path $projectRoot ".tools\openai-agents-sidecar-venv\Scripts\python.exe"
$node = (Get-Command node -ErrorAction Stop).Source
$npm = (Get-Command npm.cmd -ErrorAction Stop).Source

if (-not (Test-Path -LiteralPath $go -PathType Leaf)) {
    throw "Bundled Go toolchain not found: $go"
}
if (-not (Test-Path -LiteralPath $python -PathType Leaf)) {
    throw "Sidecar virtual environment not found: $python"
}
$revisionOutput = @(& git -C $projectRoot rev-parse HEAD)
if ($LASTEXITCODE -ne 0 -or $revisionOutput.Count -ne 1) { throw 'Unable to identify source revision' }
$report.source_revision = $revisionOutput[0].Trim()
$statusOutput = @(& git -C $projectRoot status --porcelain)
if ($LASTEXITCODE -ne 0) { throw 'Unable to identify source worktree status' }
$report.source_worktree_dirty = $statusOutput.Count -gt 0

$matrix = Get-Content -LiteralPath $MatrixPath -Raw | ConvertFrom-Json
$report.matrix_sha256 = (Get-FileHash -LiteralPath $MatrixPath -Algorithm SHA256).Hash

function Invoke-NativeChecked {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$WorkingDirectory
    )
    Push-Location $WorkingDirectory
    try {
        & $Executable @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Command failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

$compatibilityJSON = Join-Path $workRoot 'sdk-compatibility.json'
$compatibilityMarkdown = Join-Path $workRoot 'sdk-compatibility.md'
$frontendBuildDirectory = Join-Path $workRoot 'frontend-dist'
$actions = @{
    release_evidence = {
        & (Join-Path $PSScriptRoot 'test-agent-platform-release-evidence.ps1') -ReportPath (Join-Path $workRoot 'evidence-tests.json')
        $env:PYTHONUTF8 = '1'
        Invoke-NativeChecked -Executable $python -Arguments @('-m', 'unittest', 'discover', '-s', 'scripts', '-p', 'test_agent_platform_rollback_data.py', '-v') -WorkingDirectory $projectRoot
    }
    capability_contracts = {
        Invoke-NativeChecked -Executable $node -Arguments @("acceptance/validate-capability-contracts.mjs") -WorkingDirectory $projectRoot
        Invoke-NativeChecked -Executable $node -Arguments @('--test', '--test-concurrency=1', 'acceptance/validate-capability-contracts.test.mjs') -WorkingDirectory $projectRoot
    }
    acceptance_plan = {
        Invoke-NativeChecked -Executable $node -Arguments @("acceptance/validate-stage-8-plan.mjs") -WorkingDirectory $projectRoot
    }
    legacy_agent_path_absent = {
        $patterns = @(
            "github.com/cloudwego/eino",
            '"content-agent/backend/internal/modelprovider"',
            "NewEinoAgentService",
            "NewSidecarAgentService",
            "type SidecarAgentService",
            "func (c *Core) ExecuteTurn",
            "func (c *Core) Decide",
            "/internal/v1/control/interpret",
            "/internal/v1/agent/decide",
            "async def execute_agent_turn",
            '"/v1/agent/runs"',
            '"/v1/agent/runs/stream"',
            "class AgentStream",
            "self._control_agent"
        )
        $legacyPaths = @(
            "backend\cmd\worker",
            "backend\cmd\video-worker",
            "backend\cmd\video-diagnostic",
            "backend\internal\worker",
            "backend\internal\workerdeploy",
            "backend\internal\modelprovider"
        )
        foreach ($legacyPath in $legacyPaths) {
            if (Test-Path -LiteralPath (Join-Path $projectRoot $legacyPath)) {
                throw "Legacy model execution path remains: $legacyPath"
            }
        }
        $files = @(Get-ChildItem -LiteralPath $backendRoot -Recurse -File | Where-Object {
            $_.Extension -eq ".go" -or $_.Name -in @("go.mod", "go.sum")
        })
        $files += @(Get-ChildItem -LiteralPath (Join-Path $sidecarRoot "src") -Recurse -File -Filter "*.py")
        $matches = $files | Select-String -SimpleMatch -Pattern $patterns
        if ($matches) {
            throw "Legacy Agent or direct Go model implementation remains in source"
        }
        $productionSidecarFiles = @(Get-ChildItem -LiteralPath (Join-Path $sidecarRoot "src") -Recurse -File -Filter "*.py" | Where-Object {
            $_.Name -ne "compatibility.py"
        })
        $directModelCalls = $productionSidecarFiles | Select-String -SimpleMatch -Pattern @(
            ".chat.completions.create(",
            ".responses.create(",
            ".responses.compact("
        )
        if ($directModelCalls) {
            throw "Production Sidecar model call bypasses the OpenAI Agents SDK Runner"
        }
        $builtInCapabilityIDs = @(
            "novel_to_script",
            "non_novel_to_script",
            "video_reference_creation",
            "outline_critic",
            "story_review_workflow",
            "story_research_digest"
        )
        $productionCapabilityFiles = @(
            Get-ChildItem -LiteralPath (Join-Path $backendRoot "internal") -Recurse -File -Filter "*.go" |
                Where-Object { $_.Name -notlike "*_test.go" }
        )
        $productionCapabilityFiles += @(
            Get-ChildItem -LiteralPath (Join-Path $backendRoot "cmd") -Recurse -File -Filter "*.go" |
                Where-Object { $_.Name -notlike "*_test.go" }
        )
        $productionCapabilityFiles += @(
            Get-ChildItem -LiteralPath (Join-Path $sidecarRoot "src") -Recurse -File -Filter "*.py" |
                Where-Object { $_.Name -notin @("compatibility.py", "evals.py") }
        )
        $productionCapabilityFiles += @(
            Get-ChildItem -LiteralPath (Join-Path $frontendRoot "src") -Recurse -File |
                Where-Object {
                    $_.Extension -in @(".ts", ".tsx") -and
                    $_.Name -notlike "*.test.*" -and
                    $_.Name -ne "testComposerFixtures.ts"
                }
        )
        $hardcodedCapabilities = $productionCapabilityFiles |
            Select-String -SimpleMatch -Pattern $builtInCapabilityIDs
        if ($hardcodedCapabilities) {
            throw "Production code contains a hardcoded built-in Capability ID"
        }
    }
    sdk_compatibility_local = {
        Invoke-NativeChecked -Executable $python -Arguments @(
            "scripts/compatibility_probe.py",
            "--json-output", $compatibilityJSON,
            "--markdown-output", $compatibilityMarkdown
        ) -WorkingDirectory $sidecarRoot
        $compatibility = Get-Content -LiteralPath $compatibilityJSON -Raw | ConvertFrom-Json
        if ($compatibility.live_requested) {
            throw "Local compatibility probe unexpectedly requested live access"
        }
        $failed = @($compatibility.checks | Where-Object { $_.status -ne "supported" })
        if ($failed.Count -gt 0) {
            throw "Local SDK compatibility checks are incomplete"
        }
        if ($compatibility.versions.openai_agents -ne $matrix.sdk_versions.openai_agents -or
            $compatibility.versions.openai -ne $matrix.sdk_versions.openai) {
            throw "Installed SDK versions do not match the release matrix"
        }
    }
    go_test = {
        $env:GOTMPDIR = Join-Path $projectRoot ".tmp\go-release-gate"
        $env:GOCACHE = Join-Path $projectRoot ".tmp\go-cache-w9"
        New-Item -ItemType Directory -Force -Path $env:GOTMPDIR, $env:GOCACHE | Out-Null
        Invoke-NativeChecked -Executable $go -Arguments @("test", "-p", "1", "./...", "-count=1", "-timeout=10m") -WorkingDirectory $backendRoot
    }
    go_vet = {
        Invoke-NativeChecked -Executable $go -Arguments @("vet", "./...") -WorkingDirectory $backendRoot
    }
    sidecar_test = {
        $env:PYTHONUTF8 = "1"
        $env:PYTHONPATH = Join-Path $sidecarRoot "src"
        Invoke-NativeChecked -Executable $python -Arguments @("-m", "pytest", "-q") -WorkingDirectory $sidecarRoot
    }
    frontend_test = {
        Invoke-NativeChecked -Executable $npm -Arguments @("test") -WorkingDirectory $frontendRoot
    }
    frontend_build = {
        Invoke-NativeChecked -Executable $node -Arguments @('node_modules/typescript/bin/tsc', '-p', 'tsconfig.app.json', '--noEmit') -WorkingDirectory $frontendRoot
        Invoke-NativeChecked -Executable $node -Arguments @('node_modules/vite/bin/vite.js', 'build', '--configLoader', 'native', '--outDir', $frontendBuildDirectory) -WorkingDirectory $frontendRoot
    }
    frontend_approval_visual = {
        if (@($report.local_gates | Where-Object { $_.id -eq 'frontend_build' -and $_.status -eq 'passed' }).Count -ne 1) {
            throw 'Current-run frontend build did not pass'
        }
        & (Join-Path $PSScriptRoot "run-agent-tool-approval-ui-smoke.ps1") -BuildDirectory $frontendBuildDirectory
    }
    rollback_drill = {
        & (Join-Path $PSScriptRoot "run-agent-platform-rollback-drill.ps1") -ReportPath (Join-Path $workRoot 'rollback.json')
    }
}

Assert-AgentReleaseMatrix -Matrix $matrix -Actions $actions
$report.sdk_versions = $matrix.sdk_versions
$report.source_before = Get-AgentReleaseSourceSnapshot -Root $projectRoot
& $checkpoint
$phase = 'local_gates'
Invoke-AgentReleaseChecks -Matrix $matrix -Actions $actions -Report $report -Checkpoint $checkpoint
$phase = 'source_validation'
Complete-AgentReleaseChecks -Report $report -SourceAfter (Get-AgentReleaseSourceSnapshot -Root $projectRoot) `
    -MatrixSHA256 (Get-FileHash -LiteralPath $MatrixPath -Algorithm SHA256).Hash
}
catch {
    $report.local_status = 'failed'
    $report.release_status = 'blocked_local_gates'
    $report.failure_phase = $phase
    $report.failure_type = $_.Exception.GetType().Name
    throw
}
finally {
    foreach ($name in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
    $report.finished_at = [DateTimeOffset]::UtcNow.ToString('o')
    & $checkpoint
}
Write-Output "AGENT_PLATFORM_LOCAL_CHECKS_PASSED release_ready=false report=$ReportPath"
