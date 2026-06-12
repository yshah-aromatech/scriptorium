# Runner.psm1 — non-blocking script execution with live output streaming,
# process-tree resource monitoring, timeouts, per-run log files, run history
# and n8n webhook reporting.
#
# A "run handle" is a mutable hashtable the caller polls with Update-PssRun
# each tick; when the process exits, Complete-PssRun finalizes history +
# webhook. The same machinery (Kind='task') runs system tasks (module
# installs, updates) with streaming but without history/webhook.

$script:ClkTck = 100
try { $script:ClkTck = [int](& getconf CLK_TCK 2>$null) } catch { }
if (-not $script:ClkTck) { $script:ClkTck = 100 }
$script:PageSize = 4096
try { $script:PageSize = [int](& getconf PAGESIZE 2>$null) } catch { }
if (-not $script:PageSize) { $script:PageSize = 4096 }

# ---------------------------------------------------------------------------
# Start a script run (Kind='run') — full pipeline
# ---------------------------------------------------------------------------
function Start-PssRun {
    param(
        [Parameter(Mandatory)]$Script,
        [string]$Trigger = 'manual',
        [string[]]$ExtraArgs = @()
    )
    $cfg = Get-PssConfig
    $paths = Get-PssPaths

    $stamp = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH-mm-ss-fffZ')
    $logFile = Join-Path $paths.LogsDir "$($Script.Name)-$stamp.log"

    $psi = [System.Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = [string]$cfg.pwshBin
    [void]$psi.ArgumentList.Add('-NoProfile')
    [void]$psi.ArgumentList.Add('-NonInteractive')
    [void]$psi.ArgumentList.Add('-File')
    [void]$psi.ArgumentList.Add($Script.Entry)
    foreach ($a in @($Script.Args) + @($ExtraArgs)) { if ("$a") { [void]$psi.ArgumentList.Add("$a") } }
    $psi.WorkingDirectory = $Script.Dir
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true

    # per-script .env -> child environment (and secret registration)
    foreach ($kv in (Read-PssEnvFile $Script.EnvFile).GetEnumerator()) {
        $psi.Environment[$kv.Key] = $kv.Value
        Register-PssSecret -Name $kv.Key -Value $kv.Value
    }
    # per-script module dir gets first crack at module resolution
    $sep = [IO.Path]::PathSeparator
    $psi.Environment['PSModulePath'] = "$($Script.ModuleDir)$sep$($env:PSModulePath)"

    New-PssHandle -Psi $psi -Kind 'run' -Name $Script.Name -Trigger $Trigger -LogFile $logFile
}

# ---------------------------------------------------------------------------
# Start a system task (Kind='task') — streamed, no history/webhook
# ---------------------------------------------------------------------------
function Start-PssTask {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$FileName,
        [string[]]$Arguments = @()
    )
    $psi = [System.Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = $FileName
    foreach ($a in $Arguments) { [void]$psi.ArgumentList.Add($a) }
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    New-PssHandle -Psi $psi -Kind 'task' -Name $Name -Trigger 'manual' -LogFile $null
}

function New-PssHandle {
    param($Psi, [string]$Kind, [string]$Name, [string]$Trigger, [string]$LogFile)

    $proc = [System.Diagnostics.Process]::new()
    $proc.StartInfo = $Psi
    $handle = @{
        Kind        = $Kind
        Name        = $Name
        Trigger     = $Trigger
        Process     = $proc
        LogFile     = $LogFile
        LogWriter   = $null
        StartedAt   = (Get-Date).ToUniversalTime()
        FinishedAt  = $null
        Status      = 'running'        # running|success|failure|killed|timeout
        ExitCode    = $null
        OutTask     = $null
        ErrTask     = $null
        # resources
        LastSample  = $null            # @{ Time; Jiffies }
        CpuSum      = 0.0; CpuMax = 0.0
        MemSum      = 0.0; MemMax = 0.0
        Samples     = 0
        Completed   = $false
    }

    try {
        [void]$proc.Start()
    } catch {
        $handle.Status = 'failure'
        $handle.ExitCode = -1
        $handle.FinishedAt = (Get-Date).ToUniversalTime()
        $handle.StartError = $_.Exception.Message
        return $handle
    }
    if ($LogFile) {
        $handle.LogWriter = [IO.StreamWriter]::new($LogFile, $false, [Text.Encoding]::UTF8)
        $handle.LogWriter.AutoFlush = $true
    }
    $handle.OutTask = $proc.StandardOutput.ReadLineAsync()
    $handle.ErrTask = $proc.StandardError.ReadLineAsync()
    $handle
}

# ---------------------------------------------------------------------------
# Poll: drain available output lines (redacted), enforce timeout.
# Returns the new output lines from this tick.
# ---------------------------------------------------------------------------
function Update-PssRun {
    param([Parameter(Mandatory)]$Handle)

    $lines = [System.Collections.Generic.List[string]]::new()
    if ($Handle.ContainsKey('StartError') -and $Handle.StartError) {
        if (-not $Handle.StartErrorEmitted) {
            $lines.Add("failed to start: $($Handle.StartError)")
            $Handle.StartErrorEmitted = $true
        }
        return $lines
    }
    $proc = $Handle.Process

    foreach ($stream in 'Out', 'Err') {
        $taskKey = "${stream}Task"
        $reader = if ($stream -eq 'Out') { $proc.StandardOutput } else { $proc.StandardError }
        $guard = 0
        while ($Handle[$taskKey] -and $Handle[$taskKey].IsCompleted -and $guard -lt 500) {
            $guard++
            $line = $null
            try { $line = $Handle[$taskKey].GetAwaiter().GetResult() } catch { $line = $null }
            if ($null -eq $line) { $Handle[$taskKey] = $null; break }   # stream closed
            $line = Hide-PssSecret $line
            $lines.Add($line)
            if ($Handle.LogWriter) { $Handle.LogWriter.WriteLine($line) }
            $Handle[$taskKey] = $reader.ReadLineAsync()
        }
    }

    # timeout
    $cfg = Get-PssConfig
    $timeoutMin = [double]$cfg.runTimeoutMinutes
    if ($Handle.Kind -eq 'run' -and $timeoutMin -gt 0 -and $Handle.Status -eq 'running' -and -not $proc.HasExited) {
        $elapsed = ((Get-Date).ToUniversalTime() - $Handle.StartedAt).TotalMinutes
        if ($elapsed -ge $timeoutMin) {
            $lines.Add("run exceeded ${timeoutMin}min timeout — killing")
            Stop-PssRun -Handle $Handle -Reason 'timeout'
        }
    }

    $lines
}

function Test-PssRunFinished {
    param([Parameter(Mandatory)]$Handle)
    if ($Handle.ContainsKey('StartError') -and $Handle.StartError) { return $true }
    $Handle.Process.HasExited -and ($null -eq $Handle.OutTask) -and ($null -eq $Handle.ErrTask)
}

# ---------------------------------------------------------------------------
# Resource sampling — whole process tree via /proc (CPU% and RSS MB)
# ---------------------------------------------------------------------------
function Get-PssTreePids {
    param([int]$RootPid)
    $children = @{}
    foreach ($dir in [IO.Directory]::EnumerateDirectories('/proc')) {
        $pidStr = [IO.Path]::GetFileName($dir)
        if ($pidStr -notmatch '^\d+$') { continue }
        try {
            $stat = [IO.File]::ReadAllText("$dir/stat")
            $after = $stat.Substring($stat.LastIndexOf(')') + 2)
            $ppid = [int]($after -split ' ')[1]
            if (-not $children.ContainsKey($ppid)) { $children[$ppid] = [System.Collections.Generic.List[int]]::new() }
            $children[$ppid].Add([int]$pidStr)
        } catch { }
    }
    $result = [System.Collections.Generic.List[int]]::new()
    $stack = [System.Collections.Generic.Stack[int]]::new()
    $stack.Push($RootPid)
    while ($stack.Count -gt 0) {
        $p = $stack.Pop()
        $result.Add($p)
        if ($children.ContainsKey($p)) { foreach ($c in $children[$p]) { $stack.Push($c) } }
    }
    $result
}

function Measure-PssResources {
    param([Parameter(Mandatory)]$Handle)
    if ($Handle.ContainsKey('StartError') -and $Handle.StartError) { return }
    $proc = $Handle.Process
    if ($proc.HasExited) { return }

    $now = Get-Date
    $jiffies = [long]0
    $rssBytes = [long]0
    foreach ($p in (Get-PssTreePids -RootPid $proc.Id)) {
        try {
            $stat = [IO.File]::ReadAllText("/proc/$p/stat")
            $after = ($stat.Substring($stat.LastIndexOf(')') + 2)) -split ' '
            $jiffies += [long]$after[11] + [long]$after[12]     # utime + stime
            $rssBytes += [long]$after[21] * $script:PageSize    # rss pages
        } catch { }
    }

    $memMb = $rssBytes / 1MB
    if ($Handle.LastSample) {
        $dt = ($now - $Handle.LastSample.Time).TotalSeconds
        if ($dt -gt 0.2) {
            $cpu = (($jiffies - $Handle.LastSample.Jiffies) / $script:ClkTck) / $dt * 100.0
            if ($cpu -lt 0) { $cpu = 0 }
            $Handle.CpuSum += $cpu; $Handle.MemSum += $memMb
            if ($cpu -gt $Handle.CpuMax) { $Handle.CpuMax = $cpu }
            if ($memMb -gt $Handle.MemMax) { $Handle.MemMax = $memMb }
            $Handle.Samples++
            $Handle.CpuNow = $cpu; $Handle.MemNow = $memMb
            $Handle.LastSample = @{ Time = $now; Jiffies = $jiffies }
        }
    } else {
        $Handle.LastSample = @{ Time = $now; Jiffies = $jiffies }
    }
}

# ---------------------------------------------------------------------------
# Kill the whole process tree (SIGTERM, then SIGKILL after a grace period)
# ---------------------------------------------------------------------------
function Stop-PssRun {
    param([Parameter(Mandatory)]$Handle, [string]$Reason = 'killed')
    $proc = $Handle.Process
    if ($proc.HasExited) { return }
    $Handle.Status = $Reason
    $pids = Get-PssTreePids -RootPid $proc.Id
    foreach ($p in $pids) { & /bin/kill -TERM $p 2>$null }
    $deadline = (Get-Date).AddSeconds(3)
    while (-not $proc.HasExited -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 100 }
    if (-not $proc.HasExited) {
        foreach ($p in (Get-PssTreePids -RootPid $proc.Id)) { & /bin/kill -KILL $p 2>$null }
        try { $proc.WaitForExit(2000) | Out-Null } catch { }
    }
}

# ---------------------------------------------------------------------------
# Finalize: status, history line, webhook. Returns the result object.
# ---------------------------------------------------------------------------
function Complete-PssRun {
    param([Parameter(Mandatory)]$Handle)
    if ($Handle.Completed) { return $Handle.Result }
    $Handle.Completed = $true
    $cfg = Get-PssConfig
    $paths = Get-PssPaths

    $Handle.FinishedAt = (Get-Date).ToUniversalTime()
    if ($Handle.ContainsKey('StartError') -and $Handle.StartError) {
        $Handle.ExitCode = -1
    } else {
        try { $Handle.ExitCode = $Handle.Process.ExitCode } catch { $Handle.ExitCode = -1 }
    }
    if ($Handle.Status -eq 'running') {
        $Handle.Status = if ($Handle.ExitCode -eq 0) { 'success' } else { 'failure' }
    }
    if ($Handle.LogWriter) { try { $Handle.LogWriter.Dispose() } catch { } ; $Handle.LogWriter = $null }

    $duration = [Math]::Round(($Handle.FinishedAt - $Handle.StartedAt).TotalSeconds, 1)
    $n = [Math]::Max(1, $Handle.Samples)
    $resources = [ordered]@{
        cpuAvgPercent = [Math]::Round($Handle.CpuSum / $n, 1)
        cpuMaxPercent = [Math]::Round($Handle.CpuMax, 1)
        memAvgMb      = [Math]::Round($Handle.MemSum / $n, 1)
        memMaxMb      = [Math]::Round($Handle.MemMax, 1)
        samples       = $Handle.Samples
    }

    $result = [ordered]@{
        event       = 'script_run'
        script      = $Handle.Name
        trigger     = $Handle.Trigger
        status      = $Handle.Status
        success     = ($Handle.Status -eq 'success')
        exitCode    = $Handle.ExitCode
        startedAt   = $Handle.StartedAt.ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
        finishedAt  = $Handle.FinishedAt.ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
        durationSec = $duration
        host        = [Environment]::MachineName
        resources   = $resources
        logFile     = $Handle.LogFile
    }
    $Handle.Result = $result
    if ($Handle.Kind -ne 'run') { return $result }

    # history (without the log payload)
    try {
        ($result | ConvertTo-Json -Depth 6 -Compress) |
            Add-Content -Path $paths.HistoryFile -Encoding UTF8
    } catch { }

    # webhook gets a log tail
    $payload = [ordered]@{}
    foreach ($k in $result.Keys) { $payload[$k] = $result[$k] }
    $payload['log'] = Get-PssLogTail -LogFile $Handle.LogFile -TailKb ([int]$cfg.logTailKb)
    [void](Send-PssWebhook -Payload $payload)
    $result
}

function Get-PssLogTail {
    param([string]$LogFile, [int]$TailKb = 64)
    if (-not $LogFile -or -not (Test-Path $LogFile)) { return '' }
    try {
        $fs = [IO.File]::Open($LogFile, 'Open', 'Read', 'ReadWrite')
        try {
            $max = $TailKb * 1KB
            if ($fs.Length -gt $max) { $fs.Seek(-$max, 'End') | Out-Null }
            $sr = [IO.StreamReader]::new($fs)
            return $sr.ReadToEnd()
        } finally { $fs.Dispose() }
    } catch { return '' }
}

# ---------------------------------------------------------------------------
# n8n webhook
# ---------------------------------------------------------------------------
function Send-PssWebhook {
    param([Parameter(Mandatory)]$Payload)
    $cfg = Get-PssConfig
    $url = if ($env:N8N_WEBHOOK_URL) { $env:N8N_WEBHOOK_URL } else { [string]$cfg.n8nWebhookUrl }
    if (-not $url) { return $false }
    try {
        $body = $Payload | ConvertTo-Json -Depth 6
        Invoke-RestMethod -Method Post -Uri $url -Body $body -ContentType 'application/json' -TimeoutSec 15 | Out-Null
        $true
    } catch {
        $false
    }
}

function Send-PssWebhookTest {
    $payload = [ordered]@{
        event = 'test'
        host  = [Environment]::MachineName
        at    = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
    }
    Send-PssWebhook -Payload $payload
}

# ---------------------------------------------------------------------------
# Run history
# ---------------------------------------------------------------------------
function Get-PssHistory {
    param([int]$Last = 100)
    $paths = Get-PssPaths
    if (-not (Test-Path $paths.HistoryFile)) { return @() }
    $lines = Get-Content $paths.HistoryFile -Tail $Last -ErrorAction SilentlyContinue
    $items = foreach ($l in $lines) {
        try { $l | ConvertFrom-Json } catch { }
    }
    @($items)
}

function Get-PssLastStatuses {
    # script name -> status of its most recent run
    $map = @{}
    foreach ($h in (Get-PssHistory -Last 500)) {
        if ($h -and $h.script) { $map[$h.script] = $h.status }
    }
    $map
}

Export-ModuleMember -Function Start-PssRun, Start-PssTask, Update-PssRun, Test-PssRunFinished,
Measure-PssResources, Stop-PssRun, Complete-PssRun, Send-PssWebhook, Send-PssWebhookTest,
Get-PssHistory, Get-PssLastStatuses, Get-PssLogTail
