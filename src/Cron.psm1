# Cron.psm1 — cron scheduling via a managed block in the user crontab, plus
# natural-language -> cron conversion through OpenRouter.

$script:BlockStart = '# >>> psscripts managed block — do not edit by hand >>>'
$script:BlockEnd = '# <<< psscripts managed block <<<'

function Get-PssCrontabLines {
    try {
        $out = & crontab -l 2>$null
        if ($LASTEXITCODE -ne 0 -or $null -eq $out) { return @() }
        @($out)
    } catch { @() }
}

# script name -> cron expression
function Get-PssSchedules {
    $map = @{}
    $inBlock = $false
    foreach ($line in (Get-PssCrontabLines)) {
        if ($line -eq $script:BlockStart) { $inBlock = $true; continue }
        if ($line -eq $script:BlockEnd) { $inBlock = $false; continue }
        if (-not $inBlock) { continue }
        if ($line -match "--run '([^']+)'") {
            $name = $Matches[1]
            $expr = if ($line -match '^(@\S+|(?:\S+\s+){4}\S+)\s+cd ') { $Matches[1].Trim() } else { '' }
            if ($expr) { $map[$name] = $expr }
        }
    }
    $map
}

function Save-PssSchedules {
    param([Parameter(Mandatory)][hashtable]$Schedules)
    $cfg = Get-PssConfig
    $paths = Get-PssPaths
    $appDir = Get-PssAppDir
    $pwshBin = [string]$cfg.pwshBin

    # everything outside the managed block is preserved untouched
    $kept = [System.Collections.Generic.List[string]]::new()
    $inBlock = $false
    foreach ($line in (Get-PssCrontabLines)) {
        if ($line -eq $script:BlockStart) { $inBlock = $true; continue }
        if ($line -eq $script:BlockEnd) { $inBlock = $false; continue }
        if (-not $inBlock) { $kept.Add($line) }
    }

    $new = [System.Collections.Generic.List[string]]::new()
    $new.AddRange($kept)
    if ($Schedules.Count -gt 0) {
        $new.Add($script:BlockStart)
        foreach ($name in ($Schedules.Keys | Sort-Object)) {
            $expr = $Schedules[$name]
            $log = Join-Path $paths.LogsDir "cron-$name.log"
            $new.Add("$expr cd '$appDir' && '$pwshBin' -NoProfile -File psscripts.ps1 --run '$name' --cron >> '$log' 2>&1")
        }
        $new.Add($script:BlockEnd)
    }

    $text = ($new -join "`n")
    if ($text) { $text += "`n" }
    $text | & crontab - 2>&1 | Out-Null
    $LASTEXITCODE -eq 0
}

function Set-PssSchedule {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Expression)
    $schedules = Get-PssSchedules
    $schedules[$Name] = $Expression
    Save-PssSchedules -Schedules $schedules
}

function Remove-PssSchedule {
    param([Parameter(Mandatory)][string]$Name)
    $schedules = Get-PssSchedules
    if ($schedules.ContainsKey($Name)) { $schedules.Remove($Name) }
    Save-PssSchedules -Schedules $schedules
}

# ---------------------------------------------------------------------------
# Validation + natural language conversion
# ---------------------------------------------------------------------------
function Test-PssCronExpression {
    param([string]$Expression)
    $e = $Expression.Trim()
    if ($e -match '^@(hourly|daily|weekly|monthly|yearly|annually|reboot|midnight)$') { return $true }
    $fields = $e -split '\s+'
    if ($fields.Count -ne 5) { return $false }
    foreach ($f in $fields) {
        if ($f -notmatch '^[\d\*/,\-A-Za-z]+$') { return $false }
    }
    $true
}

# Returns @{ Expression; Source = 'literal'|'ai'; Error }
function Convert-PssToCron {
    param([Parameter(Mandatory)][string]$Text)
    $t = $Text.Trim()
    if (Test-PssCronExpression $t) {
        return @{ Expression = $t; Source = 'literal'; Error = $null }
    }
    $apiKey = $env:OPENROUTER_API_KEY
    if (-not $apiKey) {
        return @{ Expression = $null; Source = 'ai'; Error = 'not a cron expression, and OPENROUTER_API_KEY is not set for natural-language conversion' }
    }
    $cfg = Get-PssConfig
    try {
        $body = @{
            model    = [string]$cfg.openRouterModel
            messages = @(
                @{ role = 'system'; content = 'Convert the user''s scheduling request into a single standard 5-field cron expression. Reply with ONLY the cron expression, nothing else.' },
                @{ role = 'user'; content = $t }
            )
        } | ConvertTo-Json -Depth 5
        $resp = Invoke-RestMethod -Method Post -Uri 'https://openrouter.ai/api/v1/chat/completions' `
            -Headers @{ Authorization = "Bearer $apiKey" } -ContentType 'application/json' `
            -Body $body -TimeoutSec 30
        $expr = "$($resp.choices[0].message.content)".Trim() -replace '`', ''
        if (Test-PssCronExpression $expr) {
            return @{ Expression = $expr; Source = 'ai'; Error = $null }
        }
        return @{ Expression = $null; Source = 'ai'; Error = "model returned something that isn't a cron expression: $expr" }
    } catch {
        return @{ Expression = $null; Source = 'ai'; Error = "OpenRouter request failed: $($_.Exception.Message)" }
    }
}

Export-ModuleMember -Function Get-PssSchedules, Set-PssSchedule, Remove-PssSchedule,
Test-PssCronExpression, Convert-PssToCron
