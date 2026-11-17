#Requires -Version 5.1
<#
.SYNOPSIS
Installs a locally verified Rust pair. Preserves Go in legacy/ when supplied.
No task, service, startup entry or background process is created.
#>
param(
    [string]$BinDir,
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\wmux'),
    [string]$LegacyBinDir,
    [ValidateSet('single','grid','columns','rows','main-stack')][string]$DefaultLayout = 'grid',
    [string]$ConfigDir = (Join-Path $env:USERPROFILE '.wmux-rust')
)
$ErrorActionPreference = 'Stop'
if (-not $BinDir) {
    $BinDir = if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'wmux.exe')) { $PSScriptRoot } else { Join-Path $PSScriptRoot '..\target\release' }
}
$BinDir = [IO.Path]::GetFullPath($BinDir)
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$pairs = @()
foreach ($name in @('wmux.exe','wmuxd.exe')) {
    $source = Join-Path $BinDir $name
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "Missing tested binary: $source" }
    $pairs += [pscustomobject]@{Source=$source; Target=(Join-Path $InstallDir $name)}
}
$version = (& (Join-Path $BinDir 'wmux.exe') --version 2>&1 | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $version -notmatch '^wmux 0\.') { throw 'Source is not a Rust WMUX binary' }
if ($LegacyBinDir) {
    $LegacyBinDir = [IO.Path]::GetFullPath($LegacyBinDir)
    foreach ($name in @('wmux.exe','wmuxd.exe')) {
        $source = Join-Path $LegacyBinDir $name
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "Missing Go compatibility binary: $source" }
        $pairs += [pscustomobject]@{Source=$source; Target=(Join-Path $InstallDir "legacy\$name")}
    }
} elseif ((Test-Path -LiteralPath (Join-Path $InstallDir 'wmux.exe')) -and -not (Test-Path -LiteralPath (Join-Path $InstallDir 'legacy\wmux.exe'))) {
    throw 'Existing installation: supply -LegacyBinDir with the Go pair to preserve compatibility'
}
$stamp = [guid]::NewGuid().ToString('N')
$backup = Join-Path $InstallDir "backups\$stamp"
$changed = @()
try {
    foreach ($pair in $pairs) {
        [IO.Directory]::CreateDirectory((Split-Path -Parent $pair.Target)) > $null
        $stage = $pair.Target+".stage-$stamp"
        Copy-Item -LiteralPath $pair.Source -Destination $stage
        if ((Get-FileHash -LiteralPath $stage).Hash -ne (Get-FileHash -LiteralPath $pair.Source).Hash) { throw 'Staged binary checksum mismatch' }
        $old = $null
        if (Test-Path -LiteralPath $pair.Target) {
            $relative = $pair.Target.Substring($InstallDir.Length).TrimStart('\')
            $old = Join-Path $backup $relative
            [IO.Directory]::CreateDirectory((Split-Path -Parent $old)) > $null
            Move-Item -LiteralPath $pair.Target -Destination $old
        }
        $changed += [pscustomobject]@{Target=$pair.Target; Old=$old}
        Move-Item -LiteralPath $stage -Destination $pair.Target
    }
} catch {
    for ($i=$changed.Count-1; $i -ge 0; $i--) {
        $item=$changed[$i]
        if (Test-Path -LiteralPath $item.Target) { Remove-Item -LiteralPath $item.Target }
        if ($item.Old) { Move-Item -LiteralPath $item.Old -Destination $item.Target }
    }
    throw
}
if (-not (Test-Path -LiteralPath (Join-Path $ConfigDir 'config.toml'))) {
    [IO.Directory]::CreateDirectory($ConfigDir) > $null
    [IO.File]::WriteAllText((Join-Path $ConfigDir 'config.toml'), "default_layout = `"$DefaultLayout`"`n")
}
Write-Output "Installed $version in $InstallDir"
Write-Output "Backups: $backup (existing sessions were not stopped)"
Write-Output 'Run wmux in your terminal. Its daemon starts on demand; no logon startup was registered.'
Write-Output 'Use wmux legacy list / wmux legacy attach for preserved Go sessions.'
