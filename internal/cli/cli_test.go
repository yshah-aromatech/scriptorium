package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cli"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// setupApp writes a minimal config.json (dataDir only) and points
// SCRIPTORIUM_APP_DIR at it, so cli.Main's ResolveAppDir picks it up. It
// also blanks N8N_WEBHOOK_URL: app.Open lets the env override config, and
// no test may send real webhooks at an ambient production endpoint.
func setupApp(t *testing.T) (appDir, dataDir string) {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir = t.TempDir()
	dataDir = filepath.Join(t.TempDir(), "data")
	cfgJSON := fmt.Sprintf(`{"dataDir":%q}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)
	return appDir, dataDir
}

// writeScript drops a conventional main.ps1 under dataDir/scripts/<name>/
// (the legacy single-repo layout: discovery's root IS dataDir/scripts).
func writeScript(t *testing.T, dataDir, name, body string) {
	t.Helper()
	dir := filepath.Join(dataDir, "scripts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ps1"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------
// 1. Exit-code matrix
// ---------------------------------------------------------------------

func TestExitCodeMatrix(t *testing.T) {
	pwshtest.RequirePwsh(t)
	_, dataDir := setupApp(t)
	writeScript(t, dataDir, "ok", "exit 0")
	writeScript(t, dataDir, "bad", "exit 5")
	writeScript(t, dataDir, "busy", "exit 0")

	t.Run("success", func(t *testing.T) {
		var out, errw bytes.Buffer
		if code := cli.Main([]string{"--run", "ok"}, &out, &errw); code != 0 {
			t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
		}
		// the summary line's byte shape is a binding global constraint
		re := regexp.MustCompile(`(?m)^-- ok: success \(exit 0\) in \d+(\.\d+)?s \| cpu avg \d+(\.\d+)?% peak \d+(\.\d+)?% \| mem avg \d+(\.\d+)?MB peak \d+(\.\d+)?MB$`)
		if !re.MatchString(out.String()) {
			t.Errorf("stdout has no PS-shaped summary line:\n%s", out.String())
		}
	})

	t.Run("failure", func(t *testing.T) {
		var out, errw bytes.Buffer
		if code := cli.Main([]string{"--run", "bad"}, &out, &errw); code != 1 {
			t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
		}
	})

	t.Run("not-found", func(t *testing.T) {
		var out, errw bytes.Buffer
		code := cli.Main([]string{"--run", "does-not-exist"}, &out, &errw)
		if code != 2 {
			t.Fatalf("exit = %d, want 2\nstderr: %s", code, errw.String())
		}
		want := "script 'does-not-exist' not found — run 'scriptorium --list' (or sync first)"
		if !strings.Contains(errw.String(), want) {
			t.Errorf("stderr = %q, want to contain %q", errw.String(), want)
		}
	})

	t.Run("skipped", func(t *testing.T) {
		locksDir := filepath.Join(dataDir, "locks")
		if err := os.MkdirAll(locksDir, 0o755); err != nil {
			t.Fatal(err)
		}
		locks := lockfile.NewDir(locksDir)
		release, _, acquired := locks.Acquire("busy")
		if !acquired {
			t.Fatal("failed to pre-acquire the lock")
		}
		defer release()

		var out, errw bytes.Buffer
		code := cli.Main([]string{"--run", "busy"}, &out, &errw)
		if code != 3 {
			t.Fatalf("exit = %d, want 3\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
		}
	})
}

// ---------------------------------------------------------------------
// 2. The diff oracle: --list and --history must byte-match the real
// scriptorium.ps1 over identical seed data.
// ---------------------------------------------------------------------

// moduleRoot walks up from the test's cwd to the go.mod directory. This
// worktree ships the PowerShell app itself (scriptorium.ps1 + src/*.psm1),
// so the oracle runs against its own checkout on any machine, CI included.
// Same pattern as internal/importlint's moduleRoot.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test dir")
		}
		dir = parent
	}
}

func TestDiffOracleListAndHistory(t *testing.T) {
	pwshtest.RequirePwsh(t)
	t.Setenv("N8N_WEBHOOK_URL", "")

	repoRoot := moduleRoot(t)
	mainCfgPath := filepath.Join(repoRoot, "config.json")
	if _, err := os.Stat(mainCfgPath); err == nil {
		t.Fatalf("refusing to run: %s already exists — remove it manually before running this test", mainCfgPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking %s: %v", mainCfgPath, err)
	}

	dataDir := t.TempDir()
	writeScript(t, dataDir, "hello", "Write-Output 'hi'\n")
	if err := os.MkdirAll(filepath.Join(dataDir, "scripts", "pytool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "scripts", "pytool", "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// three crafted rows: durations under 1000s, cpu/mem at most one
	// decimal (Round1-shaped), and one row with resources missing entirely.
	historyLines := []string{
		`{"script":"hello","status":"success","trigger":"cron","startedAt":"2026-08-30T10:00:00.000Z","durationSec":12.3,"resources":{"cpuMaxPercent":45.6,"memMaxMb":128.5}}`,
		`{"script":"pytool","status":"failure","trigger":"manual","startedAt":"2026-08-30T11:15:30.000Z","durationSec":999,"resources":{"cpuMaxPercent":10,"memMaxMb":64}}`,
		`{"script":"hello","status":"skipped","trigger":"manual","startedAt":"2026-08-30T12:00:00.000Z","durationSec":0}`,
	}
	if err := os.WriteFile(filepath.Join(dataDir, "history.jsonl"), []byte(strings.Join(historyLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgJSON := fmt.Sprintf(`{"dataDir":%q}`, dataDir)

	if err := os.WriteFile(mainCfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(mainCfgPath) }()
	t.Cleanup(func() { _ = os.Remove(mainCfgPath) })

	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)

	for _, args := range [][]string{{"--list"}, {"--history"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var goOut, goErr bytes.Buffer
			if code := cli.Main(args, &goOut, &goErr); code != 0 {
				t.Fatalf("cli.Main exit = %d, stderr: %s", code, goErr.String())
			}

			psArgs := append([]string{filepath.Join(repoRoot, "scriptorium.ps1")}, args...)
			cmd := exec.Command("pwsh", append([]string{"-NoProfile"}, psArgs...)...)
			psOut, err := cmd.Output()
			if err != nil {
				stderr := ""
				if ee, ok := err.(*exec.ExitError); ok {
					stderr = string(ee.Stderr)
				}
				t.Fatalf("pwsh failed: %v\n%s", err, stderr)
			}

			if goOut.String() != string(psOut) {
				t.Errorf("Go and PS %v output differ:\n--- Go (%d bytes) ---\n%q\n--- PS (%d bytes) ---\n%q",
					args, goOut.Len(), goOut.String(), len(psOut), string(psOut))
			}
		})
	}
}

// ---------------------------------------------------------------------
// 3. The missed-run sweep piggybacks on --run without breaking it. nil
// Schedules (no crontab reader until P7) makes it a no-op today.
// ---------------------------------------------------------------------

func TestRunMissedSweepDoesNotBreakTheRun(t *testing.T) {
	pwshtest.RequirePwsh(t)
	_, dataDir := setupApp(t)
	writeScript(t, dataDir, "ok", "exit 0")

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--run", "ok"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stdout: %s, stderr: %s", code, out.String(), errw.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "missed-state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no missed-state.json (nil Schedules is a no-op until P7's crontab reader exists), stat err = %v", err)
	}
}

// ---------------------------------------------------------------------
// 4. Warnings to stderr; --repos / --add-repo (offline, no pwsh needed).
// ---------------------------------------------------------------------

func TestWarningsGoToStderr(t *testing.T) {
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	cfgJSON := fmt.Sprintf(`{"dataDir":%q,"bogusKey":true}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--repos"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	want := "WARNING: config.json: unknown key 'bogusKey' — ignored (typo?)"
	if !strings.Contains(errw.String(), want) {
		t.Errorf("stderr = %q, want to contain %q", errw.String(), want)
	}
}

func TestReposFlow(t *testing.T) {
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	cfgJSON := fmt.Sprintf(`{"dataDir":%q,"repos":[{"name":"a","url":"https://example.invalid/a.git","branch":"main"},{"name":"b","url":""}]}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--repos"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	want := "a               main     https://example.invalid/a.git\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout = %q, want to contain %q", out.String(), want)
	}
}

func TestAddRepoFlow(t *testing.T) {
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	cfgJSON := fmt.Sprintf(`{"dataDir":%q}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)

	var out, errw bytes.Buffer
	code := cli.Main([]string{"--add-repo", "https://example.invalid/foo.git"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, stdout: %s, stderr: %s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "added repo 'foo'") {
		t.Errorf("stdout = %q, want to contain \"added repo 'foo'\"", out.String())
	}
	if !strings.Contains(out.String(), "run 'scriptorium --sync' to clone it") {
		t.Errorf("stdout = %q, want the sync hint", out.String())
	}

	out.Reset()
	errw.Reset()
	code = cli.Main([]string{"--add-repo", "https://example.invalid/foo.git"}, &out, &errw)
	if code != 1 {
		t.Fatalf("duplicate add-repo: exit = %d, want 1; stdout: %s", code, out.String())
	}
}

// ---------------------------------------------------------------------
// 5. Bare invocation (no flags): an honest TUI-not-yet-available stub —
// exit 1 so a misconfigured cron entry can't report success while doing
// nothing. Also catches a flag-parsing regression falling through here.
// ---------------------------------------------------------------------

func TestBareInvocationIsAnHonestStub(t *testing.T) {
	setupApp(t)
	var out, errw bytes.Buffer
	if code := cli.Main(nil, &out, &errw); code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
	}
	if !strings.Contains(errw.String(), "the TUI is not yet available in the Go rebuild") {
		t.Errorf("stderr = %q, want the TUI stub message", errw.String())
	}
}
