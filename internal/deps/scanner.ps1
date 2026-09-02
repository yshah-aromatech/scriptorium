# scanner.ps1 — standalone transcription of the PowerShell dependency
# pipeline (src/Deps.psm1: Get-StoScriptDeps, Get-StoInstalledModules,
# Test-StoDepSatisfied) plus the param() block reader (src/Scripts.psm1:
# Get-StoScriptParameters). Every function body below is copied verbatim
# from those modules — only the call sites (a $Script object built from argv
# instead of a discovered Script, and a JSON emit instead of returning
# objects) are new. [version] comparisons and AST-walking stay here,
# pwsh-native, on purpose (Go never reimplements either); the Go side
# (psscan.go) only orchestrates: invoke this script, parse its last stdout
# line as JSON.
#
# argv: <entry> <dir> <moduleDir> <loose>   (loose: "true"/"false")
# stdout: last line = {"deps":[...],"missing":[...],"params":[...]}
param(
    [Parameter(Mandatory)][string]$Entry,
    [Parameter(Mandatory)][string]$Dir,
    [Parameter(Mandatory)][string]$ModuleDir,
    [Parameter(Mandatory)][string]$Loose
)

$ErrorActionPreference = 'Stop'
$Script = [pscustomobject]@{
    Dir       = $Dir
    Entry     = $Entry
    ModuleDir = $ModuleDir
    Loose     = ($Loose -eq 'true')
}

# ---------------------------------------------------------------------------
# Verbatim from src/Deps.psm1
# ---------------------------------------------------------------------------

$script:BuiltinModules = @(
    'Microsoft.PowerShell.Archive', 'Microsoft.PowerShell.Core',
    'Microsoft.PowerShell.Diagnostics', 'Microsoft.PowerShell.Host',
    'Microsoft.PowerShell.Management', 'Microsoft.PowerShell.Security',
    'Microsoft.PowerShell.Utility', 'Microsoft.PowerShell.PSResourceGet',
    'PSReadLine', 'PackageManagement', 'PowerShellGet', 'ThreadJob',
    'CimCmdlets', 'PSDiagnostics', 'Microsoft.WSMan.Management'
)

$script:ModuleNameMap = @{
    'pester'  = 'Pester'
    'az'      = 'Az'
    'awstools' = 'AWS.Tools.Common'
    'awspowershell' = 'AWSPowerShell.NetCore'
    'sqlps'   = 'SqlServer'
}

function New-StoDep {
    param([string]$Name, [string]$RequiredVersion, [string]$MinimumVersion, [string]$MaximumVersion)
    $disp = $Name
    if ($RequiredVersion) { $disp = "$Name (=$RequiredVersion)" }
    elseif ($MinimumVersion -and $MaximumVersion) { $disp = "$Name (>=$MinimumVersion,<=$MaximumVersion)" }
    elseif ($MinimumVersion) { $disp = "$Name (>=$MinimumVersion)" }
    elseif ($MaximumVersion) { $disp = "$Name (<=$MaximumVersion)" }
    [pscustomobject]@{
        Name            = $Name
        RequiredVersion = $RequiredVersion
        MinimumVersion  = $MinimumVersion
        MaximumVersion  = $MaximumVersion
        Display         = $disp
    }
}

