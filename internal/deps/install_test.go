package deps

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// oraclePaths is the ###PATHS### section of the combined builder oracle
// script: the exact filesystem paths it built its Script objects from, so
// the Go side calls the builders with identical inputs.
type oraclePaths struct {
	ModulesDir  string
	VenvsDir    string
	PSModuleDir string
	PyVenvDir   string
	PyDir       string
	PythonBin   string
}

// installOracleScript drives every install/upgrade command builder in
// src/Deps.psm1 (the REAL functions, not a copy) over a shared temp app dir
// and prints each result delimited by a "###NAME###" marker line, so a
// single pwsh invocation covers the whole builder matrix.
const installOracleScript = `param([string]$AppDir)
Import-Module '%[1]s/src/Core.psm1', '%[1]s/src/Deps.psm1' -Force -DisableNameChecking
Initialize-Sto -AppDir $AppDir
$paths = Get-StoPaths
$psScript = [pscustomobject]@{ Name='t'; Dir=(Join-Path $AppDir 'scr-ps'); Entry=(Join-Path $AppDir 'scr-ps/main.ps1'); ModuleDir=(Join-Path $paths.ModulesDir 't') }
$pyScript = [pscustomobject]@{ Name='pyt'; Dir=(Join-Path $AppDir "scr-py'q"); Entry=(Join-Path $AppDir "scr-py'q/main.py"); Runtime='python'; VenvDir=(Join-Path $paths.VenvsDir "pyt'q") }
New-Item -ItemType Directory -Path $psScript.Dir -Force | Out-Null
New-Item -ItemType Directory -Path $pyScript.Dir -Force | Out-Null

$psMods = @((New-StoDep -Name 'Pester' -RequiredVersion '5.8.0'), (New-StoDep -Name 'Az.Accounts' -MinimumVersion '2.0' -MaximumVersion '3.0'), (New-StoDep -Name "Weird'Name"))
$pyMods = @((New-StoDep -Name 'cv2' | Add-Member -NotePropertyName PipName -NotePropertyValue 'opencv-python' -PassThru), (New-StoDep -Name 'requests' | Add-Member -NotePropertyName PipName -NotePropertyValue 'requests' -PassThru))

$pathsOut = @{ ModulesDir = $paths.ModulesDir; VenvsDir = $paths.VenvsDir; PSModuleDir = $psScript.ModuleDir; PyVenvDir = $pyScript.VenvDir; PyDir = $pyScript.Dir; PythonBin = "$((Get-StoConfig).pythonBin)" }
Write-Host "###PATHS###"
$pathsOut | ConvertTo-Json -Compress
Write-Host "###MODULE_INSTALL###"
Get-StoInstallCommand -Script $psScript -Modules $psMods
Write-Host "###MODULE_UPGRADE###"
Get-StoModuleUpgradeCommand
Write-Host "###PY_INSTALL_DEPS###"
Get-StoInstallCommand -Script $pyScript -Modules $pyMods
Write-Host "###PY_INSTALL_NOTHING###"
Get-StoPythonInstallCommand -Script $pyScript -Deps @()
Write-Host "###PY_INSTALL_REQUIREMENTS###"
"requests" | Set-Content (Join-Path $pyScript.Dir 'requirements.txt')
Get-StoInstallCommand -Script $pyScript -Modules $pyMods
Write-Host "###VENV_UPGRADE###"
Get-StoVenvUpgradeCommand
Write-Host "###END###"
`

// oracleCase pairs a marker-section name with the Go builder result to
// compare it against.
type oracleCase struct {
	name string
	got  string
}

// parseOracleSections splits the marker-delimited stdout into a
// name->trimmed-body map.
func parseOracleSections(out string) map[string]string {
	sections := map[string]string{}
	parts := strings.Split(out, "###")
	// parts alternates: [prefix, NAME, body, NAME, body, ...]
	for i := 1; i+1 < len(parts); i += 2 {
		name := parts[i]
		body := parts[i+1]
		sections[name] = strings.Trim(body, "\r\n")
	}
	return sections
}

