package deps

import (
	"fmt"
	"os"
	"strings"
)

// InstallTarget is the minimal slice of a script's fields InstallCommand
// needs to dispatch and build the right command — kept as its own small
// struct (rather than taking internal/scripts.Script) so this package stays
// as dependency-free as ScanPS/ScanPython already are; a caller holding a
// scripts.Script constructs one from its Runtime/Dir/ModuleDir/VenvDir.
type InstallTarget struct {
	Runtime   string // "powershell" | "python"
	Dir       string // script dir — python's requirements.txt lives here
	ModuleDir string // powershell module install target
	VenvDir   string // python venv location
}

// psQuoteEscape doubles embedded single quotes for interpolation into a
// PowerShell single-quoted string literal ('...').
func psQuoteEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// InstallCommand is the port of Get-StoInstallCommand: dispatches on
// Runtime to the PowerShell module-install command or the Python
// venv+pip-install command.
func InstallCommand(target InstallTarget, modules []Dep, pythonBin string) string {
	if target.Runtime == "python" {
		return PythonInstallCommand(target.Dir, target.VenvDir, pythonBin, modules)
	}
	return ModuleInstallCommand(target.ModuleDir, modules)
}

// ModuleInstallCommand is Get-StoInstallCommand's PowerShell branch: a
// pwsh -Command string that Save-PSResource's (preferred) or Save-Module's
// each dep into moduleDir, continuing past per-module failures and exiting 1
// overall if any failed. Byte-identical to the real function's output
// (verified by the builder oracle test) — including its one quirk: dir
// itself is NOT quote-escaped when interpolated into the generated `'...'`
// literals (only each dep Name is), matching the PS source exactly.
//
// Creating moduleDir is a side effect of this call, matching
// Get-StoInstallCommand (Deps.psm1:224-226), which creates it before
// building the command string — both generated branches below abort on a
// nonexistent -Path, so skipping this would make every first-time install
// fail permanently. Best-effort: an error here (e.g. permissions) is left
// for Save-PSResource/Save-Module's own -Path validation to report, exactly
// as PS's unchecked `New-Item ... | Out-Null` would surface it.
func ModuleInstallCommand(moduleDir string, modules []Dep) string {
	_ = os.MkdirAll(moduleDir, 0o755)

	specs := make([]string, len(modules))
	for i, m := range modules {
		specs[i] = fmt.Sprintf("@{ Name='%s'; Rv='%s'; MinV='%s'; MaxV='%s' }",
			psQuoteEscape(m.Name), m.RequiredVersion, m.MinimumVersion, m.MaximumVersion)
	}
	specsJoined := strings.Join(specs, ", ")

	return fmt.Sprintf(`$ErrorActionPreference = 'Continue'
$ok = $true
foreach ($m in @(%[1]s)) {
    Write-Host "installing $($m.Name) $(if ($m.Rv) { $m.Rv } elseif ($m.MinV -or $m.MaxV) { "($($m.MinV)-$($m.MaxV))" }) -> %[2]s"
    try {
        if (Get-Command Save-PSResource -ErrorAction SilentlyContinue) {
            $p = @{ Name = $m.Name; Path = '%[2]s'; Repository = 'PSGallery'; TrustRepository = $true; ErrorAction = 'Stop' }
            if ($m.Rv) { $p.Version = $m.Rv }
            elseif ($m.MinV -and $m.MaxV) { $p.Version = "[$($m.MinV),$($m.MaxV)]" }
            elseif ($m.MinV) { $p.Version = "[$($m.MinV),)" }
            elseif ($m.MaxV) { $p.Version = "(,$($m.MaxV)]" }
            Save-PSResource @p
        } else {
            $p = @{ Name = $m.Name; Path = '%[2]s'; Repository = 'PSGallery'; Force = $true; ErrorAction = 'Stop' }
            if ($m.Rv) { $p.RequiredVersion = $m.Rv }
            if ($m.MinV) { $p.MinimumVersion = $m.MinV }
            if ($m.MaxV) { $p.MaximumVersion = $m.MaxV }
            Save-Module @p
        }
        Write-Host "$($m.Name) installed"
    } catch {
        Write-Host "FAILED to install $($m.Name) : $($_.Exception.Message)"
        $ok = $false
    }
}
if (-not $ok) { exit 1 }`, specsJoined, moduleDir)
}

// ModuleUpgradeCommand is the port of Get-StoModuleUpgradeCommand: a
// pwsh -Command string that re-saves the latest version of every module in
// every per-script module dir under modulesDir.
func ModuleUpgradeCommand(modulesDir string) string {
	return fmt.Sprintf(`$root = '%[1]s'
if (-not (Test-Path $root)) { Write-Host 'no module dirs yet'; exit 0 }
foreach ($scriptDir in Get-ChildItem $root -Directory) {
    foreach ($mod in Get-ChildItem $scriptDir.FullName -Directory) {
        Write-Host "upgrading $($mod.Name) in $($scriptDir.Name)"
        try {
            if (Get-Command Save-PSResource -ErrorAction SilentlyContinue) {
                Save-PSResource -Name $mod.Name -Path $scriptDir.FullName -Repository PSGallery -TrustRepository -ErrorAction Stop
            } else {
                Save-Module -Name $mod.Name -Path $scriptDir.FullName -Repository PSGallery -Force -ErrorAction Stop
            }
        } catch { Write-Host "  failed: $($_.Exception.Message)" }
    }
}
Write-Host 'module upgrade complete'`, modulesDir)
}

