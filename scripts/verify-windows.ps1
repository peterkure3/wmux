param([switch]$SkipBuild)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
function Check-Exit { if ($LASTEXITCODE -ne 0) { throw "Command failed: $LASTEXITCODE" } }
New-Item -ItemType Directory -Force build/verification | Out-Null
[System.IO.File]::WriteAllText('build/go.mod',"module wmux-build-output`ngo 1.26.6`n")
if (Test-Path target) { [System.IO.File]::WriteAllText('target/go.mod',"module wmux-rust-build-output`ngo 1.26.6`n") }
if (-not $SkipBuild) {
cargo fmt --all -- --check
Check-Exit
cargo clippy --workspace --all-targets --locked -- -D warnings
Check-Exit
cargo test --workspace --locked
Check-Exit
cargo build --workspace --release --bins --examples --locked
Check-Exit
}
$env:WMUX_HOME = Join-Path $root 'build/measure-windows'
New-Item -ItemType Directory -Force $env:WMUX_HOME | Out-Null
[System.IO.File]::WriteAllText((Join-Path $env:WMUX_HOME 'config.toml'), 'shell = ["powershell.exe", "-NoLogo", "-NoProfile"]')
$wmux = Join-Path $root 'target/release/wmux.exe'
try {
    # Windows PowerShell 5 wraps redirected native stderr (including the normal
    # startup notice) in NativeCommandError records. Judge native success by its
    # exit code; keep terminating errors for the rest of the verification script.
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $wmux daemon start } finally { $ErrorActionPreference = $savedPreference }
    Check-Exit
    & $wmux doctor | Out-File -Encoding utf8 build/verification/doctor-windows.txt
    Check-Exit
    & ./target/release/examples/measure.exe | Out-File -Encoding utf8 build/verification/performance-windows.json
    Check-Exit
    $resourceName = 'resource-measure-' + [guid]::NewGuid().ToString('N')
    & $wmux new $resourceName --detach | Out-Null
    Check-Exit
    $state = (& $wmux --json list) | ConvertFrom-Json
    $workspace = $state.workspaces.PSObject.Properties.Value | Where-Object name -eq $resourceName
    $process = Get-Process -Name wmuxd | Where-Object { $_.Path -eq (Join-Path $root 'target\release\wmuxd.exe') } | Select-Object -First 1
    if (-not $process) { throw 'Measurement daemon not found' }
    $cpuStart = $process.TotalProcessorTime.TotalSeconds
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    Start-Sleep -Seconds 2
    $process.Refresh()
    $idleCpu = 100 * ($process.TotalProcessorTime.TotalSeconds - $cpuStart) / $timer.Elapsed.TotalSeconds
    $idleMemory = $process.WorkingSet64
    $cpuStart = $process.TotalProcessorTime.TotalSeconds
    $timer.Restart()
    & $wmux pane send --pane $workspace.focused -w $resourceName --enter "1..20000 | ForEach-Object { 'WMUX_HEAVY_OUTPUT' * 8 }"
    Check-Exit
    Start-Sleep -Seconds 2
    $process.Refresh()
    [ordered]@{
        platform = 'windows-x86_64'
        scenario = 'one PowerShell pane, no UI subscriber, 20000 output lines'
        idle_daemon_cpu_one_core_percent = $idleCpu
        idle_daemon_rss_bytes = $idleMemory
        output_daemon_cpu_one_core_percent = 100 * ($process.TotalProcessorTime.TotalSeconds - $cpuStart) / $timer.Elapsed.TotalSeconds
        output_daemon_rss_bytes = $process.WorkingSet64
        sample_seconds = $timer.Elapsed.TotalSeconds
    } | ConvertTo-Json | Out-File -Encoding utf8 build/verification/resources-windows.json
} finally {
    & $wmux daemon stop --force
    Remove-Item Env:WMUX_HOME -ErrorAction SilentlyContinue
}

[System.IO.File]::WriteAllText('build/verification/windows-measurement-status.txt','passed')
