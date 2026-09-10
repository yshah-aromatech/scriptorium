param([string]$Root, [string]$Path, [string]$Operation)
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $Root 'src/Core.psm1') -Force -DisableNameChecking
Import-Module (Join-Path $Root 'src/Runner.psm1') -Force -DisableNameChecking
& (Get-Module Core) {
    param($file)
    $script:Paths = @{ HistoryFile = $file; DataDir = (Split-Path $file); LogsDir = (Join-Path (Split-Path $file) 'logs') }
    $script:Config = @{ historyDays = 30; historyMaxLines = 1; logRetentionDays = 0; logTailKb = 1; n8nWebhookUrl = '' }
} $Path
# Warm the native type before announcing readiness, so compilation cannot
# consume the parent's contention-check interval.
$warmup = Open-StoHistoryLock -Path "$Path.warmup"
$warmup.Dispose()
[Console]::WriteLine('ready')
if ($Operation -eq 'ps-prune') {
    Clear-StoOldData -Force
} else {
    $handle = @{
        Completed = $false; Kind = 'run'; Name = 'new'; Runtime = 'python'; Trigger = 'manual'
        StartedAt = [datetime]::UtcNow; Status = 'running'; Process = @{ ExitCode = 0 }
        Samples = 0; CpuSum = 0; CpuMax = 0; MemSum = 0; MemMax = 0; LogFile = $null
    }
    $null = Complete-StoRun -Handle $handle
}
