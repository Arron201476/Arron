[CmdletBinding()]
param([string]$ReportPath = '.tmp/goal-g449-release-evidence-tests.json')

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
. (Join-Path $PSScriptRoot 'agent-platform-release-evidence.ps1')
$fixture = Resolve-AgentReleaseChildPath -Root $root -Path ('.tmp/goal-g449-release-fixtures/' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $fixture | Out-Null
$results = [Collections.Generic.List[object]]::new()
$matrixJSON = Get-Content -LiteralPath (Join-Path $root 'acceptance/agent-platform-release-matrix.json') -Raw

function Test-Case {
    param([string]$Name, [scriptblock]$Body)
    try {
        & $Body | Out-Null
        $results.Add([ordered]@{ name = $Name; status = 'passed' })
    }
    catch {
        $results.Add([ordered]@{ name = $Name; status = 'failed'; error = $_.Exception.Message })
    }
}
function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}
function Assert-Rejected {
    param([scriptblock]$Body)
    $rejected = $false
    try { & $Body | Out-Null } catch { $rejected = $true }
    Assert-True $rejected 'Expected rejection'
}
function New-FixtureActions {
    $actions = @{}
    foreach ($gate in ($matrixJSON | ConvertFrom-Json).local_gates) { $actions[$gate.id] = {} }
    return $actions
}
function New-FixtureReport {
    return [ordered]@{ local_status = 'running'; release_status = 'pending_local_gates';
        release_ready = $false; local_release_candidate_ready = $false;
        source_before = @{ sha256 = 'source-before' }; matrix_sha256 = 'matrix-before' }
}

