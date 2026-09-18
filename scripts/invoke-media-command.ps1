param(
    [Parameter(Mandatory = $true)]
    [string]$ExecutableBase64,
    [Parameter(Mandatory = $true)]
    [string]$ArgumentsBase64
)

$ErrorActionPreference = "Stop"

$executable = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($ExecutableBase64))
$argumentsJson = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($ArgumentsBase64))
$decodedArguments = ConvertFrom-Json -InputObject $argumentsJson
[string[]]$commandArguments = @($decodedArguments | ForEach-Object { [string]$_ })

function ConvertTo-CommandLineArgument([string]$value) {
    if ($value -notmatch '[\s"]') {
        return $value
    }
    $builder = [Text.StringBuilder]::new()
    [void]$builder.Append('"')
    $backslashes = 0
    foreach ($character in $value.ToCharArray()) {
        if ($character -eq '\') {
            $backslashes++
            continue
        }
        if ($character -eq '"') {
            [void]$builder.Append(('\' * (($backslashes * 2) + 1)))
            [void]$builder.Append('"')
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void]$builder.Append(('\' * $backslashes))
            $backslashes = 0
        }
        [void]$builder.Append($character)
    }
    if ($backslashes -gt 0) {
        [void]$builder.Append(('\' * ($backslashes * 2)))
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

$commandLineParts = @((ConvertTo-CommandLineArgument $executable))
$commandLineParts += @($commandArguments | ForEach-Object { ConvertTo-CommandLineArgument $_ })
$commandLine = $commandLineParts -join ' '
# cmd /S requires one outer quote pair when the executable path itself is quoted.
# Without it, Windows can intermittently pass argv[0] back as ffprobe's first input.
$cmdCommandLine = '"' + $commandLine + '"'
$maximumLaunchAttempts = 12
$ErrorActionPreference = "Continue"

for ($attempt = 1; $attempt -le $maximumLaunchAttempts; $attempt++) {
    $result = @(& cmd.exe /d /s /c $cmdCommandLine 2>&1)
    $exitCode = $LASTEXITCODE
    $text = ($result | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine
    $launchWasBlocked =
        $text -match '(?i)access is denied|拒绝访问' -or
        $text -match "provided as input filename, but .*ff(?:mpeg|probe)\.exe.*was already specified" -or
        ($exitCode -eq 0 -and [string]::IsNullOrWhiteSpace($text))
    if (-not $launchWasBlocked -or $attempt -eq $maximumLaunchAttempts) {
        if ($launchWasBlocked) {
            [Console]::Error.WriteLine("Windows blocked the media command after $maximumLaunchAttempts launch attempts.")
            exit 126
        }
        if (-not [string]::IsNullOrEmpty($text)) {
            if ($exitCode -eq 0) {
                [Console]::Out.WriteLine($text)
            }
            else {
                [Console]::Error.WriteLine($text)
            }
        }
        exit $exitCode
    }
    Start-Sleep -Milliseconds ([Math]::Min(250, 25 * $attempt))
}
