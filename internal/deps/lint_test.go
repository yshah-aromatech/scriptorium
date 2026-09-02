package deps_test

import (
	"os"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/deps"
)

// The PowerShell branch is inventory §9.8: PSScriptAnalyzer saved into the
// tools dir on first use, Information/Warning/Error, exit 1 only on an Error.
func TestLintCommandPowerShell(t *testing.T) {
	got := deps.LintCommand(deps.LintTarget{
		Runtime:  "powershell",
		Entry:    "/scripts/o'brien/job.ps1",
		ToolsDir: "/data/tools",
	}, "python3")

	for _, want := range []string{
		`$tools = '/data/tools'`,
		`$env:PSModulePath = "$tools` + string(os.PathListSeparator) + `$env:PSModulePath"`,
		"Save-PSResource -Name PSScriptAnalyzer",
		"Save-Module -Name PSScriptAnalyzer",
		"-Severity Information, Warning, Error",
		"no findings — clean",
		`exit ([int](@($results | Where-Object Severity -eq 'Error').Count -gt 0))`,
		// the entry path is interpolated into a PS single-quoted literal, so
		// its own quote has to be doubled or the command is broken (and, with
		// a hostile path, more than broken)
		`-Path '/scripts/o''brien/job.ps1'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the PowerShell lint command is missing %q:\n%s", want, got)
		}
	}
}

// The Python branch: pyflakes in the venv, the system interpreter when there
// is no venv, and py_compile as the last resort.
func TestLintCommandPython(t *testing.T) {
	got := deps.LintCommand(deps.LintTarget{
		Runtime: "python",
		Entry:   "/scripts/sync/main.py",
		VenvDir: "/data/venvs/sync",
	}, "python3")

	for _, want := range []string{
		`$py = '/data/venvs/sync/bin/python'`,
		`if (-not (Test-Path $py)) { $py = 'python3' }`,
		"-m pip install --quiet pyflakes",
		`-m pyflakes '/scripts/sync/main.py'`,
		"pyflakes unavailable — syntax check only (py_compile)",
		"syntax OK",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the Python lint command is missing %q:\n%s", want, got)
		}
	}
}
