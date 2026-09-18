param(
    [string]$ApiBase = "http://127.0.0.1:8831",
    [int]$PollSeconds = 3,
    [int]$TimeoutMinutes = 12,
    [string]$ResumeRunId = "",
    [string]$ResumeProjectId = "",
    [ValidateRange(1, 100)]
    [int]$EpisodeCount = 1,
    [switch]$SkipMaterial
)

$ErrorActionPreference = "Stop"

function Invoke-Api {
    param(
        [ValidateSet("GET", "POST")]
        [string]$Method,
        [string]$Path,
        [object]$Body
    )

    $parameters = @{
        Method = $Method
        Uri = "$ApiBase$Path"
    }
    if ($null -ne $Body) {
        $parameters.ContentType = "application/json; charset=utf-8"
        $parameters.Body = $Body | ConvertTo-Json -Depth 12
    }
    Invoke-RestMethod @parameters
}

function ConvertFrom-Utf8Base64 {
    param([string]$Value)
    [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Value))
}

function Wait-RunCheckpoint {
    param([string]$RunId, [datetime]$Deadline)

    while ((Get-Date) -lt $Deadline) {
        $snapshot = Invoke-Api -Method GET -Path "/api/runs/$RunId"
        if ($snapshot.run.status -ne "running" -and $snapshot.run.status -ne "pending") {
            return $snapshot
        }
        Start-Sleep -Seconds $PollSeconds
    }
    throw "Run $RunId exceeded the $TimeoutMinutes minute smoke-test timeout."
}

