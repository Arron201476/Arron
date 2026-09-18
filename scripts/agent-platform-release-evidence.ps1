function Resolve-AgentReleaseChildPath {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$Path)
    $rootPath = [IO.Path]::GetFullPath($Root)
    if (-not [IO.Path]::IsPathRooted($Path)) { $Path = Join-Path $rootPath $Path }
    $fullPath = [IO.Path]::GetFullPath($Path)
    $prefix = $rootPath.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $fullPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Release path must be a child of its approved root"
    }
    $ancestor = $fullPath
    while ($ancestor) {
        if (Test-Path -LiteralPath $ancestor) {
            $item = Get-Item -LiteralPath $ancestor -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "Release paths must not traverse a reparse point"
            }
        }
        $ancestor = Split-Path -Parent $ancestor
    }
    return $fullPath
}

function Write-AgentReleaseEvidence {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][Collections.IDictionary]$Report
    )
    $fullPath = Resolve-AgentReleaseChildPath -Root $Root -Path $Path
    $relative = $fullPath.Substring([IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar).Length + 1).Replace('\', '/')
    if (($relative -notlike '.tmp/*' -and $relative -notlike 'docs/evidence/*') -or
        [IO.Path]::GetExtension($fullPath) -ne '.json') {
        throw "Release reports must be JSON inside .tmp or docs/evidence"
    }
    $directory = Split-Path -Parent $fullPath
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $temporary = Join-Path $directory ('.release-report-' + [Guid]::NewGuid().ToString('N') + '.tmp')
    try {
        [IO.File]::WriteAllText($temporary, ($Report | ConvertTo-Json -Depth 20) + "`n", [Text.UTF8Encoding]::new($false))
        if ([IO.File]::Exists($fullPath)) { [IO.File]::Replace($temporary, $fullPath, [NullString]::Value) }
        else { [IO.File]::Move($temporary, $fullPath) }
    }
    finally {
        if ([IO.File]::Exists($temporary)) { [IO.File]::Delete($temporary) }
    }
}

function Get-AgentReleaseSourceSnapshot {
    param([Parameter(Mandatory)][string]$Root)
    # Exclude runtime data, installed dependencies and generated evidence; hash uncommitted source too.
    $scope = @('.gitignore', 'backend', 'frontend', 'experiments/openai-agents-sidecar',
        'scripts', 'acceptance', 'capabilities', 'schemas', 'design', 'novel2script_agent_project',
        '.agents/skills', 'configs/agent-tools.example.json',
        'fixtures', 'docs', ':(exclude)docs/evidence', ':(exclude)**/.env', ':(exclude)**/.env.*')
    $inventory = @(& git -C $Root ls-files -z --cached --others --exclude-standard -- @scope)
    if ($LASTEXITCODE -ne 0) { throw "Unable to inventory release source" }
    $paths = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($path in (($inventory -join "`n").Split([char]0))) {
        if ($path) { [void]$paths.Add($path) }
    }
    if ($paths.Count -eq 0) { throw "Release source inventory is empty" }
    $ordered = [string[]]@($paths)
    [Array]::Sort($ordered, [StringComparer]::Ordinal)
    $manifest = [Text.StringBuilder]::new("agent-release-source-v1`0")
    foreach ($relative in $ordered) {
        $path = Resolve-AgentReleaseChildPath -Root $Root -Path $relative
        $hash = if (Test-Path -LiteralPath $path -PathType Leaf) {
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        } else { 'missing' }
        [void]$manifest.Append($relative).Append([char]0).Append($hash).Append([char]0)
    }
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $digest = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($manifest.ToString()))).Replace('-', '').ToLowerInvariant()
    }
    finally { $sha.Dispose() }
    return [ordered]@{ schema_version = 'agent_release_source.v1'; sha256 = $digest; file_count = $paths.Count; scope = $scope }
}