// pythonInstallPackages resolves the unique PipNames for the Modules list,
// the same way Get-StoPythonInstallCommand's ForEach-Object | Select-Object
// -Unique does: a Dep already carrying a non-empty PipName uses it verbatim,
// else it's derived via PipName(Name).
func pythonInstallPackages(modules []Dep) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range modules {
		pkg := m.PipName
		if pkg == "" {
			pkg = PipName(m.Name)
		}
		if !seen[pkg] {
			seen[pkg] = true
			out = append(out, pkg)
		}
	}
	return out
}

// PythonInstallCommand is the port of Get-StoPythonInstallCommand: a
// pwsh -Command string that ensures the venv exists, then either
// `pip install -r requirements.txt` (when dir has one) or
// `pip install <unique pip names>` (or a no-op "nothing to install" line).
func PythonInstallCommand(dir, venvDir, pythonBin string, modules []Dep) string {
	venv := psQuoteEscape(venvDir)
	py := psQuoteEscape(VenvPython(venvDir))
	pythonBinEsc := psQuoteEscape(pythonBin)

	var installLine string
	if reqPath := requirementsPath(dir); reqPath != "" {
		installLine = fmt.Sprintf("& '%s' -m pip install -r '%s'", py, psQuoteEscape(reqPath))
	} else {
		pkgs := pythonInstallPackages(modules)
		if len(pkgs) == 0 {
			installLine = `Write-Host "nothing to install"`
		} else {
			quoted := make([]string, len(pkgs))
			for i, p := range pkgs {
				quoted[i] = "'" + psQuoteEscape(p) + "'"
			}
			installLine = fmt.Sprintf("& '%s' -m pip install @(%s)", py, strings.Join(quoted, ", "))
		}
	}

	return fmt.Sprintf(`$ErrorActionPreference = 'Continue'
if (-not (Test-Path '%[1]s')) {
    Write-Host "creating venv -> %[2]s"
    & '%[3]s' -m venv '%[2]s'
    if ($LASTEXITCODE -ne 0) { Write-Host 'FAILED to create venv (is python3-venv installed?)'; exit 1 }
    & '%[1]s' -m pip install --upgrade pip --quiet
}
%[4]s
if ($LASTEXITCODE -ne 0) { Write-Host 'pip install FAILED'; exit 1 }
Write-Host 'python deps installed'`, py, venv, pythonBinEsc, installLine)
}

// VenvUpgradeCommand is the port of Get-StoVenvUpgradeCommand: upgrades the
// system pip first (PEP 668 --break-system-packages fallback), then every
// existing venv's pip + outdated top-level packages, pip-checking each
// afterward and reporting (non-fatal exit 1 overall) if broken.
func VenvUpgradeCommand(venvsDir, pythonBin string) string {
	pythonBinEsc := psQuoteEscape(pythonBin)
	venvsDirEsc := psQuoteEscape(venvsDir)

	return fmt.Sprintf(`if (Get-Command '%[1]s' -ErrorAction SilentlyContinue) {
    Write-Host 'upgrading system pip...'
    & '%[1]s' -m pip install --upgrade pip --quiet 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        # Ubuntu 23.04+ PEP 668 managed environment
        & '%[1]s' -m pip install --upgrade pip --quiet --break-system-packages 2>&1 | Out-Null
        Write-Host $(if ($LASTEXITCODE -eq 0) { 'system pip upgraded (--break-system-packages)' } else { 'WARNING: system pip upgrade failed (non-fatal)' })
    } else { Write-Host 'system pip upgraded' }
}
$root = '%[2]s'
if (-not (Test-Path $root)) { Write-Host 'no venvs yet'; exit 0 }
$venvs = @(Get-ChildItem $root -Directory | Where-Object { Test-Path (Join-Path $_.FullName 'bin/python') })
if ($venvs.Count -eq 0) { Write-Host 'no venvs to upgrade yet'; exit 0 }
$broken = $false
foreach ($v in $venvs) {
    $py = Join-Path $v.FullName 'bin/python'
    Write-Host "'$($v.Name)': upgrading pip..."
    & $py -m pip install --upgrade pip --quiet
    # --not-required: only TOP-LEVEL packages. Explicitly upgrading a
    # dependency (e.g. pydantic-core, which pydantic pins exactly) forces it
    # past its parent's pin and breaks the venv; upgrading the parents lets
    # the resolver pull matching dependency versions.
    $outdated = & $py -m pip list --outdated --not-required --format=json 2>$null
    $pkgs = @()
    try { $pkgs = @(("$outdated" | ConvertFrom-Json) | ForEach-Object name) } catch { }
    if ($pkgs.Count -eq 0) { Write-Host "'$($v.Name)': all packages up to date."; continue }
    Write-Host "'$($v.Name)': upgrading $($pkgs.Count) package(s): $($pkgs -join ', ')"
    & $py -m pip install --upgrade @pkgs
    # verify the venv is still consistent; report loudly if not
    $check = & $py -m pip check 2>&1
    if ($LASTEXITCODE -ne 0) {
        $broken = $true
        Write-Host "'$($v.Name)': WARNING — dependency conflict after upgrade:"
        $check | ForEach-Object { Write-Host "  $_" }
        Write-Host "  fix: delete $($v.FullName) and re-run the script (venv is rebuilt automatically)"
    }
}
Write-Host 'venv upgrade complete'
if ($broken) { exit 1 }`, pythonBinEsc, venvsDirEsc)
}