func TestInstallCommandBuildersMatchRealDepsPsm1(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)
	repoRoot := findRepoRoot(t)

	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "config.json"),
		[]byte(`{"dataDir":"`+filepath.ToSlash(filepath.Join(appDir, "data"))+`","pythonBin":"/usr/bin/py'thon3"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "oracle.ps1")
	content := fmt.Sprintf(installOracleScript, filepath.ToSlash(repoRoot))
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", script, appDir).CombinedOutput()
	if err != nil {
		t.Fatalf("oracle pwsh failed: %v\n%s", err, out)
	}
	sections := parseOracleSections(string(out))

	var p oraclePaths
	if err := json.Unmarshal([]byte(strings.TrimSpace(sections["PATHS"])), &p); err != nil {
		t.Fatalf("parsing PATHS section: %v\nraw: %s", err, sections["PATHS"])
	}

	psMods := []Dep{
		{Name: "Pester", RequiredVersion: "5.8.0"},
		{Name: "Az.Accounts", MinimumVersion: "2.0", MaximumVersion: "3.0"},
		{Name: "Weird'Name"},
	}
	pyMods := []Dep{
		{Name: "cv2", PipName: "opencv-python"},
		{Name: "requests", PipName: "requests"},
	}

	// The oracle script generated PY_INSTALL_DEPS/PY_INSTALL_NOTHING before
	// writing pyScript.Dir's requirements.txt, then PY_INSTALL_REQUIREMENTS
	// after — but that file now sits permanently on disk, so the Go builder
	// (which re-checks the filesystem on every call) must be driven in the
	// same order, checking PY_INSTALL_REQUIREMENTS first while the file
	// exists, then removing it to reproduce the no-requirements-file state.
	var cases []oracleCase
	cases = append(cases,
		oracleCase{"MODULE_INSTALL", ModuleInstallCommand(p.PSModuleDir, psMods)},
		oracleCase{"MODULE_UPGRADE", ModuleUpgradeCommand(p.ModulesDir)},
		oracleCase{"PY_INSTALL_REQUIREMENTS", PythonInstallCommand(p.PyDir, p.PyVenvDir, p.PythonBin, pyMods)},
		oracleCase{"VENV_UPGRADE", VenvUpgradeCommand(p.VenvsDir, p.PythonBin)},
	)
	if err := os.Remove(filepath.Join(p.PyDir, "requirements.txt")); err != nil {
		t.Fatal(err)
	}
	cases = append(cases,
		oracleCase{"PY_INSTALL_DEPS", PythonInstallCommand(p.PyDir, p.PyVenvDir, p.PythonBin, pyMods)},
		oracleCase{"PY_INSTALL_NOTHING", PythonInstallCommand(p.PyDir, p.PyVenvDir, p.PythonBin, nil)},
	)
	for _, c := range cases {
		want := sections[c.name]
		if want == "" {
			t.Fatalf("oracle produced no %s section; full output:\n%s", c.name, out)
		}
		if c.got != want {
			t.Errorf("%s mismatch:\n--- go ---\n%s\n--- ps ---\n%s", c.name, c.got, want)
		}
	}
}

// TestInstallCommandDispatch checks InstallCommand's ps/python dispatch
// against the same oracle's two-runtime Get-StoInstallCommand results.
func TestInstallCommandDispatch(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)
	repoRoot := findRepoRoot(t)

	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "config.json"),
		[]byte(`{"dataDir":"`+filepath.ToSlash(filepath.Join(appDir, "data"))+`","pythonBin":"/usr/bin/py'thon3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "oracle.ps1")
	content := fmt.Sprintf(installOracleScript, filepath.ToSlash(repoRoot))
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", script, appDir).CombinedOutput()
	if err != nil {
		t.Fatalf("oracle pwsh failed: %v\n%s", err, out)
	}
	sections := parseOracleSections(string(out))
	var p oraclePaths
	if err := json.Unmarshal([]byte(strings.TrimSpace(sections["PATHS"])), &p); err != nil {
		t.Fatalf("parsing PATHS section: %v", err)
	}

	psMods := []Dep{
		{Name: "Pester", RequiredVersion: "5.8.0"},
		{Name: "Az.Accounts", MinimumVersion: "2.0", MaximumVersion: "3.0"},
		{Name: "Weird'Name"},
	}
	pyMods := []Dep{
		{Name: "cv2", PipName: "opencv-python"},
		{Name: "requests", PipName: "requests"},
	}

	gotPS := InstallCommand(InstallTarget{Runtime: "powershell", ModuleDir: p.PSModuleDir}, psMods, p.PythonBin)
	if gotPS != sections["MODULE_INSTALL"] {
		t.Errorf("InstallCommand(powershell) mismatch:\n--- go ---\n%s\n--- ps ---\n%s", gotPS, sections["MODULE_INSTALL"])
	}
	// the PY_INSTALL_DEPS section was generated before requirements.txt
	// existed, matching a Dir with no requirements file.
	gotPy := InstallCommand(InstallTarget{Runtime: "python", Dir: p.PyDir + "-none", VenvDir: p.PyVenvDir}, pyMods, p.PythonBin)
	if gotPy != sections["PY_INSTALL_DEPS"] {
		t.Errorf("InstallCommand(python) mismatch:\n--- go ---\n%s\n--- ps ---\n%s", gotPy, sections["PY_INSTALL_DEPS"])
	}
}