function Assert-AgentReleaseMatrix {
    param([Parameter(Mandatory)]$Matrix, [Parameter(Mandatory)][Collections.IDictionary]$Actions)
    if ($Matrix.schema_version -cne 'agent_platform_release_matrix.v1' -or
        $Matrix.sdk_versions.openai_agents -cne '0.21.1' -or $Matrix.sdk_versions.openai -cne '3.3.1') {
        throw "Unsupported release matrix schema or SDK versions"
    }
    $requiredLocal = @('release_evidence', 'capability_contracts', 'acceptance_plan', 'legacy_agent_path_absent',
        'sdk_compatibility_local', 'go_test', 'go_vet', 'sidecar_test', 'frontend_test',
        'frontend_build', 'frontend_approval_visual', 'rollback_drill')
    $requiredExternal = @('gateway_live_responses_tools_skills_compact', 'pre_release_full_e2e', 'oci_sandbox_attack_suite')
    foreach ($group in @(
        @{ gates = @($Matrix.local_gates); required = $requiredLocal; field = 'required'; local = $true },
        @{ gates = @($Matrix.external_gates); required = $requiredExternal; field = 'required_for_release'; local = $false }
    )) {
        $ids = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach ($gate in $group.gates) {
            if ($null -eq $gate -or $gate.id -isnot [string] -or [string]::IsNullOrWhiteSpace($gate.id) -or
                -not $ids.Add($gate.id) -or $gate.($group.field) -isnot [bool]) {
                throw "Invalid or duplicate release gate"
            }
            if ($group.local -and (-not $Actions.Contains($gate.id) -or $Actions[$gate.id] -isnot [scriptblock])) {
                throw "No implementation exists for a local release gate"
            }
            if ($group.required -ccontains $gate.id) {
                if (-not $gate.($group.field)) { throw "A mandatory release gate cannot be optional" }
            }
        }
        foreach ($id in $group.required) {
            if (-not $ids.Contains($id)) { throw "A mandatory release gate is missing" }
        }
    }
}

function Invoke-AgentReleaseChecks {
    param(
        [Parameter(Mandatory)]$Matrix,
        [Parameter(Mandatory)][Collections.IDictionary]$Actions,
        [Parameter(Mandatory)][Collections.IDictionary]$Report,
        [Parameter(Mandatory)][scriptblock]$Checkpoint
    )
    Assert-AgentReleaseMatrix -Matrix $Matrix -Actions $Actions
    $Report.local_gates = @($Matrix.local_gates | ForEach-Object {
        [ordered]@{ id = $_.id; required = $_.required; status = 'pending' }
    })
    # A declaration in the input matrix is not an external execution receipt.
    $Report.external_gates = @($Matrix.external_gates | ForEach-Object {
        [ordered]@{ id = $_.id; required_for_release = $_.required_for_release;
            declared_status = $_.status; status = 'unverified'; reason = $_.reason }
    })
    $Report.release_ready = $false
    $Report.local_release_candidate_ready = $false
    foreach ($gate in $Report.local_gates) {
        $gate.status = 'running'
        & $Checkpoint | Out-Null
        $timer = [Diagnostics.Stopwatch]::StartNew()
        try {
            & $Actions[$gate.id] | Out-Host
            $gate.status = 'passed'
        }
        catch {
            $gate.status = 'failed'
            $gate.failure_type = $_.Exception.GetType().Name
            Write-Warning "Release gate $($gate.id) failed: $($gate.failure_type)"
        }
        finally { $timer.Stop() }
        $gate.duration_ms = [Math]::Round($timer.Elapsed.TotalMilliseconds)
        & $Checkpoint | Out-Null
    }
    $failed = @($Report.local_gates | Where-Object { $_.required -and $_.status -ne 'passed' }).Count -gt 0
    $Report.local_status = if ($failed) { 'failed' } else { 'checks_passed_pending_source_validation' }
}

function Complete-AgentReleaseChecks {
    param(
        [Parameter(Mandatory)][Collections.IDictionary]$Report,
        [Parameter(Mandatory)][Collections.IDictionary]$SourceAfter,
        [Parameter(Mandatory)][string]$MatrixSHA256
    )
    $Report.source_after = $SourceAfter
    $Report.source_validation = if ($Report.source_before.sha256 -and
        $Report.source_before.sha256 -ceq $SourceAfter.sha256 -and
        $Report.matrix_sha256 -ceq $MatrixSHA256) { 'unchanged' } else { 'changed' }
    if ($Report.source_validation -ne 'unchanged') { throw 'Source changed during the local gate; rerun on a stable version' }
    if ($Report.local_status -ne 'checks_passed_pending_source_validation') { throw 'Mandatory local gates did not finish successfully' }
    $Report.local_status = 'passed'
    $Report.release_status = 'pending_external_validation'
    $Report.release_ready = $false
    $Report.local_release_candidate_ready = $false
}
