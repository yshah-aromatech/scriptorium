#!/usr/bin/env pwsh
# psscripts.ps1 — PowerShell Scripts TUI entry point
#
#   psscripts                 launch the TUI
#   psscripts --list          list discovered scripts
#   psscripts --run <name>    run one script headless (full pipeline)
#   psscripts --run <name> --args "<extra args>"   pass extra arguments
#   psscripts --run <name> --cron     same, marks the run as cron-triggered
#   psscripts --sync          sync the scripts repo and exit
#   psscripts --history [name]        print recent runs (optionally one script)
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
$extraArgsRaw = ''
$isCron = $false
$listOnly = $false
$syncOnly = $false
$historyOnly = $false
$historyName = $null
$showHelp = $false
for ($i = 0; $i -lt $args.Count; $i++) {
    switch ($args[$i]) {
        '--run' { $runName = $args[$i + 1]; $i++ }
        '--args' { $extraArgsRaw = "$($args[$i + 1])"; $i++ }
        '--cron' { $isCron = $true }
        '--list' { $listOnly = $true }
        '--sync' { $syncOnly = $true }
        '--history' {
            $historyOnly = $true
            if ($i + 1 -lt $args.Count -and "$($args[$i + 1])" -notlike '--*') { $historyName = "$($args[$i + 1])"; $i++ }
        }
        { $_ -in '--help', '-h' } { $showHelp = $true }
    }
}

foreach ($w in (Get-PssConfigWarnings)) { Write-Warning $w }

if ($showHelp) {
    Get-Content $PSCommandPath | Select-Object -Skip 1 -First 11 | ForEach-Object { $_ -replace '^#\s?', '' }
    exit 0
}

if ($listOnly) {
    $statuses = Get-PssLastStatuses
    $schedules = Get-PssSchedules
    foreach ($s in (Get-PssScripts)) {
        $st = if ($statuses.ContainsKey($s.Name)) { $statuses[$s.Name].Status } else { 'never run' }
        $sched = if ($schedules.ContainsKey($s.Name)) { "  [$($schedules[$s.Name])]" } else { '' }
        '{0,-30} {1,-10}{2}' -f $s.Name, $st, $sched
    }
    exit 0
}

if ($syncOnly) {
    $ok = Sync-PssRepo -OnOutput { param($line) Write-Host $line }
    exit $(if ($ok) { 0 } else { 1 })
}

if ($historyOnly) {
    $items = @(Get-PssHistory -Last 200)
    if ($historyName) { $items = @($items | Where-Object { "$($_.script)" -eq $historyName }) }
    if ($items.Count -eq 0) { Write-Host 'no runs recorded'; exit 0 }
    foreach ($h in $items) {
        $when = "$($h.startedAt)" -replace 'T', ' ' -replace '\.\d+Z$', 'Z'
        '{0}  {1,-9} {2,-25} {3,8}  cpu {4,5}%  mem {5,7}MB  [{6}]' -f
        $when, $h.status, $h.script, (Format-PssDuration ([double]$h.durationSec)),
        $h.resources.cpuMaxPercent, $h.resources.memMaxMb, $h.trigger
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
        Write-Host "installing missing modules: $(($missing | ForEach-Object Display) -join ', ')"
        $cfg = Get-PssConfig
        $cmd = Get-PssInstallCommand -Script $target -Modules $missing
        & ([string]$cfg.pwshBin) -NoProfile -NonInteractive -Command $cmd
    }

    $trigger = if ($isCron) { 'cron' } else { 'manual' }
    $extraArgs = @(Split-PssArguments $extraArgsRaw)
    $handle = Start-PssRun -Script $target -Trigger $trigger -ExtraArgs $extraArgs
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
    exit $(if ($result.success) { 0 } elseif ($result.status -eq 'skipped') { 3 } else { 1 })
}

# ---------------------------------------------------------------------------
# default: TUI
# ---------------------------------------------------------------------------
Start-PssTui
