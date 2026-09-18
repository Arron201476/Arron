param(
    [Parameter(Mandatory = $true)]
    [string]$ZipPath,

    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory,

    [Parameter(Mandatory = $true)]
    [string]$FFmpegPath,

    [Parameter(Mandatory = $true)]
    [string]$FFprobePath,

    [Parameter(Mandatory = $true)]
    [string]$CanonicalTitle
)

$ErrorActionPreference = "Stop"
$maxPreparedBytes = 10MB
$maxDurationSeconds = 180
$episodePattern = [char]0x7b2c + '(\d+)' + [char]0x96c6

if (-not (Test-Path -LiteralPath $ZipPath -PathType Leaf)) {
    throw "ZIP fixture does not exist: $ZipPath"
}
if (-not (Test-Path -LiteralPath $FFmpegPath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $FFprobePath -PathType Leaf)) {
    throw "FFmpeg and FFprobe are required"
}

New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$outputRoot = [System.IO.Path]::GetFullPath($OutputDirectory)
Add-Type -AssemblyName System.IO.Compression

function Get-VideoDurationSeconds {
    param([string]$Path)

    $raw = & $FFprobePath -v error -show_entries format=duration `
        -of default=noprint_wrappers=1:nokey=1 $Path
    if ($LASTEXITCODE -ne 0) {
        throw "FFprobe failed: $Path"
    }
    $seconds = 0.0
    $parsed = [double]::TryParse(
        ($raw | Select-Object -First 1),
        [System.Globalization.NumberStyles]::Float,
        [System.Globalization.CultureInfo]::InvariantCulture,
        [ref]$seconds
    )
    if (-not $parsed -or $seconds -le 0) {
        throw "Invalid video duration: $Path"
    }
    return $seconds
}

function Invoke-VideoPreparation {
    param(
        [string]$InputPath,
        [string]$OutputPath,
        [double]$DurationSeconds
    )

    $targetKbps = [int](9MB * 8 / $DurationSeconds / 1000) - 64
    if ($targetKbps -lt 128) {
        $targetKbps = 128
    }
    $bitrate = "${targetKbps}k"
    $arguments = @(
        "-nostdin", "-y", "-i", $InputPath,
        "-vf", "scale=w='min(1280,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
        "-c:v", "libx264", "-preset", "veryfast", "-b:v", $bitrate,
        "-maxrate", $bitrate, "-bufsize", "$($targetKbps * 2)k",
        "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart", $OutputPath
    )
    $previousErrorPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    & $FFmpegPath @arguments 2>$null
    $ffmpegExitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorPreference
    if ($ffmpegExitCode -ne 0) {
        throw "FFmpeg failed: $InputPath"
    }
    $prepared = Get-Item -LiteralPath $OutputPath
    if ($prepared.Length -le 0 -or $prepared.Length -gt $maxPreparedBytes) {
        throw "Prepared video exceeds 10 MB: $OutputPath ($($prepared.Length) bytes)"
    }
}

$zipStream = [System.IO.File]::OpenRead($ZipPath)
$archive = $null
try {
    $archive = [System.IO.Compression.ZipArchive]::new(
        $zipStream,
        [System.IO.Compression.ZipArchiveMode]::Read,
        $false,
        [System.Text.Encoding]::UTF8
    )
    $videos = @(
        $archive.Entries |
            Where-Object { $_.Name -match "\.(mp4|mov)$" -and $_.FullName -notlike "__MACOSX/*" } |
            ForEach-Object {
                if ($_.Name -notmatch $episodePattern) {
                    throw "Episode number is missing: $($_.FullName)"
                }
                [PSCustomObject]@{ Episode = [int]$Matches[1]; Entry = $_ }
            } |
            Sort-Object Episode
    )
    $duplicates = @($videos | Group-Object Episode | Where-Object Count -gt 1)
    if ($videos.Count -eq 0 -or $duplicates.Count -gt 0) {
        throw "Video fixture has no episodes or duplicate episode numbers"
    }

    $manifestItems = @()
    foreach ($video in $videos) {
        $episode = $video.Episode
        $entry = $video.Entry
        $preparedName = "{0}-{1}{2:D2}{3}.mp4" -f $CanonicalTitle, [char]0x7b2c, $episode, [char]0x96c6
        $preparedPath = Join-Path $outputRoot $preparedName
        $temporaryPath = Join-Path $outputRoot (".source-{0:D2}.mov" -f $episode)

        if (Test-Path -LiteralPath $preparedPath -PathType Leaf) {
            $prepared = Get-Item -LiteralPath $preparedPath
            if ($prepared.Length -gt 0 -and $prepared.Length -le $maxPreparedBytes) {
                try {
                    $duration = Get-VideoDurationSeconds -Path $preparedPath
                    $manifestItems += [PSCustomObject]@{
                        episode_no = $episode
                        original_entry_name = $entry.FullName
                        original_bytes = $entry.Length
                        original_over_50mb = $entry.Length -gt 50MB
                        duration_seconds = [math]::Round($duration, 3)
                        prepared_file_name = $preparedName
                        prepared_bytes = $prepared.Length
                        reused = $true
                    }
                    Write-Output "REUSED episode=$episode bytes=$($prepared.Length)"
                    continue
                }
                catch {
                    Remove-Item -LiteralPath $preparedPath -Force
                }
            }
        }

        $entryStream = $entry.Open()
        $temporaryStream = [System.IO.File]::Create($temporaryPath)
        try {
            $entryStream.CopyTo($temporaryStream)
        }
        finally {
            $temporaryStream.Dispose()
            $entryStream.Dispose()
        }

        try {
            $duration = Get-VideoDurationSeconds -Path $temporaryPath
            if ($duration -gt $maxDurationSeconds) {
                throw "Episode $episode exceeds 3 minutes"
            }
            Invoke-VideoPreparation -InputPath $temporaryPath -OutputPath $preparedPath -DurationSeconds $duration
        }
        finally {
            if (Test-Path -LiteralPath $temporaryPath) {
                Remove-Item -LiteralPath $temporaryPath -Force
            }
        }

        $prepared = Get-Item -LiteralPath $preparedPath
        $manifestItems += [PSCustomObject]@{
            episode_no = $episode
            original_entry_name = $entry.FullName
            original_bytes = $entry.Length
            original_over_50mb = $entry.Length -gt 50MB
            duration_seconds = [math]::Round($duration, 3)
            prepared_file_name = $preparedName
            prepared_bytes = $prepared.Length
            reused = $false
        }
        Write-Output "PREPARED episode=$episode bytes=$($prepared.Length)"
    }

    $manifest = [ordered]@{
        schema_version = "1.0.0"
        canonical_title = $CanonicalTitle
        source_zip = [System.IO.Path]::GetFileName($ZipPath)
        episode_count = $manifestItems.Count
        generated_at = [DateTimeOffset]::Now.ToString("o")
        episodes = $manifestItems
    }
    $manifestPath = Join-Path $outputRoot "series-manifest.json"
    $manifest | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $manifestPath -Encoding UTF8
    Write-Output "MANIFEST $manifestPath"
}
finally {
    if ($archive) {
        $archive.Dispose()
    }
    $zipStream.Dispose()
}
