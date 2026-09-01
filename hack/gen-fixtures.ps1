#!/usr/bin/env pwsh
# Regenerates testdata/psfixtures/ from the PowerShell implementation.
# Run from the repo root:  pwsh -NoProfile -File hack/gen-fixtures.ps1
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$out = Join-Path $root 'testdata/psfixtures'
New-Item -ItemType Directory -Path $out -Force | Out-Null
foreach ($m in 'Core','Scripts','Deps','Runner','Cron','Mcp') {
    Import-Module (Join-Path $root "src/$m.psm1") -Force -DisableNameChecking
}
# isolated app+data dir so nothing touches ~/.scriptorium. pwshBin is pinned
# to the exact interpreter running this generator (not just 'pwsh' resolved
# off PATH) so Start-StoRun's real run below spawns a real, known binary.
$appDir = Join-Path ([IO.Path]::GetTempPath()) "sto-fixtures-$(New-Guid)"
New-Item -ItemType Directory -Path $appDir -Force | Out-Null
$pwshExe = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
@{ dataDir = (Join-Path $appDir 'data'); pwshBin = $pwshExe } | ConvertTo-Json | Set-Content (Join-Path $appDir 'config.json')
Initialize-Sto -AppDir $appDir

$ic = [Globalization.CultureInfo]::InvariantCulture

# --- cron truth table -------------------------------------------------------
$exprs = @(
  '* * * * *','*/5 * * * *','*/10 * * * *','*/15 * * * *','0 * * * *',
  '30 2 * * *','0 0 * * *','0 9 * * 1-5','0 9 * * mon-fri','15 14 1 * *',
  '0 22 * * 1,3,5','5/15 * * * *','0 0 1 1 *','0 12 * * 0','0 12 * * 7',
  '0 0 29 2 *','0 8 15 * 3','*/7 3-6 * * *','1-5 * * * *','0 0 31 4 *',
  '@hourly','@daily','@midnight','@weekly','@monthly','@yearly','@annually','@reboot',
  '0 6,18 * * *','20,40 */2 * * sat,sun','0 0 */3 * *','45 23 28-31 * *'
)
$times = @(
  '2026-01-01T00:00:00','2026-02-28T23:59:00','2026-03-01T00:00:30',
  '2026-06-15T14:30:45','2026-07-03T14:30:45','2026-08-31T23:00:00',
  '2026-12-31T23:59:59','2027-01-01T00:00:00','2028-02-28T12:00:00',
  '2028-02-29T12:00:00','2026-09-01T09:59:59','2026-11-23T04:05:06'
)
$rows = foreach ($e in $exprs) { foreach ($t in $times) {
    $from = [datetime]::ParseExact($t, "yyyy-MM-dd'T'HH:mm:ss", $ic)
    $n = Get-StoCronNext -Expression $e -From $from
    $p = Get-StoCronPrev -Expression $e -From $from
    '"{0}",{1},{2},{3}' -f $e, $t,
      $(if ($n) { $n.ToString("yyyy-MM-dd'T'HH:mm:ss", $ic) } else { '' }),
      $(if ($p) { $p.ToString("yyyy-MM-dd'T'HH:mm:ss", $ic) } else { '' })
} }
@('expression,from,next,prev') + $rows | Set-Content (Join-Path $out 'cron-truth.csv') -Encoding UTF8

# --- rounding ([Math]::Round banker's) --------------------------------------
$vals = @(0.05,0.15,0.25,0.35,0.45,1.05,1.15,2.5,3.5,-0.25,-1.15,0.449,0.451,99.95,100.05) +
        @(for ($i = 0; $i -lt 185; $i++) { [Math]::Round(($i * 7.3 + 0.017 * $i * $i) % 1000, 3) })
$lines = @('input,rounded') + @($vals | ForEach-Object {
    '{0},{1}' -f $_.ToString($ic), ([Math]::Round([double]$_, 1)).ToString($ic) })
$lines | Set-Content (Join-Path $out 'rounding.csv') -Encoding UTF8

# --- duration + relative-time format tables ---------------------------------
$secs = @(0,0.04,0.05,5.26,59.94,59.95,60,61,90,599,600,3599,3600,3725,7199,7200,86399,86400,90000,172800,604800)
@('seconds,formatted') + @($secs | ForEach-Object { '{0},{1}' -f $_.ToString($ic), (Format-StoDuration $_) }) |
    Set-Content (Join-Path $out 'duration-format.csv') -Encoding UTF8
@('seconds,formatted') + @($secs | ForEach-Object { '{0},{1}' -f $_.ToString($ic), (Format-StoRelativeTime $_) }) |
    Set-Content (Join-Path $out 'relative-time.csv') -Encoding UTF8

# --- display width (text base64-encoded) ------------------------------------
$texts = @('hello','', '日本語','🎉ok',"e$([char]0x0301)",'a　b','ｱｲｳ','👨‍👩‍👧‍👦','ﬀ','café','—dash—','┌─┐')
@('text_b64,width') + @($texts | ForEach-Object {
    '{0},{1}' -f [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($_)), (Get-StoDisplayWidth $_) }) |
    Set-Content (Join-Path $out 'display-width.csv') -Encoding UTF8

