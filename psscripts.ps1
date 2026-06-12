#!/usr/bin/env pwsh
# psscripts.ps1 — PowerShell Scripts TUI entry point
#
#   psscripts                 launch the TUI
#   psscripts --list          list discovered scripts
#   psscripts --run <name>    run one script headless (full pipeline)
#   psscripts --run <name> --cron     same, marks the run as cron-triggered
#   psscripts --help

$ErrorActionPreference = 'Stop'
$appDir = $PSScriptRoot

foreach ($m in 'Core', 'Scripts', 'Deps', 'Runner', 'Cron', 'Tui') {
    Import-Module (Join-Path $appDir "src/$m.psm1") -Force -Global -DisableNameChecking
}

Initialize-Pss -AppDir $appDir

# ---------------------------------------------------------------------------
# argument parsing
# ---------------------------------------------------------------------------
$runName = $null
$isCron = $false
$listOnly = $false
$showHelp = $false
for ($i = 0; $i -lt $args.Count; $i++) {
    switch ($args[$i]) {
        '--run' { $runName = $args[$i + 1]; $i++ }
        '--cron' { $isCron = $true }
        '--list' { $listOnly = $true }
        { $_ -in '--help', '-h' } { $showHelp = $true }
    }
}

if ($showHelp) {
    Get-Content $PSCommandPath | Select-Object -Skip 1 -First 8 | ForEach-Object { $_ -replace '^#\s?', '' }
    exit 0
}

if ($listOnly) {
    $statuses = Get-PssLastStatuses
    $schedules = Get-PssSchedules
    foreach ($s in (Get-PssScripts)) {
        $st = if ($statuses.ContainsKey($s.Name)) { $statuses[$s.Name] } else { 'never run' }
        $sched = if ($schedules.ContainsKey($s.Name)) { "  [$($schedules[$s.Name])]" } else { '' }
        '{0,-30} {1,-10}{2}' -f $s.Name, $st, $sched
    }
    exit 0
}

# ---------------------------------------------------------------------------
# headless run: full pipeline, missing modules auto-installed, no prompts
# ---------------------------------------------------------------------------
if ($runName) {
    $target = Get-PssScripts | Where-Object Name -eq $runName | Select-Object -First 1
    if (-not $target) {
        Write-Error "script '$runName' not found — run 'psscripts --list' (or sync first)"
        exit 2
    }

    $missing = @(Get-PssMissingDeps -Script $target)
    if ($missing.Count -gt 0) {
        Write-Host "installing missing modules: $($missing -join ', ')"
        $cfg = Get-PssConfig
        $cmd = Get-PssInstallCommand -Script $target -Modules $missing
        & ([string]$cfg.pwshBin) -NoProfile -NonInteractive -Command $cmd
    }

    $trigger = if ($isCron) { 'cron' } else { 'manual' }
    $handle = Start-PssRun -Script $target -Trigger $trigger
    $cfg = Get-PssConfig
    $lastSample = [datetime]::MinValue
    while (-not (Test-PssRunFinished -Handle $handle)) {
        foreach ($line in (Update-PssRun -Handle $handle)) { Write-Host $line }
        if (((Get-Date) - $lastSample).TotalMilliseconds -ge [int]$cfg.monitorIntervalMs) {
            Measure-PssResources -Handle $handle
            $lastSample = Get-Date
        }
        Start-Sleep -Milliseconds 50
    }
    foreach ($line in (Update-PssRun -Handle $handle)) { Write-Host $line }
    $result = Complete-PssRun -Handle $handle
    $r = $result.resources
    Write-Host ("-- {0}: {1} (exit {2}) in {3}s | cpu avg {4}% peak {5}% | mem avg {6}MB peak {7}MB" -f
        $result.script, $result.status, $result.exitCode, $result.durationSec,
        $r.cpuAvgPercent, $r.cpuMaxPercent, $r.memAvgMb, $r.memMaxMb)
    exit $(if ($result.success) { 0 } else { 1 })
}

# ---------------------------------------------------------------------------
# default: TUI
# ---------------------------------------------------------------------------
Start-PssTui