function Get-StoScriptDeps {
    param([Parameter(Mandatory)]$Script)

    # name(lower) -> version spec; a versioned mention wins over a bare one
    $deps = [System.Collections.Generic.Dictionary[string, object]]::new([StringComparer]::OrdinalIgnoreCase)
    $addDep = {
        param([string]$Name, [string]$Rv = '', [string]$MinV = '', [string]$MaxV = '')
        if (-not $Name) { return }
        $spec = @{ Name = $Name; Rv = $Rv; MinV = $MinV; MaxV = $MaxV }
        if (-not $deps.ContainsKey($Name) -or ($Rv -or $MinV -or $MaxV)) { $deps[$Name] = $spec }
    }
    $tokens = $null; $errors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($Script.Entry, [ref]$tokens, [ref]$errors)
    if (-not $ast) { return @() }

    # #Requires -Modules (ModuleSpecification: Version = minimum)
    if ($ast.ScriptRequirements -and $ast.ScriptRequirements.RequiredModules) {
        foreach ($m in $ast.ScriptRequirements.RequiredModules) {
            & $addDep $m.Name "$($m.RequiredVersion)" "$($m.Version)" "$($m.MaximumVersion)"
        }
    }

    # using module X
    if ($ast.UsingStatements) {
        foreach ($u in $ast.UsingStatements) {
            if ($u.UsingStatementKind -eq 'Module' -and $u.Name) { & $addDep $u.Name.Value }
        }
    }

    # Import-Module X / Import-Module -Name X
    $calls = $ast.FindAll({
            param($n)
            $n -is [System.Management.Automation.Language.CommandAst] -and
            $n.GetCommandName() -in @('Import-Module', 'ipmo')
        }, $true)
    # parameters that consume the following element as their value (so it must NOT
    # be mistaken for a module name). Includes Import-Module's own value parameters
    # plus the common parameters (and their aliases) — e.g. `-ErrorAction Stop`,
    # `-ErrorVariable e`, whose string values were being picked up as module names.
    $valueParams = @(
        'Function', 'Cmdlet', 'Variable', 'Alias', 'Prefix', 'MinimumVersion',
        'MaximumVersion', 'RequiredVersion', 'ArgumentList', 'Args', 'FullyQualifiedName',
        'Scope', 'PSSession', 'CimSession', 'CimResourceUri', 'CimNamespace',
        # common parameters that take a value
        'ErrorAction', 'ea', 'WarningAction', 'wa', 'InformationAction', 'infa',
        'ProgressAction', 'proga', 'ErrorVariable', 'ev', 'WarningVariable', 'wv',
        'InformationVariable', 'iv', 'OutVariable', 'ov', 'OutBuffer', 'ob',
        'PipelineVariable', 'pv'
    )
    foreach ($call in $calls) {
        $elements = $call.CommandElements
        $gotName = $false   # Import-Module's only positional is Name (position 0)
        for ($i = 1; $i -lt $elements.Count; $i++) {
            $el = $elements[$i]
            if ($el -is [System.Management.Automation.Language.CommandParameterAst]) {
                if ($el.Argument) { continue }                       # -Param:value form
                if ($el.ParameterName -eq 'Name') { continue }       # next element is the name(s)
                if ($valueParams -contains $el.ParameterName) { $i++ } # skip this parameter's value
                continue                                             # switches consume nothing
            }
            # bare positional element — only the first one carries module name(s);
            # anything later is a stray value, not a second module to install.
            if (-not $gotName) {
                foreach ($name in (Resolve-StoModuleElement $el)) { & $addDep $name }
                $gotName = $true
            }
        }
    }

    # filter: local paths and builtins
    $result = [System.Collections.Generic.List[object]]::new()
    $seen = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($spec in $deps.Values) {
        $d = [string]$spec.Name
        if (-not $d) { continue }
        if ($d -match '[/\\]' -or $d -match '\.(psm1|psd1|dll)$') { continue }   # local/path import
        if ($script:BuiltinModules -contains $d) { continue }
        if (Test-Path (Join-Path $Script.Dir "$d.psm1")) { continue }            # local module next to script
        # bare-folder check only for folder scripts: a loose root script's Dir
        # is the whole repo, where every sibling SCRIPT folder would false-match
        if (-not ($null -ne $Script.PSObject.Properties['Loose'] -and $Script.Loose) -and
            (Test-Path (Join-Path $Script.Dir $d))) { continue }                 # local module folder
        $mapped = if ($script:ModuleNameMap.ContainsKey($d.ToLower())) { $script:ModuleNameMap[$d.ToLower()] } else { $d }
        if ($seen.Add($mapped)) {
            $result.Add((New-StoDep -Name $mapped -RequiredVersion $spec.Rv -MinimumVersion $spec.MinV -MaximumVersion $spec.MaxV))
        }
    }
    $result | Sort-Object Name
}

