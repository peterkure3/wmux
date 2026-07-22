#Requires -Version 5.1
<#
.SYNOPSIS
    Installs wmux: downloads a published GitHub release, places
    wmux.exe/wmuxd.exe under %LOCALAPPDATA%\Programs\wmux, adds that
    directory to the current user's PATH, and (by default) registers wmuxd
    to start at logon.

.DESCRIPTION
    No admin rights needed — everything here is per-user (user PATH,
    Task Scheduler task at /RL LIMITED, same privilege level `wmux update`
    already runs wmuxd with). Same release archive, same SHA256SUMS
    verification convention as `wmux update --release` (cmd/wmux/release.go)
    — this script exists for the bootstrap case where wmux.exe doesn't
    exist yet to run that command with.

.PARAMETER Version
    A release tag (e.g. "v0.2.1") or "latest" (default).

.PARAMETER InstallDir
    Where to place wmux.exe/wmuxd.exe. Default: %LOCALAPPDATA%\Programs\wmux.

.PARAMETER NoAutostart
    Skip registering the Task Scheduler autostart entry (and skip starting
    wmuxd now). You can run `wmux autostart install` yourself later.

.EXAMPLE
    iwr https://raw.githubusercontent.com/peterkure3/wmux/main/install/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Version v0.2.1 -InstallDir D:\tools\wmux
#>
param(
    [string]$Version = "latest",
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\wmux",
    [switch]$NoAutostart
)

$ErrorActionPreference = "Stop"

# Matches releaseRepo in cmd/wmux/release.go: the GitHub repo (peterkure3,
# with a 3) deliberately differs from go.mod's module path (peterkure).
$repo = "peterkure3/wmux"

function Get-LatestTag {
    $resp = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ "User-Agent" = "wmux-installer" }
    if (-not $resp.tag_name) {
        throw "could not resolve the latest release tag from GitHub"
    }
    return $resp.tag_name
}

if ($Version -eq "latest") {
    Write-Host "Resolving latest release..."
    $Version = Get-LatestTag
}
Write-Host "Installing wmux $Version to $InstallDir"

$asset = "wmux_${Version}_windows-amd64.zip"
$base = "https://github.com/$repo/releases/download/$Version/"
$staging = Join-Path $env:TEMP "wmux-install-$([guid]::NewGuid())"
New-Item -ItemType Directory -Path $staging -Force | Out-Null

try {
    $zipPath = Join-Path $staging $asset
    Write-Host "Downloading $asset..."
    Invoke-WebRequest -Uri "$base$asset" -OutFile $zipPath -UseBasicParsing

    Write-Host "Verifying SHA256SUMS..."
    # GitHub serves this asset as application/octet-stream, so
    # Invoke-WebRequest's .Content comes back as a raw byte[] rather than a
    # string (PowerShell 5.1 only auto-decodes recognized text content
    # types) -- decode explicitly rather than -split-ing the byte array.
    $sumsRaw = (Invoke-WebRequest -Uri "$($base)SHA256SUMS" -UseBasicParsing).Content
    $sums = if ($sumsRaw -is [byte[]]) { [System.Text.Encoding]::UTF8.GetString($sumsRaw) } else { $sumsRaw }
    $wantLine = ($sums -split "`n") | Where-Object { $_ -match [regex]::Escape($asset) } | Select-Object -First 1
    if (-not $wantLine) {
        throw "SHA256SUMS has no entry for $asset"
    }
    $want = ($wantLine -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want) {
        throw "SHA256 mismatch for ${asset}: got $got, want $want -- refusing to install a tampered or corrupted archive"
    }

    Write-Host "Extracting..."
    Expand-Archive -Path $zipPath -DestinationPath $staging -Force

    $wmuxSrc = Get-ChildItem -Path $staging -Filter "wmux.exe" -Recurse | Select-Object -First 1
    $wmuxdSrc = Get-ChildItem -Path $staging -Filter "wmuxd.exe" -Recurse | Select-Object -First 1
    if (-not $wmuxSrc -or -not $wmuxdSrc) {
        throw "release archive is missing wmux.exe/wmuxd.exe"
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

    # An already-running wmuxd holds its own .exe locked (same reason
    # cmd/wmux/update.go stops it before swapping) -- stop it first if this
    # is a reinstall over a live daemon.
    $wmuxdDest = Join-Path $InstallDir "wmuxd.exe"
    $healthUrl = "http://127.0.0.1:47823/healthz"
    $wasRunning = $false
    try {
        Invoke-WebRequest -Uri $healthUrl -TimeoutSec 1 -UseBasicParsing | Out-Null
        $wasRunning = $true
    } catch {}
    if ($wasRunning -and (Test-Path $wmuxdDest)) {
        Write-Host "Stopping running wmuxd..."
        try {
            Invoke-WebRequest -Uri "http://127.0.0.1:47823/shutdown" -Method Post -TimeoutSec 2 -UseBasicParsing | Out-Null
        } catch {}
        Start-Sleep -Seconds 1
    }

    Copy-Item -Path $wmuxSrc.FullName -Destination (Join-Path $InstallDir "wmux.exe") -Force
    Copy-Item -Path $wmuxdSrc.FullName -Destination $wmuxdDest -Force
}
finally {
    Remove-Item -Path $staging -Recurse -Force -ErrorAction SilentlyContinue
}

# Add InstallDir to the user's persisted PATH (registry), not just this
# process's PATH -- so it survives into every future terminal, not just
# this script's own session.
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathEntries = $userPath -split ";" | Where-Object { $_ -ne "" }
if ($pathEntries -notcontains $InstallDir) {
    Write-Host "Adding $InstallDir to your user PATH..."
    $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    $env:Path = "$env:Path;$InstallDir" # so autostart install below can find wmux.exe in this same session
} else {
    $env:Path = "$env:Path;$InstallDir"
}

if (-not $NoAutostart) {
    Write-Host "Registering wmuxd autostart..."
    & (Join-Path $InstallDir "wmux.exe") autostart install
} else {
    Write-Host "Skipped autostart (-NoAutostart) -- run 'wmux autostart install' later to enable it."
}

Write-Host ""
Write-Host "wmux $Version installed to $InstallDir"
Write-Host "Open a NEW terminal for the updated PATH to take effect, then run: wmux version"