# --- .env corpus ------------------------------------------------------------
$envDir = Join-Path $out 'env-corpus'; New-Item -ItemType Directory -Path $envDir -Force | Out-Null
$corpus = [ordered]@{
  'basic.env'   = "A=1`nB=two words"
  'comments.env'= "# c`n`nA=1`n  # indented comment`nBAD_LINE`n=noval`nX=ok"
  'quotes.env'  = "A='quoted'`nB=`"dquoted`"`nC='unbalanced`nD=`"mixed'`nE=''"
  'equals.env'  = "A=x=y`nB = spaced `nC=trail  "
  'dupes.env'   = "K=first`nK=second"
}
$expected = [ordered]@{}
foreach ($kv in $corpus.GetEnumerator()) {
    $f = Join-Path $envDir $kv.Key
    $kv.Value | Set-Content $f -Encoding UTF8 -NoNewline
    $expected[$kv.Key] = Read-StoEnvFile $f
}
$expected | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $envDir 'expected.json') -Encoding UTF8

# --- config corpus + exact warning strings ----------------------------------
# Controller ruling: the corpus FILES keep dataDir: '~/x' verbatim (so the Go
# parser fixture matches real-world configs), but the config actually fed to
# Initialize-Sto for warning capture must never point dataDir at the real
# home dir — it gets a per-case temp dir instead. Same warning-triggering
# keys either way; only dataDir differs between the two written files.
$cfgDir = Join-Path $out 'config-corpus'; New-Item -ItemType Directory -Path $cfgDir -Force | Out-Null
$cases = [ordered]@{
  'valid.json'      = [ordered]@{ dataDir = '~/x'; historyDays = 14 }
  'unknown-key.json'= [ordered]@{ dataDir = '~/x'; notAKey = 1 }
  'bad-numeric.json'= [ordered]@{ dataDir = '~/x'; runTimeoutMinutes = 'lots' }
  'legacy-repo.json'= [ordered]@{ dataDir = '~/x'; scriptsRepo = 'https://github.com/org/ps-scripts'; branch = 'dev' }
}
$warnLines = foreach ($c in $cases.GetEnumerator()) {
    $dir = Join-Path ([IO.Path]::GetTempPath()) "sto-cfg-$(New-Guid)"
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $c.Value | ConvertTo-Json | Set-Content (Join-Path $cfgDir $c.Key) -Encoding UTF8
    $probeCfg = [ordered]@{}
    foreach ($k in $c.Value.Keys) { $probeCfg[$k] = $c.Value[$k] }
    $probeCfg['dataDir'] = (Join-Path $dir 'data')   # never ~/x — stays under TEMP
    $probeCfg | ConvertTo-Json | Set-Content (Join-Path $dir 'config.json') -Encoding UTF8
    Initialize-Sto -AppDir $dir
    foreach ($w in (Get-StoConfigWarnings)) { "$($c.Key)`t$w" }
    Remove-Item $dir -Recurse -Force
}
$warnLines | Set-Content (Join-Path $cfgDir 'warnings.txt') -Encoding UTF8
Initialize-Sto -AppDir $appDir   # restore the fixture app dir

# --- history: one real run + hand-written era rows --------------------------
$sdir = Join-Path $appDir 'scriptdir'; New-Item -ItemType Directory -Path $sdir -Force | Out-Null
'Write-Output "fixture run"' | Set-Content (Join-Path $sdir 'fix.ps1')
$script = [pscustomobject]@{ Name='fixture-script'; Dir=$sdir; Entry=(Join-Path $sdir 'fix.ps1')
    Runtime='powershell'; Repo='fixrepo'; Args=@(); EnvFile=(Join-Path $sdir '.env')
    ModuleDir=(Join-Path $sdir 'mods'); TimeoutMinutes=$null }
$h = Start-StoRun -Script $script -Trigger 'manual'
$null = Invoke-StoRunToCompletion -Handle $h
$modern = Get-Content (Get-StoPaths).HistoryFile -Tail 1
$legacy = @(
  '{"event":"script_run","script":"old-a","trigger":"cron","status":"success","exitCode":0,"startedAt":"2026-05-01T12:00:00.000Z","finishedAt":"2026-05-01T12:01:00.000Z","durationSec":60.0,"host":"h","logFile":"/tmp/x.log"}'
  '{"event":"script_run","script":"old-b","runtime":"python","trigger":"manual","status":"failure","exitCode":1,"startedAt":"2026-06-01T12:00:00.000Z","finishedAt":"2026-06-01T12:00:05.000Z","durationSec":5.0,"host":"h","resources":{"cpuAvgPercent":1.0,"cpuMaxPercent":2.0,"memAvgMb":10.0,"memMaxMb":11.0,"samples":5},"logFile":"/tmp/y.log"}'
  '{"event":"script_run","script":"old-c","runtime":"powershell","repo":"r","trigger":"cron","status":"skipped","success":false,"exitCode":-1,"startedAt":"2026-07-01T12:00:00.000Z","finishedAt":"2026-07-01T12:00:00.000Z","durationSec":0.0,"host":"h","logFile":null}'
  'not json at all'
  '{"event":"script_run","script":"old-d","trigger":"mcp","status":"timeout","exitCode":-1,"startedAt":"2026-08-01T07:00:00+02:00","finishedAt":"2026-08-01T07:30:00+02:00","durationSec":1800.0,"host":"h","logFile":"/tmp/z.log"}'
)
@($legacy[0..2]) + @($modern) + @($legacy[3..4]) | Set-Content (Join-Path $out 'history-mixed.jsonl') -Encoding UTF8
# webhook payload golden = the modern row plus a log field, exactly as Send-StoWebhook receives it
$row = $modern | ConvertFrom-Json
$payload = [ordered]@{}
foreach ($p in $row.PSObject.Properties) { $payload[$p.Name] = $p.Value }
$payload['log'] = Get-StoLogTail -LogFile $row.logFile -TailKb 64
$payload | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $out 'webhook-payload.json') -Encoding UTF8

