# Deps.psm1 — automatic module dependency detection (PowerShell AST) and
# per-script module directory management. The PowerShell analog of one venv
# per script: each script gets its own module dir prepended to PSModulePath.

# Modules that ship with PowerShell 7 / are always available — never installed.
$script:BuiltinModules = @(
    'Microsoft.PowerShell.Archive', 'Microsoft.PowerShell.Core',
    'Microsoft.PowerShell.Diagnostics', 'Microsoft.PowerShell.Host',
    'Microsoft.PowerShell.Management', 'Microsoft.PowerShell.Security',
    'Microsoft.PowerShell.Utility', 'Microsoft.PowerShell.PSResourceGet',
    'PSReadLine', 'PackageManagement', 'PowerShellGet', 'ThreadJob',
    'CimCmdlets', 'PSDiagnostics', 'Microsoft.WSMan.Management'
)

# Common "imported name" -> "gallery name" mismatches (cv2->opencv-python analog)
$script:ModuleNameMap = @{
    'pester'  = 'Pester'
    'az'      = 'Az'
    'awstools' = 'AWS.Tools.Common'
    'awspowershell' = 'AWSPowerShell.NetCore'
    'sqlps'   = 'SqlServer'
}

# A dependency is a small object: Name plus optional version constraints
# (RequiredVersion exact, MinimumVersion/MaximumVersion bounds — only
# #Requires -Modules hashtable syntax carries versions). Display is the
# human-readable form used in prompts and output.
function New-PssDep {
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

# ---------------------------------------------------------------------------
# Scan a script's source for module dependencies:
#   - #Requires -Modules ...   (versions honored)
#   - using module ...
#   - Import-Module / ipmo calls
# Local files (paths, .psm1/.psd1 in the script folder) and builtins excluded.
# ---------------------------------------------------------------------------
function Get-PssScriptDeps {
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
                foreach ($name in (Resolve-PssModuleElement $el)) { & $addDep $name }
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
        if (Test-Path (Join-Path $Script.Dir $d)) { continue }                   # local module folder
        $mapped = if ($script:ModuleNameMap.ContainsKey($d.ToLower())) { $script:ModuleNameMap[$d.ToLower()] } else { $d }
        if ($seen.Add($mapped)) {
            $result.Add((New-PssDep -Name $mapped -RequiredVersion $spec.Rv -MinimumVersion $spec.MinV -MaximumVersion $spec.MaxV))
        }
    }
    $result | Sort-Object Name
}

function Resolve-PssModuleElement {
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

# ---------------------------------------------------------------------------
# What's missing = declared deps minus (script module dir + system modules),
# with version constraints checked against the installed versions.
# ---------------------------------------------------------------------------
function Get-PssInstalledModules {
    # name(lower) -> list of installed [version]s (0.0 when unknown)
    param([Parameter(Mandatory)]$Script)
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
    foreach ($m in (Get-Module -ListAvailable -ErrorAction SilentlyContinue)) {
        & $add $m.Name $m.Version
    }
    $installed
}

function Test-PssDepSatisfied {
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

function Get-PssMissingDeps {
    param([Parameter(Mandatory)]$Script)
    $deps = @(Get-PssScriptDeps -Script $Script)
    if (-not $deps) { return @() }
    $installed = Get-PssInstalledModules -Script $Script
    @($deps | Where-Object { -not (Test-PssDepSatisfied -Dep $_ -Installed $installed) })
}

# ---------------------------------------------------------------------------
# Build the pwsh -Command string that installs modules into the script's
# module dir. Run as a streamed task so the TUI shows live progress.
# ---------------------------------------------------------------------------
function Get-PssInstallCommand {
    # $Modules: dep objects from Get-PssScriptDeps/Get-PssMissingDeps (plain
    # strings also accepted — treated as unversioned names)
    param([Parameter(Mandatory)]$Script, [Parameter(Mandatory)][object[]]$Modules)
    if (-not (Test-Path $Script.ModuleDir)) {
        New-Item -ItemType Directory -Path $Script.ModuleDir -Force | Out-Null
    }
    $dir = $Script.ModuleDir
    $specs = ($Modules | ForEach-Object {
            $m = if ($_ -is [string]) { New-PssDep -Name $_ } else { $_ }
            $n = [string]$m.Name -replace "'", "''"
            "@{ Name='$n'; Rv='$($m.RequiredVersion)'; MinV='$($m.MinimumVersion)'; MaxV='$($m.MaximumVersion)' }"
        }) -join ', '
    @"
`$ErrorActionPreference = 'Continue'
`$ok = `$true
foreach (`$m in @($specs)) {
    Write-Host "installing `$(`$m.Name) `$(if (`$m.Rv) { `$m.Rv } elseif (`$m.MinV -or `$m.MaxV) { "(`$(`$m.MinV)-`$(`$m.MaxV))" }) -> $dir"
    try {
        if (Get-Command Save-PSResource -ErrorAction SilentlyContinue) {
            `$p = @{ Name = `$m.Name; Path = '$dir'; Repository = 'PSGallery'; TrustRepository = `$true; ErrorAction = 'Stop' }
            if (`$m.Rv) { `$p.Version = `$m.Rv }
            elseif (`$m.MinV -and `$m.MaxV) { `$p.Version = "[`$(`$m.MinV),`$(`$m.MaxV)]" }
            elseif (`$m.MinV) { `$p.Version = "[`$(`$m.MinV),)" }
            elseif (`$m.MaxV) { `$p.Version = "(,`$(`$m.MaxV)]" }
            Save-PSResource @p
        } else {
            `$p = @{ Name = `$m.Name; Path = '$dir'; Repository = 'PSGallery'; Force = `$true; ErrorAction = 'Stop' }
            if (`$m.Rv) { `$p.RequiredVersion = `$m.Rv }
            if (`$m.MinV) { `$p.MinimumVersion = `$m.MinV }
            if (`$m.MaxV) { `$p.MaximumVersion = `$m.MaxV }
            Save-Module @p
        }
        Write-Host "`$(`$m.Name) installed"
    } catch {
        Write-Host "FAILED to install `$(`$m.Name) : `$(`$_.Exception.Message)"
        `$ok = `$false
    }
}
if (-not `$ok) { exit 1 }
"@
}

# pwsh -Command string that re-saves the latest version of every module in
# every per-script module dir (used by the system update action).
function Get-PssModuleUpgradeCommand {
    $paths = Get-PssPaths
    @"
`$root = '$($paths.ModulesDir)'
if (-not (Test-Path `$root)) { Write-Host 'no module dirs yet'; exit 0 }
foreach (`$scriptDir in Get-ChildItem `$root -Directory) {
    foreach (`$mod in Get-ChildItem `$scriptDir.FullName -Directory) {
        Write-Host "upgrading `$(`$mod.Name) in `$(`$scriptDir.Name)"
        try {
            if (Get-Command Save-PSResource -ErrorAction SilentlyContinue) {
                Save-PSResource -Name `$mod.Name -Path `$scriptDir.FullName -Repository PSGallery -TrustRepository -ErrorAction Stop
            } else {
                Save-Module -Name `$mod.Name -Path `$scriptDir.FullName -Repository PSGallery -Force -ErrorAction Stop
            }
        } catch { Write-Host "  failed: `$(`$_.Exception.Message)" }
    }
}
Write-Host 'module upgrade complete'
"@
}

Export-ModuleMember -Function Get-PssScriptDeps, Get-PssMissingDeps, Get-PssInstalledModules,
Test-PssDepSatisfied, Get-PssInstallCommand, Get-PssModuleUpgradeCommand, New-PssDep
