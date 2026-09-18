[CmdletBinding()]
param(
    [string[]]$Packages = @("./..."),
    [string]$Timeout = "10m"
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$backendRoot = Join-Path $projectRoot "backend"
$go = Join-Path $projectRoot ".tools\go-sdk\go\bin\go.exe"
$testBinRoot = Join-Path $projectRoot ".tools\testbin"

if (-not (Test-Path -LiteralPath $go -PathType Leaf)) {
    throw "Bundled Go toolchain not found: $go"
}

New-Item -ItemType Directory -Force -Path $testBinRoot | Out-Null
$env:GOCACHE = Join-Path $projectRoot ".gocache"
$env:GOTMPDIR = Join-Path $projectRoot ".gotmp"

Push-Location $backendRoot
try {
    $packageList = @(
        & $go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}|{{.Dir}}{{end}}' @Packages |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($LASTEXITCODE -ne 0) {
        throw "go list failed"
    }

    $failures = @()
    foreach ($packageRecord in $packageList) {
        $package, $packageDir = $packageRecord -split '\|', 2
        $safeName = $package -replace '[^A-Za-z0-9._-]', '__'
        $binary = Join-Path $testBinRoot "$safeName.test.exe"
        & $go test -c -o $binary $package
        if ($LASTEXITCODE -ne 0) {
            $failures += "$package (compile)"
            continue
        }
        Push-Location $packageDir
        try {
            & $binary "-test.timeout=$Timeout"
            if ($LASTEXITCODE -ne 0) {
                $failures += "$package (test)"
            }
        }
        finally {
            Pop-Location
        }
    }

    if ($failures.Count -gt 0) {
        throw "Go test failures: $($failures -join ', ')"
    }
    Write-Output "GO_TESTS_PASSED packages=$($packageList.Count)"
}
finally {
    Pop-Location
}
