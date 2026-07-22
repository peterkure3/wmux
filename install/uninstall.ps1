#Requires -Version 5.1
<#
.SYNOPSIS
    Uninstalls wmux: stops wmuxd, removes the Task Scheduler autostart
    entry, removes InstallDir from the user PATH, and deletes the
    installed binaries.

.PARAMETER InstallDir
    Where wmux.exe/wmuxd.exe were installed. Default: %LOCALAPPDATA%\Programs\wmux.

.PARAMETER Purge
    Also delete ~/.wmux (session state, logs, theme/log-level settings,
    debug dumps). Without this, user data survives a reinstall.

.EXAMPLE
    .\uninstall.ps1

.EXAMPLE
    .\uninstall.ps1 -Purge
#>
param(
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\wmux",
    [switch]$Purge
)

$ErrorActionPreference = "Stop"

$wmuxExe = Join-Path $InstallDir "wmux.exe"

if (Test-Path $wmuxExe) {
    Write-Host "Removing autostart entry..."
    try {
        & $wmuxExe autostart uninstall
    } catch {
        Write-Warning "wmux autostart uninstall failed: $_"
    }
} else {
    Write-Warning "$wmuxExe not found -- removing the Task Scheduler entry directly."
    try {
        schtasks /Delete /TN "wmux-wmuxd" /F 2>&1 | Out-Null
    } catch {}
}

$healthUrl = "http://127.0.0.1:47823/healthz"
try {
    Invoke-WebRequest -Uri $healthUrl -TimeoutSec 1 -UseBasicParsing | Out-Null
    Write-Host "Stopping running wmuxd..."
    try {
        Invoke-WebRequest -Uri "http://127.0.0.1:47823/shutdown" -Method Post -TimeoutSec 2 -UseBasicParsing | Out-Null
    } catch {
        # Predates /shutdown, or already gone -- fall back to a hard kill.
        Get-Process -Name "wmuxd" -ErrorAction SilentlyContinue | Stop-Process -Force
    }
    Start-Sleep -Seconds 1
} catch {
    # Not running -- nothing to stop.
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath) {
    $pathEntries = $userPath -split ";" | Where-Object { $_ -ne "" -and $_ -ne $InstallDir }
    [Environment]::SetEnvironmentVariable("Path", ($pathEntries -join ";"), "User")
}

if (Test-Path $InstallDir) {
    Write-Host "Removing $InstallDir..."
    Remove-Item -Path $InstallDir -Recurse -Force
}

if ($Purge) {
    $dataDir = Join-Path $env:USERPROFILE ".wmux"
    if (Test-Path $dataDir) {
        Write-Host "Purging $dataDir (session state, logs, settings)..."
        Remove-Item -Path $dataDir -Recurse -Force
    }
}

Write-Host ""
Write-Host "wmux uninstalled."
if (-not $Purge) {
    Write-Host "Session state, logs, and settings were left at $env:USERPROFILE\.wmux -- rerun with -Purge to remove them too."
}