function Complete-FlowSmoke {
    param(
        [string]$ProjectId,
        [string]$RunId,
        [ValidateSet("novel", "non_novel")]
        [string]$SourceMode,
        [string[]]$ExpectedTypes,
        [int]$ExpectedScriptUnits = 1
    )

    $deadline = (Get-Date).AddMinutes($TimeoutMinutes)
    $approved = 0
    $retried = 0
    while ($true) {
        $snapshot = Wait-RunCheckpoint -RunId $RunId -Deadline $deadline
        switch ($snapshot.run.status) {
            "completed" { break }
            "failed" {
                if ($retried -ge 1) {
                    $failedEvents = @($snapshot.events | Where-Object { $_.type -eq "step_failed" })
                    throw "Run $RunId failed after retry: $($failedEvents[-1] | ConvertTo-Json -Depth 8 -Compress)"
                }
                $failedStep = [string]$snapshot.run.current_step_id
                if ([string]::IsNullOrWhiteSpace($failedStep)) {
                    throw "Run $RunId failed without a recorded failed step."
                }
                Invoke-Api -Method POST -Path "/api/runs/$RunId/steps/$failedStep/rerun" -Body @{ reason = "live smoke retry after provider failure" } | Out-Null
                $retried += 1
            }
            "waiting_approval" {
                if ($null -eq $snapshot.approval_request -or [string]::IsNullOrWhiteSpace($snapshot.approval_request.approval_request_id)) {
                    throw "Run $RunId is waiting without an approval request."
                }
                Invoke-Api -Method POST -Path "/api/approvals/$($snapshot.approval_request.approval_request_id)/resolve" -Body @{ action = "approve" } | Out-Null
                $approved += 1
            }
            default { throw "Run $RunId stopped in unexpected status $($snapshot.run.status)." }
        }
        if ($snapshot.run.status -eq "completed") { break }
    }

    $final = Invoke-Api -Method GET -Path "/api/runs/$RunId"
    $activeArtifacts = @($final.artifacts | Where-Object { $_.status -ne "superseded" -and $_.status -ne "invalidated" })
    $activeTypes = @($activeArtifacts | ForEach-Object { $_.artifact_type } | Sort-Object -Unique)
    $missingTypes = @($ExpectedTypes | Where-Object { $_ -notin $activeTypes })
    $scriptUnits = @($activeArtifacts | Where-Object { $_.artifact_type -eq "script_unit" })
    $invalidatedIds = @($final.run.invalidated_artifacts | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
    $unexpectedStatuses = @($activeArtifacts | Where-Object { $_.status -ne "confirmed" })
    $duplicateTypes = @($ExpectedTypes | Where-Object { $_ -ne "script_unit" } | ForEach-Object {
        $expectedType = $_
        if (@($activeArtifacts | Where-Object { $_.artifact_type -eq $expectedType }).Count -ne 1) { $expectedType }
    })
    $scriptContextApprovals = @($final.events | Where-Object { $_.type -eq "approval_requested" -and $_.payload.artifact_type -eq "script_context" })
    $completedEvents = @($final.events | Where-Object { $_.type -eq "run_completed" })
    if ($missingTypes.Count -gt 0) {
        throw "Run $RunId is missing artifacts: $($missingTypes -join ', ')."
    }
    if ($scriptUnits.Count -ne $ExpectedScriptUnits) {
        throw "Run $RunId expected $ExpectedScriptUnits active script_unit artifacts, found $($scriptUnits.Count)."
    }
    if ([string]::IsNullOrWhiteSpace([string]$scriptUnits[0].payload.script_text)) {
        throw "Run $RunId produced an empty script_unit."
    }
    if ($invalidatedIds.Count -gt 0) {
        throw "Run $RunId unexpectedly invalidated artifacts: $($invalidatedIds -join ', ')."
    }
    if ($unexpectedStatuses.Count -gt 0) {
        throw "Run $RunId has active artifacts that are not confirmed."
    }
    if ($duplicateTypes.Count -gt 0) {
        throw "Run $RunId does not have exactly one active artifact for: $($duplicateTypes -join ', ')."
    }
    if ($scriptContextApprovals.Count -gt 0) {
        throw "Run $RunId exposed an approval for internal script_context."
    }
    if ($completedEvents.Count -ne 1) {
        throw "Run $RunId expected exactly one run_completed event, found $($completedEvents.Count)."
    }

    [pscustomobject]@{
        project_id = $ProjectId
        run_id = $RunId
        source_mode = $SourceMode
        status = $final.run.status
        approvals = $approved
        retries = $retried
        active_artifact_types = $activeTypes
        script_units = $scriptUnits.Count
    }
}

function Invoke-FlowSmoke {
    param(
        [string]$Title,
        [ValidateSet("novel", "non_novel")]
        [string]$SourceMode,
        [string]$Content,
        [string[]]$ExpectedTypes,
        [int]$TargetEpisodeCount = 1
    )

    $projectResponse = Invoke-Api -Method POST -Path "/api/projects" -Body @{
        title = $Title
        source_mode = $SourceMode
    }
    $projectId = $projectResponse.project.project_id

    $messageResponse = Invoke-Api -Method POST -Path "/api/projects/$projectId/messages" -Body @{
        content = $Content
        display_content = "Live smoke: generate $TargetEpisodeCount episode(s) at 1.5 minutes"
        source_mode_hint = $SourceMode
        generation_config = @{
            target_episode_count = $TargetEpisodeCount
            episode_duration_minutes = 1.5
            target_script_chars = 500
            boundary_detection_window_chars = 400
        }
    }
    if ($null -eq $messageResponse.run -or [string]::IsNullOrWhiteSpace($messageResponse.run.run_id)) {
        throw "Project $projectId did not start a run. Decision: $($messageResponse.decision | ConvertTo-Json -Depth 8 -Compress)"
    }

    Complete-FlowSmoke -ProjectId $projectId -RunId $messageResponse.run.run_id -SourceMode $SourceMode -ExpectedTypes $ExpectedTypes -ExpectedScriptUnits $TargetEpisodeCount
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$novelContent = ConvertFrom-Utf8Base64 "5rex5aSc77yM5p6X5aSP5Y+R546w54i25Lqy55WZ5LiL55qE5pen5b2V6Z+z77yM5b2V6Z+z6K+B5piO5ZCI5LyZ5Lq65Lyq6YCg5ZCI5ZCM44CC56ys5LqM5aSp5aW55Zyo5Lya6K6u5LiK5pKt5pS+5b2V6Z+z77yM6L+r5L2/5a+55pa55om/6K6k77yM5bm25Yaz5a6a6YeN5paw57uP6JCl54i25Lqy55qE5bqX44CC"
$materialContent = ConvertFrom-Utf8Base64 "6K+35oqK6L+Z5Liq54G15oSf55Sf5oiQMembhuefreWJp++8jOavj+mbhjAuNeWIhumSn++8muS4gOS4quaWsOadpeeahOekvuWMuuiwg+ino+WRmOWPkeeOsOS4pOS9jeS6ieWQteeahOmCu+WxheWFtuWunumDveWcqOWBt+WBt+eFp+mhvuWQjOS4gOS9jeeLrOWxheiAgeS6uu+8jOivr+S8muino+mZpOWQjuS7luS7rOS4gOi1t+aUuemAoOiAgeS6uua8j+mbqOeahOWxi+mhtuOAgg=="

$novelArguments = @{
    Title = "E2E-novel-smoke-$stamp"
    SourceMode = "novel"
    Content = $novelContent
    ExpectedTypes = @("source_input", "story_bible", "episode_split", "episode_cards", "script_context", "script_unit", "scripts")
    TargetEpisodeCount = $EpisodeCount
}
if ([string]::IsNullOrWhiteSpace($ResumeRunId)) {
    $novel = Invoke-FlowSmoke @novelArguments
} else {
    if ([string]::IsNullOrWhiteSpace($ResumeProjectId)) {
        throw "ResumeProjectId is required when ResumeRunId is provided."
    }
    $novel = Complete-FlowSmoke -ProjectId $ResumeProjectId -RunId $ResumeRunId -SourceMode "novel" -ExpectedTypes $novelArguments.ExpectedTypes -ExpectedScriptUnits $EpisodeCount
}

$results = @($novel)
if (-not $SkipMaterial) {
$materialArguments = @{
    Title = "E2E-material-smoke-$stamp"
    SourceMode = "non_novel"
    Content = $materialContent
    ExpectedTypes = @("source_input", "material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts")
}
$material = Invoke-FlowSmoke @materialArguments
$results += $material
}

$results | ConvertTo-Json -Depth 8