function Resolve-StoModuleElement {
    param($Element)
    $names = @()
    if ($Element -is [System.Management.Automation.Language.StringConstantExpressionAst]) {
        $names += $Element.Value
    } elseif ($Element -is [System.Management.Automation.Language.ArrayLiteralAst]) {
        foreach ($e in $Element.Elements) {
            if ($e -is [System.Management.Automation.Language.StringConstantExpressionAst]) { $names += $e.Value }
        }
    }
    $names
}

function Get-StoInstalledModules {
    # name(lower) -> list of installed [version]s (0.0 when unknown).
    # -Names limits the system-module walk to those names — a full
    # Get-Module -ListAvailable walks every PSModulePath dir and costs seconds
    # on every run start
    param([Parameter(Mandatory)]$Script, [string[]]$Names = @())
    $installed = [System.Collections.Generic.Dictionary[string, object]]::new([StringComparer]::OrdinalIgnoreCase)
    $add = {
        param([string]$Name, $Version)
        if (-not $installed.ContainsKey($Name)) { $installed[$Name] = [System.Collections.Generic.List[version]]::new() }
        $v = $Version -as [version]
        if ($null -eq $v) { $v = [version]'0.0' }
        $installed[$Name].Add($v)
    }
    if (Test-Path $Script.ModuleDir) {
        foreach ($d in (Get-ChildItem $Script.ModuleDir -Directory -ErrorAction SilentlyContinue)) {
            # Save-PSResource/Save-Module layout: <ModuleDir>/<Name>/<Version>/
            $verDirs = @(Get-ChildItem $d.FullName -Directory -ErrorAction SilentlyContinue |
                Where-Object { $null -ne ($_.Name -as [version]) })
            if ($verDirs) { foreach ($vd in $verDirs) { & $add $d.Name $vd.Name } }
            else { & $add $d.Name $null }
        }
    }
    $listArgs = @{}
    if ($Names.Count -gt 0) { $listArgs.Name = $Names }
    foreach ($m in (Get-Module -ListAvailable @listArgs -ErrorAction SilentlyContinue)) {
        & $add $m.Name $m.Version
    }
    $installed
}

function Test-StoDepSatisfied {
    param([Parameter(Mandatory)]$Dep, [Parameter(Mandatory)]$Installed)
    if (-not $Installed.ContainsKey($Dep.Name)) { return $false }
    $versions = $Installed[$Dep.Name]
    if ($Dep.RequiredVersion) {
        $rv = $Dep.RequiredVersion -as [version]
        if ($null -eq $rv) { return $true }   # unparseable constraint — assume ok
        return [bool]($versions | Where-Object { $_ -eq $rv })
    }
    $min = $Dep.MinimumVersion -as [version]
    $max = $Dep.MaximumVersion -as [version]
    if (-not $min -and -not $max) { return $true }
    [bool]($versions | Where-Object { (-not $min -or $_ -ge $min) -and (-not $max -or $_ -le $max) })
}

# ---------------------------------------------------------------------------
# Verbatim from src/Scripts.psm1's Get-StoScriptParameters, full return shape
# (Parameters + Synopsis + Help + ParseWarnings) — get_script_details (P9)
# composes all four from this single scan, never a second AST parse.
# ---------------------------------------------------------------------------

