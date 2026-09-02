package deps

import (
	"fmt"
	"os"
)

// LintTarget is the slice of a script LintCommand needs — the same
// keep-this-package-standalone rule as InstallTarget.
type LintTarget struct {
	Runtime  string // "powershell" | "python"
	Entry    string // the file to analyse
	VenvDir  string // python venv, if the script has one
	ToolsDir string // <dataDir>/tools — where PSScriptAnalyzer is saved on first use
}

// LintCommand is the port of Invoke-TuiLint (inventory §9.8): a pwsh -Command
// string that lints one script and exits non-zero on a finding that matters.
//
// It lives here rather than in the TUI because this package is where every
// generated pwsh command string is built — one place that knows how to quote a
// path into a PowerShell literal is worth more than one fewer file.
//
//   - PowerShell: PSScriptAnalyzer, saved into ToolsDir on first use
//     (Save-PSResource preferred, Save-Module fallback), run at
//     Information/Warning/Error, one line per finding, exit 1 only if any
//     finding is an Error.
//   - Python: pyflakes in the venv (or the system interpreter when there is no
//     venv yet), pip-installed on first use, falling back to a py_compile
//     syntax check when it cannot be had at all.
func LintCommand(t LintTarget, pythonBin string) string {
	entry := psQuoteEscape(t.Entry)
	if t.Runtime == "python" {
		return fmt.Sprintf(`$py = '%[1]s'
if (-not (Test-Path $py)) { $py = '%[2]s' }   # no venv yet — lint with the system python
& $py -c 'import pyflakes' 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host 'installing pyflakes (first use)...'
    & $py -m pip install --quiet pyflakes 2>&1 | Out-Null
    & $py -c 'import pyflakes' 2>$null
}
if ($LASTEXITCODE -eq 0) {
    $findings = & $py -m pyflakes '%[3]s' 2>&1
    if ($LASTEXITCODE -eq 0 -and -not "$findings") { Write-Host 'no findings — clean'; exit 0 }
    $findings | ForEach-Object { Write-Host $_ }
    exit 1
}
Write-Host 'pyflakes unavailable — syntax check only (py_compile)'
& $py -m py_compile '%[3]s' 2>&1 | ForEach-Object { Write-Host $_ }
if ($LASTEXITCODE -eq 0) { Write-Host 'syntax OK' } else { exit 1 }`,
			psQuoteEscape(VenvPython(t.VenvDir)), psQuoteEscape(pythonBin), entry)
	}

	return fmt.Sprintf(`$tools = '%[1]s'
$env:PSModulePath = "$tools%[2]s$env:PSModulePath"
if (-not (Get-Module -ListAvailable PSScriptAnalyzer)) {
    Write-Host 'installing PSScriptAnalyzer (first use)...'
    New-Item -ItemType Directory -Path $tools -Force | Out-Null
    if (Get-Command Save-PSResource -ErrorAction SilentlyContinue) {
        Save-PSResource -Name PSScriptAnalyzer -Path $tools -Repository PSGallery -TrustRepository -ErrorAction Stop
    } else {
        Save-Module -Name PSScriptAnalyzer -Path $tools -Repository PSGallery -Force -ErrorAction Stop
    }
}
$results = @(Invoke-ScriptAnalyzer -Path '%[3]s' -Severity Information, Warning, Error)
if ($results.Count -eq 0) { Write-Host 'no findings — clean'; exit 0 }
foreach ($r in $results) {
    Write-Host ('{0,-11} L{1,-4} {2}: {3}' -f $r.Severity, $r.Line, $r.RuleName, $r.Message)
}
Write-Host "$($results.Count) finding(s)"
exit ([int](@($results | Where-Object Severity -eq 'Error').Count -gt 0))`,
		psQuoteEscape(t.ToolsDir), string(os.PathListSeparator), entry)
}
