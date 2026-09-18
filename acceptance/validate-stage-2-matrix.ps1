[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$acceptanceDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $acceptanceDir
$matrixPath = Join-Path $acceptanceDir "stage-2-matrix.json"
$schemaPath = Join-Path $acceptanceDir "stage-2-matrix.schema.json"
$documentPath = Join-Path $root "docs\stage-2-acceptance-test-matrix.md"

$errors = [System.Collections.Generic.List[string]]::new()

function Add-ValidationError {
    param([Parameter(Mandatory = $true)][string]$Message)
    $errors.Add($Message)
}

function Get-UniqueValues {
    param([Parameter(Mandatory = $true)][object[]]$Values)
    return @($Values | Sort-Object -Unique)
}

try {
    $null = Get-Content -Raw -Encoding UTF8 -LiteralPath $schemaPath | ConvertFrom-Json
} catch {
    Add-ValidationError "Schema JSON cannot be parsed: $($_.Exception.Message)"
}

try {
    $matrix = Get-Content -Raw -Encoding UTF8 -LiteralPath $matrixPath | ConvertFrom-Json
} catch {
    Add-ValidationError "Matrix JSON cannot be parsed: $($_.Exception.Message)"
    $matrix = $null
}

if ($null -ne $matrix) {
    $allowedAreas = @(
        "design", "shell", "registry", "runtime", "artifact", "context",
        "asset", "api", "sse", "frontend", "novel", "non_novel",
        "video", "quality_review", "migration", "security", "nfr"
    )
    $allowedPriorities = @("P0", "P1", "P2")
    $allowedLevels = @("contract", "unit", "component", "integration", "e2e", "manual")
    $allowedAutomation = @("automated", "hybrid", "manual")
    $allowedGates = @("stage2_design", "implementation", "release")

    if ($matrix.status -ne "design_contract") {
        Add-ValidationError "status must be design_contract"
    }

    $gateIds = @($matrix.gates.id)
    $fixtureIds = @($matrix.fixtures.id)
    $caseIds = @($matrix.cases.id)

    if ((Get-UniqueValues $gateIds).Count -ne $gateIds.Count) {
        Add-ValidationError "Gate IDs must be unique"
    }
    if ((Get-UniqueValues $fixtureIds).Count -ne $fixtureIds.Count) {
        Add-ValidationError "Fixture IDs must be unique"
    }
    if ((Get-UniqueValues $caseIds).Count -ne $caseIds.Count) {
        Add-ValidationError "Case IDs must be unique"
    }

    foreach ($requiredGate in $allowedGates) {
        if ($requiredGate -notin $gateIds) {
            Add-ValidationError "Missing gate: $requiredGate"
        }
    }

    foreach ($fixture in $matrix.fixtures) {
        if ($fixture.id -notmatch "^fixture_[a-z0-9_]+$") {
            Add-ValidationError "Invalid fixture ID: $($fixture.id)"
        }
    }

    foreach ($case in $matrix.cases) {
        if ($case.id -notmatch "^[A-Z][A-Z0-9_]+-[0-9]{3}$") {
            Add-ValidationError "Invalid case ID: $($case.id)"
        }
        if ($case.area -notin $allowedAreas) {
            Add-ValidationError "$($case.id): invalid area $($case.area)"
        }
        if ($case.priority -notin $allowedPriorities) {
            Add-ValidationError "$($case.id): invalid priority $($case.priority)"
        }
        if ($case.level -notin $allowedLevels) {
            Add-ValidationError "$($case.id): invalid level $($case.level)"
        }
        if ($case.automation -notin $allowedAutomation) {
            Add-ValidationError "$($case.id): invalid automation $($case.automation)"
        }
        if ($case.required_by -notin $allowedGates) {
            Add-ValidationError "$($case.id): invalid gate $($case.required_by)"
        }
        if (@($case.assertions).Count -eq 0) {
            Add-ValidationError "$($case.id): assertions cannot be empty"
        }
        if (@($case.contract_refs).Count -eq 0) {
            Add-ValidationError "$($case.id): contract_refs cannot be empty"
        }

        foreach ($fixtureId in $case.fixture_ids) {
            if ($fixtureId -notin $fixtureIds) {
                Add-ValidationError "$($case.id): unknown fixture $fixtureId"
            }
        }

        foreach ($contractRef in $case.contract_refs) {
            $relativePath = ($contractRef -split "#")[0]
            $absolutePath = Join-Path $root $relativePath
            if (-not (Test-Path -LiteralPath $absolutePath)) {
                Add-ValidationError "$($case.id): missing contract reference $contractRef"
            }
        }
    }

    foreach ($area in $allowedAreas) {
        if (@($matrix.cases | Where-Object { $_.area -eq $area }).Count -eq 0) {
            Add-ValidationError "No test case covers area: $area"
        }
    }

    $requiredContracts = @(
        "docs/stage-2-architecture-baseline.md",
        "docs/capability-registry-contract.md",
        "docs/runtime-domain-model.md",
        "docs/artifact-dependency-contract.md",
        "docs/capability-context-pack-contract.md",
        "docs/asset-and-retention-contract.md",
        "docs/api-event-contract.md",
        "docs/frontend-capability-ui-contract.md",
        "docs/capability-workflow-schema-contract.md",
        "docs/content-semantics-and-quality-review-contract.md",
        "docs/data-migration-compatibility-plan.md"
    )
    $coveredContracts = @(
        $matrix.cases.contract_refs |
            ForEach-Object { ($_ -split "#")[0] } |
            Sort-Object -Unique
    )
    foreach ($contract in $requiredContracts) {
        if ($contract -notin $coveredContracts) {
            Add-ValidationError "Core contract is not covered: $contract"
        }
    }

    $qualityContractPath = Join-Path $root "docs\content-semantics-and-quality-review-contract.md"
    if (-not (Test-Path -LiteralPath $qualityContractPath)) {
        Add-ValidationError "Missing content semantics and quality review contract"
    } else {
        $qualityContract = Get-Content -Raw -Encoding UTF8 -LiteralPath $qualityContractPath
        $requiredQualityMarkers = @(
            "FACT",
            "CONFIRMED_CHANGE",
            "INFERENCE",
            "PROPOSAL",
            "UNKNOWN",
            "script_handoff",
            "review_script_set",
            "QualityReview",
            "conditional_review",
            "quality_review",
            "QualityOverride",
            "NEEDS_POLICY"
        )
        foreach ($marker in $requiredQualityMarkers) {
            if (-not $qualityContract.Contains($marker)) {
                Add-ValidationError "Quality contract is missing required marker: $marker"
            }
        }
    }

    $qualityLinkedDocuments = @(
        "docs/capability-registry-contract.md",
        "docs/runtime-domain-model.md",
        "docs/artifact-dependency-contract.md",
        "docs/capability-context-pack-contract.md",
        "docs/api-event-contract.md",
        "docs/frontend-capability-ui-contract.md",
        "docs/capability-workflow-schema-contract.md"
    )
    foreach ($relativeDocument in $qualityLinkedDocuments) {
        $linkedDocumentPath = Join-Path $root $relativeDocument
        $linkedDocument = Get-Content -Raw -Encoding UTF8 -LiteralPath $linkedDocumentPath
        if (-not $linkedDocument.Contains("content-semantics-and-quality-review-contract.md")) {
            Add-ValidationError "Quality contract is not referenced by $relativeDocument"
        }
    }

    $qualityCases = @($matrix.cases | Where-Object { $_.area -eq "quality_review" })
    if ($qualityCases.Count -ne 8) {
        Add-ValidationError "quality_review area must contain exactly 8 P0 cases"
    }
    foreach ($qualityCase in $qualityCases) {
        if ($qualityCase.priority -ne "P0") {
            Add-ValidationError "$($qualityCase.id): quality review cases must be P0"
        }
    }

    $document = Get-Content -Raw -Encoding UTF8 -LiteralPath $documentPath
    $documentCaseIds = @(
        [regex]::Matches($document, "\b[A-Z][A-Z0-9_]+-[0-9]{3}\b") |
            ForEach-Object { $_.Value } |
            Sort-Object -Unique
    )
    foreach ($caseId in $caseIds) {
        if ($caseId -notin $documentCaseIds) {
            Add-ValidationError "Case missing from human-readable matrix: $caseId"
        }
    }
    foreach ($caseId in $documentCaseIds) {
        if ($caseId -notin $caseIds) {
            Add-ValidationError "Unknown case in human-readable matrix: $caseId"
        }
    }
}

$capabilityValidatorPath = Join-Path $acceptanceDir "validate-capability-contracts.mjs"
if (-not (Test-Path -LiteralPath $capabilityValidatorPath)) {
    Add-ValidationError "Missing capability contract validator: $capabilityValidatorPath"
} else {
    $capabilityValidationOutput = @(& node $capabilityValidatorPath 2>&1)
    if ($LASTEXITCODE -ne 0) {
        Add-ValidationError ("Capability contract validation failed:`n" + ($capabilityValidationOutput -join "`n"))
    }
}

if ($errors.Count -gt 0) {
    Write-Error ("Stage 2 acceptance matrix validation failed:`n- " + ($errors -join "`n- "))
    exit 1
}

$prioritySummary = $matrix.cases |
    Group-Object priority |
    Sort-Object Name |
    ForEach-Object { "$($_.Name)=$($_.Count)" }
$gateSummary = $matrix.cases |
    Group-Object required_by |
    Sort-Object Name |
    ForEach-Object { "$($_.Name)=$($_.Count)" }

Write-Output "Stage 2 acceptance matrix validation passed."
Write-Output "Cases: $($matrix.cases.Count); Fixtures: $($matrix.fixtures.Count); Areas: $($allowedAreas.Count)"
Write-Output "Priorities: $($prioritySummary -join ', ')"
Write-Output "Required by: $($gateSummary -join ', ')"
Write-Output ($capabilityValidationOutput -join "`n")