function Get-StoScriptParams {
    param([Parameter(Mandatory)][string]$Entry)
    $params = [System.Collections.Generic.List[object]]::new()
    $tokens = $null; $errors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($Entry, [ref]$tokens, [ref]$errors)
    $help = $null
    try { $help = $ast.GetHelpContent() } catch { }

    if ($ast.ParamBlock) {
        foreach ($p in @($ast.ParamBlock.Parameters)) {
            $name = $p.Name.VariablePath.UserPath
            $type = "$($p.StaticType.Name)"
            $default = if ($p.DefaultValue) { $p.DefaultValue.Extent.Text } else { $null }
            $mandatory = $false
            $validateSet = @()
            foreach ($attr in $p.Attributes) {
                if ($attr -isnot [System.Management.Automation.Language.AttributeAst]) { continue }
                if ($attr.TypeName.Name -match '^Parameter') {
                    foreach ($na in @($attr.NamedArguments)) {
                        if ($na.ArgumentName -eq 'Mandatory' -and
                            ($na.ExpressionOmitted -or "$($na.Argument.Extent.Text)" -match '\$true|^1$')) {
                            $mandatory = $true
                        }
                    }
                } elseif ($attr.TypeName.Name -match '^ValidateSet') {
                    $validateSet = @($attr.PositionalArguments |
                            Where-Object { $_ -is [System.Management.Automation.Language.StringConstantExpressionAst] } |
                            ForEach-Object Value)
                }
            }
            $desc = ''
            if ($help -and $help.Parameters -and $help.Parameters.ContainsKey($name.ToUpperInvariant())) {
                $desc = ("$($help.Parameters[$name.ToUpperInvariant()])").Trim()
            }
            $params.Add([pscustomobject]@{
                    Name        = $name
                    Type        = $type
                    Mandatory   = $mandatory
                    Default     = $default
                    ValidateSet = $validateSet
                    IsSwitch    = ($type -eq 'SwitchParameter')
                    Description = $desc
                })
        }
    }
    [pscustomobject]@{
        Parameters    = $params
        Synopsis      = $(if ($help -and $help.Synopsis) { "$($help.Synopsis)".Trim() } else { '' })
        Help          = $(if ($help -and $help.Description) { "$($help.Description)".Trim() } else { '' })
        ParseWarnings = @($errors).Count
    }
}

# ---------------------------------------------------------------------------
# Drive the pipeline and emit the JSON result.
# ---------------------------------------------------------------------------

$allDeps = @(Get-StoScriptDeps -Script $Script)
# Deps.psm1:210's Get-StoMissingDeps short-circuit: no deps -> skip
# Get-StoInstalledModules entirely, avoiding its unfiltered (no -Names)
# Get-Module -ListAvailable walk of the whole PSModulePath on every
# dep-free script — the exact cost the -Names restriction exists to avoid.
if ($allDeps.Count -eq 0) {
    $missingDeps = @()
} else {
    $installed = Get-StoInstalledModules -Script $Script -Names @($allDeps | ForEach-Object Name)
    $missingDeps = @($allDeps | Where-Object { -not (Test-StoDepSatisfied -Dep $_ -Installed $installed) })
}
$paramScan = Get-StoScriptParams -Entry $Entry
$paramList = @($paramScan.Parameters)

function ConvertTo-DepRecord {
    param($D)
    [ordered]@{
        name            = $D.Name
        requiredVersion = $D.RequiredVersion
        minimumVersion  = $D.MinimumVersion
        maximumVersion  = $D.MaximumVersion
        display         = $D.Display
    }
}

$result = [ordered]@{
    deps    = @($allDeps | ForEach-Object { ConvertTo-DepRecord $_ })
    missing = @($missingDeps | ForEach-Object { ConvertTo-DepRecord $_ })
    params  = @($paramList | ForEach-Object {
            [ordered]@{
                name        = $_.Name
                type        = $_.Type
                mandatory   = [bool]$_.Mandatory
                default     = $_.Default
                validateSet = @($_.ValidateSet)
                isSwitch    = [bool]$_.IsSwitch
                description = $_.Description
            }
        })
    synopsis      = $paramScan.Synopsis
    help          = $paramScan.Help
    parseWarnings = $paramScan.ParseWarnings
}
$result | ConvertTo-Json -Depth 8 -Compress