# --- crontab fixtures (parse-only — never touches the real crontab) ---------
$cronDir = Join-Path $out 'crontab'; New-Item -ItemType Directory -Path $cronDir -Force | Out-Null
$app = (Get-StoAppDir); $logs = (Get-StoPaths).LogsDir
$mk = { param($markStart, $markEnd) @(
    'MAILTO=someone@example.com'
    '15 3 * * * /usr/local/bin/certbot renew'
    $markStart
    "*/10 * * * * cd '$app' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'fast-job' --cron >> '$logs/cron-fast-job.log' 2>&1"
    "0 2 * * * cd '$app' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'nightly' --cron >> '$logs/cron-nightly.log' 2>&1"
    $markEnd
    '# trailing user comment'
) }
(& $mk '# >>> scriptorium managed block — do not edit by hand >>>' '# <<< scriptorium managed block <<<') -join "`n" |
    Set-Content (Join-Path $cronDir 'current.txt') -Encoding UTF8
(& $mk '# >>> psscripts managed block — do not edit by hand >>>' '# <<< psscripts managed block <<<') -join "`n" |
    Set-Content (Join-Path $cronDir 'legacy.txt') -Encoding UTF8
@('0 1 * * * /bin/true',
  '# >>> scriptorium managed block — do not edit by hand >>>',
  "5 5 * * * cd '$app' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'solo' --cron >> '$logs/cron-solo.log' 2>&1",
  '# <<< scriptorium managed block <<<',
  '30 6 * * 1 /usr/bin/backup-home') -join "`n" |
    Set-Content (Join-Path $cronDir 'interleaved.txt') -Encoding UTF8
# expected parse: run the REAL parser logic over each fixture (in-process —
# never touches the real crontab)
$parsed = [ordered]@{}
foreach ($f in 'current.txt','legacy.txt','interleaved.txt') {
    $lines = Get-Content (Join-Path $cronDir $f)
    $map = @{}
    $inBlock = $false
    foreach ($line in $lines) {
        if ($line -match '^# >>> (scriptorium|psscripts) managed block') { $inBlock = $true; continue }
        if ($line -match '^# <<< (scriptorium|psscripts) managed block') { $inBlock = $false; continue }
        if (-not $inBlock) { continue }
        if ($line -match "--run '([^']+)'" ) {
            $name = $Matches[1]
            if ($line -match '^(@\S+|(?:\S+\s+){4}\S+)\s+cd ') { $map[$name] = $Matches[1].Trim() }
        }
    }
    $parsed[$f] = $map
}
$parsed | ConvertTo-Json -Depth 3 | Set-Content (Join-Path $cronDir 'expected-schedules.json') -Encoding UTF8

# --- MCP request/response pairs (pure dispatch, no sockets) -----------------
$mcpDir = Join-Path $out 'mcp'; New-Item -ItemType Directory -Path $mcpDir -Force | Out-Null
$record = { param([string]$name, [string]$body)
    $r = Invoke-StoMcpRequest -Body $body -Authorized $true
    $body | Set-Content (Join-Path $mcpDir "$name.request.json") -Encoding UTF8
    [ordered]@{ statusCode = $r.StatusCode; json = $r.Json } | ConvertTo-Json -Depth 3 |
        Set-Content (Join-Path $mcpDir "$name.response.json") -Encoding UTF8
}
& $record '01-initialize'   '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}'
& $record '02-ping'         '{"jsonrpc":"2.0","id":2,"method":"ping"}'
& $record '03-tools-list'   '{"jsonrpc":"2.0","id":3,"method":"tools/list"}'
& $record '04-notification' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
& $record '05-parse-error'  'this is not json'
& $record '06-unknown-method' '{"jsonrpc":"2.0","id":6,"method":"nope"}'
& $record '07-unknown-tool' '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"nope"}}'
& $record '08-get-history'  '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"get_history","arguments":{}}}'
& $record '09-bad-logid'    '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"get_run_log","arguments":{"log_id":"../etc/passwd"}}}'

Remove-Item $appDir -Recurse -Force
Write-Host "fixtures written to $out"