foreach ($id in ($matrixJSON | ConvertFrom-Json).local_gates.id) {
    Test-Case "missing local gate $id" {
        $matrix = $matrixJSON | ConvertFrom-Json
        $matrix.local_gates = @($matrix.local_gates | Where-Object { $_.id -ne $id })
        Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
    }
    Test-Case "optional local gate $id" {
        $matrix = $matrixJSON | ConvertFrom-Json
        ($matrix.local_gates | Where-Object { $_.id -eq $id }).required = $false
        Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
    }
}
foreach ($id in ($matrixJSON | ConvertFrom-Json).external_gates.id) {
    Test-Case "missing external gate $id" {
        $matrix = $matrixJSON | ConvertFrom-Json
        $matrix.external_gates = @($matrix.external_gates | Where-Object { $_.id -ne $id })
        Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
    }
    Test-Case "optional external gate $id" {
        $matrix = $matrixJSON | ConvertFrom-Json
        ($matrix.external_gates | Where-Object { $_.id -eq $id }).required_for_release = $false
        Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
    }
}
Test-Case 'duplicate gate' {
    $matrix = $matrixJSON | ConvertFrom-Json
    $matrix.local_gates += $matrix.local_gates[0]
    Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
}
Test-Case 'string boolean' {
    $matrix = $matrixJSON | ConvertFrom-Json
    $matrix.local_gates[0].required = 'true'
    Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
}
Test-Case 'different pinned SDK' {
    $matrix = $matrixJSON | ConvertFrom-Json
    $matrix.sdk_versions.openai_agents = '0.0.0'
    Assert-Rejected { Assert-AgentReleaseMatrix -Matrix $matrix -Actions (New-FixtureActions) }
}
Test-Case 'missing executable action' {
    $actions = New-FixtureActions
    $actions.Remove('go_test')
    Assert-Rejected { Assert-AgentReleaseMatrix -Matrix ($matrixJSON | ConvertFrom-Json) -Actions $actions }
}
Test-Case 'all fake checks pass but external declarations never establish release readiness' {
    $matrix = $matrixJSON | ConvertFrom-Json
    foreach ($gate in $matrix.external_gates) { $gate.status = 'passed' }
    $report = New-FixtureReport
    $states = [Collections.Generic.List[string]]::new()
    Invoke-AgentReleaseChecks -Matrix $matrix -Actions (New-FixtureActions) -Report $report -Checkpoint {
        $states.Add(($report | ConvertTo-Json -Depth 10 -Compress))
    }
    Assert-True ($states.Count -eq 2 * $matrix.local_gates.Count) 'Every start and terminal status must be checkpointed'
    $first = $states[0] | ConvertFrom-Json
    Assert-True ($first.local_gates[0].status -eq 'running') 'Start must be visible before action execution'
    Assert-True ($first.local_gates[1].status -eq 'pending') 'Unexecuted checks must be pending'
    Complete-AgentReleaseChecks -Report $report -SourceAfter @{ sha256 = 'source-before' } -MatrixSHA256 'matrix-before'
    Assert-True ($report.local_status -eq 'passed' -and -not $report.release_ready -and -not $report.local_release_candidate_ready) 'Local success cannot mean release ready'
    Assert-True ($report.release_status -eq 'pending_external_validation') 'External evidence is still missing'
    Assert-True (@($report.external_gates | Where-Object { $_.status -ne 'unverified' }).Count -eq 0) 'Declared passed must remain unverified'
}
Test-Case 'failed action remains failed and later diagnostics still run' {
    $report = New-FixtureReport
    $actions = New-FixtureActions
    $state = @{ later = $false }
    $actions.go_test = { throw 'fixture command failure' }
    $actions.rollback_drill = { $state.later = $true }
    Invoke-AgentReleaseChecks -Matrix ($matrixJSON | ConvertFrom-Json) -Actions $actions -Report $report -Checkpoint {}
    Assert-True ($report.local_status -eq 'failed' -and $state.later) 'Failure must not be lost or suppress later diagnostics'
    Assert-Rejected { Complete-AgentReleaseChecks -Report $report -SourceAfter @{ sha256 = 'source-before' } -MatrixSHA256 'matrix-before' }
}
Test-Case 'checkpoint failure prevents next action' {
    $report = New-FixtureReport
    $actions = New-FixtureActions
    $state = @{ called = $false }
    $actions.capability_contracts = { $state.called = $true }
    Assert-Rejected { Invoke-AgentReleaseChecks -Matrix ($matrixJSON | ConvertFrom-Json) -Actions $actions -Report $report -Checkpoint { throw 'fixture storage failure' } }
    Assert-True (-not $state.called) 'Do not run without a current evidence checkpoint'
}
foreach ($change in @('source', 'matrix', 'incomplete')) {
    Test-Case "reject final $change mismatch" {
        $report = New-FixtureReport
        $report.local_status = 'checks_passed_pending_source_validation'
        $sourceAfter = @{ sha256 = 'source-before' }
        $matrixAfter = 'matrix-before'
        if ($change -eq 'source') { $sourceAfter.sha256 = 'changed' }
        if ($change -eq 'matrix') { $matrixAfter = 'changed' }
        if ($change -eq 'incomplete') { $report.local_status = 'running' }
        Assert-Rejected { Complete-AgentReleaseChecks -Report $report -SourceAfter $sourceAfter -MatrixSHA256 $matrixAfter }
        Assert-True (-not $report.release_ready) 'Mismatch cannot be release ready'
    }
}
Test-Case 'atomic checkpoints replace old success without overwriting history' {
    $latest = Join-Path $fixture '.tmp/latest.json'
    $history = Join-Path $fixture '.tmp/previous.json'
    Write-AgentReleaseEvidence -Root $fixture -Path $latest -Report @{ run_id = 'old'; local_status = 'passed' }
    Write-AgentReleaseEvidence -Root $fixture -Path $history -Report @{ run_id = 'old'; local_status = 'passed' }
    foreach ($status in @('running', 'failed')) {
        Write-AgentReleaseEvidence -Root $fixture -Path $latest -Report @{ run_id = 'new'; local_status = $status }
        $read = Get-Content -LiteralPath $latest -Raw | ConvertFrom-Json
        Assert-True ($read.run_id -eq 'new' -and $read.local_status -eq $status) 'Latest report retained stale success'
    }
    Assert-True ((Get-Content -LiteralPath $history -Raw | ConvertFrom-Json).run_id -eq 'old') 'History was overwritten'
    Assert-True (@(Get-ChildItem -LiteralPath (Split-Path -Parent $latest) -Filter '*.tmp').Count -eq 0) 'Temporary report was leaked'
}
foreach ($invalid in @('../outside.json', '.tmp/../../outside.json', 'scripts/report.json', '.tmp/report.ps1', '.tmp-other/report.json')) {
    Test-Case "reject report path $invalid" {
        Assert-Rejected { Write-AgentReleaseEvidence -Root $fixture -Path $invalid -Report @{ status = 'passed' } }
    }
}
Test-Case 'reparse point ancestor rejected without following it' {
    $target = Join-Path $fixture '.tmp'
    function Get-Item {
        param([string]$LiteralPath, [switch]$Force)
        if ($LiteralPath -eq $target) { return @{ Attributes = [IO.FileAttributes]::ReparsePoint } }
        Microsoft.PowerShell.Management\Get-Item -LiteralPath $LiteralPath -Force:$Force
    }
    Assert-Rejected { Resolve-AgentReleaseChildPath -Root $fixture -Path '.tmp/linked/report.json' }
}
Test-Case 'source fingerprint includes untracked edits additions and deletion but excludes runtime outputs' {
    $sourceRoot = Join-Path $fixture 'source repo'
    New-Item -ItemType Directory -Force -Path (Join-Path $sourceRoot 'backend'), (Join-Path $sourceRoot 'data') | Out-Null
    & git init --quiet $sourceRoot
    if ($LASTEXITCODE -ne 0) { throw 'Unable to initialize isolated source fixture' }
    $sourceFile = Join-Path $sourceRoot 'backend/fixture.txt'
    [IO.File]::WriteAllText($sourceFile, 'first')
    $before = Get-AgentReleaseSourceSnapshot -Root $sourceRoot
    [IO.File]::WriteAllText((Join-Path $sourceRoot 'data/private.txt'), 'runtime-only')
    $same = Get-AgentReleaseSourceSnapshot -Root $sourceRoot
    Assert-True ($before.sha256 -ceq $same.sha256) 'Runtime output changed source identity'
    [IO.File]::WriteAllText($sourceFile, 'second')
    $edited = Get-AgentReleaseSourceSnapshot -Root $sourceRoot
    Assert-True ($before.sha256 -cne $edited.sha256) 'Untracked source edits were omitted'
    $extra = Join-Path $sourceRoot 'backend/extra.txt'
    [IO.File]::WriteAllText($extra, 'extra')
    $added = Get-AgentReleaseSourceSnapshot -Root $sourceRoot
    Assert-True ($added.sha256 -cne $edited.sha256 -and $added.file_count -eq 2) 'New source omitted'
    [IO.File]::Delete($extra)
    $deleted = Get-AgentReleaseSourceSnapshot -Root $sourceRoot
    Assert-True ($deleted.sha256 -ceq $edited.sha256) 'Deleted source remains in identity'
}
foreach ($entrypoint in @('run-agent-platform-local-gate.ps1', 'run-agent-platform-rollback-drill.ps1')) {
    Test-Case "actual $entrypoint preflight failure replaces stale success and restores environment" {
        # Materialize only these scripts, with no toolchains: execution must stop before any native command.
        $isolatedRoot = Join-Path $fixture ([IO.Path]::GetFileNameWithoutExtension($entrypoint))
        $isolatedScripts = Join-Path $isolatedRoot 'scripts'
        New-Item -ItemType Directory -Force -Path $isolatedScripts | Out-Null
        foreach ($name in @($entrypoint, 'agent-platform-release-evidence.ps1')) {
            [IO.File]::Copy((Join-Path $PSScriptRoot $name), (Join-Path $isolatedScripts $name))
        }
        $latest = Join-Path $isolatedRoot '.tmp/latest.json'
        Write-AgentReleaseEvidence -Root $isolatedRoot -Path $latest -Report @{ status = 'passed'; local_status = 'passed'; run_id = 'old' }
        $previous = @{}
        foreach ($name in @('GOTMPDIR', 'GOCACHE')) {
            $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
            [Environment]::SetEnvironmentVariable($name, "fixture-original-$name", 'Process')
        }
        try {
            $caught = $null
            try { & (Join-Path $isolatedScripts $entrypoint) -ReportPath $latest } catch { $caught = $_ }
            Assert-True ($null -ne $caught -and $caught.Exception.Message -like 'Bundled Go toolchain not found:*') 'Entry point did not stop at the expected missing-tool preflight'
            $current = Get-Content -LiteralPath $latest -Raw | ConvertFrom-Json
            Assert-True ($current.run_id -ne 'old') 'Entry point retained the old success receipt'
            if ($entrypoint -eq 'run-agent-platform-local-gate.ps1') {
                Assert-True ($current.local_status -eq 'failed' -and $current.failure_phase -eq 'preflight' -and -not $current.release_ready) 'Local gate preflight was not recorded as failed'
            } else {
                Assert-True ($current.status -eq 'failed' -and $current.cleanup -eq 'passed' -and -not $current.rollback_ready) 'Rollback preflight or cleanup status is incorrect'
                Assert-True ($current.business_data_validation -eq 'not_run') 'An unexecuted drill cannot prove business-data recovery'
            }
            foreach ($name in $previous.Keys) {
                Assert-True ([Environment]::GetEnvironmentVariable($name, 'Process') -ceq "fixture-original-$name") 'Preflight failure changed parent environment'
            }
        }
        finally {
            foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
        }
    }
}
Test-Case 'all touched PowerShell files parse without execution' {
    foreach ($name in @('agent-platform-release-evidence.ps1', 'run-agent-platform-local-gate.ps1',
        'run-agent-tool-approval-ui-smoke.ps1', 'run-agent-platform-rollback-drill.ps1')) {
        $tokens = $null
        $parseErrors = $null
        [void][Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $name), [ref]$tokens, [ref]$parseErrors)
        Assert-True ($parseErrors.Count -eq 0) "Parse errors in $name"
    }
}
$failed = @($results | Where-Object { $_.status -eq 'failed' })
$testReport = [ordered]@{ schema_version = 'agent_release_evidence_tests.v1';
    executed_at = [DateTimeOffset]::UtcNow.ToString('o'); total = $results.Count;
    passed = $results.Count - $failed.Count; failed = $failed.Count; tests = @($results.ToArray());
    proof_scope = 'PowerShell helpers and copied entry-point preflight failures in toolchain-free fixtures only; no gate actions, Go binary, browser or remote service executed' }
Write-AgentReleaseEvidence -Root $root -Path $ReportPath -Report $testReport
$failed | ConvertTo-Json -Depth 5 | Write-Output
Write-Output "RELEASE_EVIDENCE_TESTS total=$($results.Count) failed=$($failed.Count) report=$ReportPath"
if ($failed.Count -gt 0) { throw 'Release evidence fixture tests failed' }
