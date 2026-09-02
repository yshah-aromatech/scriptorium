package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cli"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// ---------------------------------------------------------------------
// --run dependency auto-install (scriptorium.ps1:157-163 parity).
// ---------------------------------------------------------------------

// setupAppWithPwshBin is setupApp plus a custom pwshBin config value — every
// auto-install test needs to control which "pwsh" binary the CLI shells out
// to, for the install command AND (for PowerShell scripts) the scan itself.
func setupAppWithPwshBin(t *testing.T, pwshBin string) (appDir, dataDir string) {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir = t.TempDir()
	dataDir = filepath.Join(t.TempDir(), "data")
	cfgJSON := fmt.Sprintf(`{"dataDir":%q,"pwshBin":%q}`, dataDir, pwshBin)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)
	return appDir, dataDir
}

// writeRelayingPwshStub builds a fake "pwsh": any `-File` invocation (the
// dependency scan, and the actual script launch for a PowerShell run) is
// relayed to the real pwsh so those keep working correctly; a `-Command`
// invocation (the install step) is intercepted instead — its full argv is
// captured to captureFile (one arg per line) and a recognizable marker line
// is printed, WITHOUT ever touching PSGallery or the network.
func writeRelayingPwshStub(t *testing.T, dir, realPwsh, captureFile string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"-Command\" ]; then\n" +
		"    : > '" + captureFile + "'\n" +
		"    for x in \"$@\"; do printf '%s\\n' \"$x\" >> '" + captureFile + "'; done\n" +
		"    echo 'STUB_INSTALL_RAN'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"exec '" + realPwsh + "' \"$@\"\n"
	path := filepath.Join(dir, "stub-pwsh.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunAutoInstallsMissingPowerShellModule(t *testing.T) {
	realPwsh := pwshtest.RequirePwsh(t)
	stubDir := t.TempDir()
	captureFile := filepath.Join(stubDir, "capture.txt")
	stub := writeRelayingPwshStub(t, stubDir, realPwsh, captureFile)

	_, dataDir := setupAppWithPwshBin(t, stub)
	// Import-Module (not #Requires) so the actual run — after the stub
	// "installs" nothing for real — doesn't get hard-blocked by pwsh's own
	// #Requires enforcement; -ErrorAction SilentlyContinue lets it proceed
	// to exit 0 exactly like a script that degrades gracefully without the
	// dep would.
	writeScript(t, dataDir, "depscript",
		"Import-Module ThisModuleDoesNotExistAnywhere12345 -ErrorAction SilentlyContinue\nWrite-Host ran\nexit 0\n")

	var out, errw bytes.Buffer
	code := cli.Main([]string{"--run", "depscript"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
	}

	if !strings.Contains(out.String(), "installing missing modules: ThisModuleDoesNotExistAnywhere12345") {
		t.Errorf("stdout missing the install announcement:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "STUB_INSTALL_RAN") {
		t.Errorf("stdout should carry the install command's streamed output, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "-- depscript: success (exit 0)") {
		t.Errorf("the run must still proceed after install, stdout:\n%s", out.String())
	}

	captured, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("install command was never invoked: %v", err)
	}
	capturedStr := string(captured)
	if !strings.Contains(capturedStr, "-NoProfile") || !strings.Contains(capturedStr, "-Command") {
		t.Errorf("captured argv missing expected flags:\n%s", capturedStr)
	}
	if !strings.Contains(capturedStr, "Name='ThisModuleDoesNotExistAnywhere12345'") {
		t.Errorf("captured install command missing the dep spec:\n%s", capturedStr)
	}
	moduleDir := filepath.Join(dataDir, "modules", "depscript")
	if !strings.Contains(capturedStr, moduleDir) {
		t.Errorf("captured install command missing the script's ModuleDir %q:\n%s", moduleDir, capturedStr)
	}
}

func TestRunAutoInstallsMissingPythonPackageViaRequirementsTxt(t *testing.T) {
	stubDir := t.TempDir()
	captureFile := filepath.Join(stubDir, "capture.txt")
	// The python path never shells to pwsh except for the install command
	// itself (scanning and the run both go straight through the python
	// interpreter), so this stub needs no real-pwsh relay at all.
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"-Command\" ]; then\n" +
		"    : > '" + captureFile + "'\n" +
		"    for x in \"$@\"; do printf '%s\\n' \"$x\" >> '" + captureFile + "'; done\n" +
		"    echo 'STUB_INSTALL_RAN'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"exit 1\n"
	stub := filepath.Join(stubDir, "stub-pwsh.sh")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	_, dataDir := setupAppWithPwshBin(t, stub)
	scriptDir := filepath.Join(dataDir, "scripts", "pytool")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "requirements.txt"), []byte("requests\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stub venv python: `-m pip list --format=json` reports nothing
	// installed (so requirements.txt's "requests" is missing), and the
	// script-launch invocation (the actual run, after auto-install) just
	// succeeds — no real pip, no real interpreter.
	venvDir := filepath.Join(dataDir, "venvs", "pytool")
	if err := os.MkdirAll(filepath.Join(venvDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	venvPyScript := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-m\" ] && [ \"$2\" = \"pip\" ] && [ \"$3\" = \"list\" ]; then\n" +
		"  echo '[]'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(venvDir, "bin", "python"), []byte(venvPyScript), 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errw bytes.Buffer
	code := cli.Main([]string{"--run", "pytool"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
	}

	if !strings.Contains(out.String(), "installing missing modules: requests") {
		t.Errorf("stdout missing the install announcement:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "STUB_INSTALL_RAN") {
		t.Errorf("stdout should carry the install command's streamed output, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "-- pytool: success (exit 0)") {
		t.Errorf("the run must still proceed after install, stdout:\n%s", out.String())
	}

	captured, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("install command was never invoked: %v", err)
	}
	capturedStr := string(captured)
	if !strings.Contains(capturedStr, "pip install -r") || !strings.Contains(capturedStr, "requirements.txt") {
		t.Errorf("captured install command should prefer requirements.txt:\n%s", capturedStr)
	}
}

func TestRunDegradedScanWarnsToStderrAndStillRuns(t *testing.T) {
	_, dataDir := setupAppWithPwshBin(t, "/nonexistent-pwsh-xyz")
	writeScript(t, dataDir, "degraded", "#Requires -Modules SomeModule\nexit 0\n")

	var out, errw bytes.Buffer
	code := cli.Main([]string{"--run", "degraded"}, &out, &errw)

	const wantWarning = "WARNING: pwsh not found — dependency scan degraded (regex), install checks skipped"
	if !strings.Contains(errw.String(), wantWarning) {
		t.Errorf("stderr = %q, want to contain %q", errw.String(), wantWarning)
	}
	if strings.Contains(out.String(), "installing missing modules") {
		t.Errorf("degraded scan reports Missing empty -> no install should be attempted, stdout:\n%s", out.String())
	}
	// with no working pwsh at all, the run itself can't start either — but
	// the pipeline must still have PROCEEDED to attempt it (not bailed out
	// early because the scan degraded).
	if !strings.Contains(out.String(), "degraded: failure") {
		t.Errorf("expected the run to still be attempted (and fail to start, no pwsh), stdout:\n%s", out.String())
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1 (failure)", code)
	}
}
