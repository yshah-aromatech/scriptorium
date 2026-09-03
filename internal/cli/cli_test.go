package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/buildinfo"
	"github.com/yshah-aromatech/scriptorium/internal/cli"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// stubBuildinfo overrides the package-level buildinfo vars for one test —
// they are link-time globals, not something Main takes as a parameter.
func stubBuildinfo(version, commit, date string) func() {
	oldV, oldC, oldD := buildinfo.Version, buildinfo.Commit, buildinfo.Date
	buildinfo.Version, buildinfo.Commit, buildinfo.Date = version, commit, date
	return func() { buildinfo.Version, buildinfo.Commit, buildinfo.Date = oldV, oldC, oldD }
}

// ---------------------------------------------------------------------
// 0. NOTHING in this package may reach the real crontab.
//
// app.Open reads the managed block on every open, so cli.Main shells out to
// `crontab` on every call. TestMain therefore puts a fake `crontab` — one
// that reports an empty crontab and refuses every other argument — ahead of
// the real binary on PATH for the whole package, and shimCrontab swaps in a
// canned block for the tests that want one. A test that forgets is still
// safe: the package-wide shim is the floor, not an opt-in.
// ---------------------------------------------------------------------

// writeShim drops an executable fake `crontab` (and its canned spool) into
// dir. `-l` cats the spool and exits 0; every other argument list exits 1,
// so a write attempt can only fail — never land anywhere.
func writeShim(dir, block string) error {
	spool := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(spool, []byte(block), 0o644); err != nil {
		return err
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"-l\" ]; then exec cat " + spool + "; fi\nexit 1\n"
	return os.WriteFile(filepath.Join(dir, "crontab"), []byte(script), 0o755)
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sto-crontab-shim")
	if err != nil {
		panic(err)
	}
	if err := writeShim(dir, ""); err != nil {
		panic(err)
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// shimCrontab points PATH at a fake crontab serving block, for this test
// only. It serves BOTH the in-process Go reader (exec.LookPath honors the
// env) and any pwsh child, which inherits the same PATH.
func shimCrontab(t *testing.T, block string) {
	t.Helper()
	dir := t.TempDir()
	if err := writeShim(dir, block); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeMutableShim drops an executable fake `crontab` that, unlike
// writeShim's write-proof baseline, actually accepts a write: `-l` reads
// the spool, `-` (over stdin) replaces it, everything else fails. --migrate
// is the one place in this package a crontab write is EXPECTED to succeed.
func writeMutableShim(dir, block string) error {
	spool := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(spool, []byte(block), 0o644); err != nil {
		return err
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-l\" ]; then exec cat " + spool + "; fi\n" +
		"if [ \"$1\" = \"-\" ]; then cat > " + spool + "; exit 0; fi\n" +
		"exit 1\n"
	return os.WriteFile(filepath.Join(dir, "crontab"), []byte(script), 0o755)
}

func shimMutableCrontab(t *testing.T, block string) {
	t.Helper()
	dir := t.TempDir()
	if err := writeMutableShim(dir, block); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// psManagedBlock renders a managed block in the PowerShell app's spelling,
// so the PS reader and the Go reader both parse it (the compat mandate).
func psManagedBlock(name, expr string) string {
	return strings.Join([]string{
		cron.BlockStart,
		expr + " cd '/opt/scriptorium' && 'pwsh' -NoProfile -File scriptorium.ps1 --run '" + name +
			"' --cron >> '/tmp/cron-" + name + ".log' 2>&1",
		cron.BlockEnd,
	}, "\n") + "\n"
}

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

	// One seeded script is scheduled, in PS spelling, through a fake crontab
	// both sides read: --list's schedule column has to agree byte-for-byte,
	// not just be empty on both sides.
	//
	// It must be 'pytool', NOT 'hello': a */5 schedule makes a script
	// "frequent", and the now-live retention rule drops a frequent script's
	// success rows older than a day — which would delete the one --history
	// fixture row carrying the one-decimal cpu/mem and fractional-duration
	// values before either side renders it. Both sides prune identically, so
	// that loss would pass silently. pytool's row is a failure, immune to the
	// rule. The decimal assertion below is the tripwire if this ever moves.
	shimCrontab(t, psManagedBlock("pytool", "*/5 * * * *"))

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

			// An identically-empty column would diff clean while proving
			// nothing — assert the shim actually reached both readers.
			if args[0] == "--list" {
				if !strings.Contains(goOut.String(), "  [*/5 * * * *]") {
					t.Errorf("Go --list has no schedule column — the crontab shim never reached it:\n%s", goOut.String())
				}
				if !strings.Contains(string(psOut), "  [*/5 * * * *]") {
					t.Errorf("PS --list has no schedule column — the crontab shim never reached pwsh:\n%s", psOut)
				}
			}

			// The seed row with one-decimal cpu/mem and a fractional duration
			// is the whole point of the --history half of this oracle. The
			// startup prune can delete a row on BOTH sides and leave the diff
			// clean, so its survival is asserted, not assumed.
			if args[0] == "--history" {
				want := fmt.Sprintf("%8s  cpu %5v%%  mem %7vMB", "12.3s", "45.6", "128.5")
				if !strings.Contains(goOut.String(), want) {
					t.Errorf("the one-decimal fixture row is gone from --history (pruned before rendering?) — want %q in:\n%s", want, goOut.String())
				}
			}
		})
	}
}

// ---------------------------------------------------------------------
// 2b. --list schedule column, without pwsh: exactly PS's bracket shape,
// and only on the scheduled script.
// ---------------------------------------------------------------------

func TestListScheduleColumn(t *testing.T) {
	_, dataDir := setupApp(t)
	writeScript(t, dataDir, "scheduled", "exit 0")
	writeScript(t, dataDir, "unscheduled", "exit 0")
	shimCrontab(t, psManagedBlock("scheduled", "*/5 * * * *"))

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--list"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	want := fmt.Sprintf("%-30s %-3s %-10s%s\n", "scheduled", "ps", "never run", "  [*/5 * * * *]")
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout =\n%q\nwant to contain\n%q", out.String(), want)
	}
	unwanted := fmt.Sprintf("%-30s %-3s %-10s\n", "unscheduled", "ps", "never run")
	if !strings.Contains(out.String(), unwanted) {
		t.Errorf("unscheduled script should carry no column:\n%q", out.String())
	}
}

// ---------------------------------------------------------------------
// 3. The missed-run sweep piggybacks on --run, now with real schedules.
// ---------------------------------------------------------------------

func TestRunMissedSweepDoesNotBreakTheRun(t *testing.T) {
	pwshtest.RequirePwsh(t)
	_, dataDir := setupApp(t)
	writeScript(t, dataDir, "ok", "exit 0")

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--run", "ok"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stdout: %s, stderr: %s", code, out.String(), errw.String())
	}
	// An empty crontab is a real sweep over zero schedules: the state file
	// is created and stays empty. (nil Schedules — "no reader" — is gone.)
	b, err := os.ReadFile(filepath.Join(dataDir, "missed-state.json"))
	if err != nil {
		t.Fatalf("expected an empty missed-state.json after the sweep: %v", err)
	}
	if s := strings.TrimSpace(string(b)); s != "" && s != "{}" {
		t.Errorf("missed-state.json = %q, want empty for an empty crontab", s)
	}
}

// End-to-end: a schedule that fired and left no trace alerts exactly once
// through the webhook, and the alert is stamped so it never repeats.
func TestRunMissedSweepAlertsOnASilentSchedule(t *testing.T) {
	pwshtest.RequirePwsh(t)

	var mu sync.Mutex
	var events []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			mu.Lock()
			events = append(events, m)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"dataDir":%q,"missedGraceMinutes":0}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)
	t.Setenv("N8N_WEBHOOK_URL", srv.URL)
	writeScript(t, dataDir, "other", "exit 0")
	shimCrontab(t, psManagedBlock("job", "*/5 * * * *"))

	// 'job' has been known for two hours, and has never been alerted on.
	// The state file speaks .NET 'o' format in naive-local labeling.
	seen := time.Now().Add(-2*time.Hour).Format("2006-01-02T15:04:05.0000000") + "Z"
	state := fmt.Sprintf(`{"job":{"expr":"*/5 * * * *","firstSeen":%q,"lastAlerted":null}}`, seen)
	if err := os.WriteFile(filepath.Join(dataDir, "missed-state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--run", "other"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stdout: %s, stderr: %s", code, out.String(), errw.String())
	}

	mu.Lock()
	defer mu.Unlock()
	var missedEvents []map[string]any
	for _, e := range events {
		if e["event"] == "missed" {
			missedEvents = append(missedEvents, e)
		}
	}
	if len(missedEvents) != 1 {
		t.Fatalf("missed webhooks = %d, want exactly 1 (all events: %v)", len(missedEvents), events)
	}
	if missedEvents[0]["script"] != "job" {
		t.Errorf("script = %v, want \"job\"", missedEvents[0]["script"])
	}
	if missedEvents[0]["schedule"] != "*/5 * * * *" {
		t.Errorf("schedule = %v, want \"*/5 * * * *\"", missedEvents[0]["schedule"])
	}

	raw, err := os.ReadFile(filepath.Join(dataDir, "missed-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]struct {
		LastAlerted *string `json:"lastAlerted"`
	}
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if after["job"].LastAlerted == nil || *after["job"].LastAlerted == "" {
		t.Errorf("lastAlerted was not stamped: %s", raw)
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
// --version (Go-only; no PS counterpart)
// ---------------------------------------------------------------------

func TestVersionFlag(t *testing.T) {
	setupApp(t)
	restore := stubBuildinfo("v1.2.3", "abcdef1", "2026-09-02T00:00:00Z")
	defer restore()

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--version"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	want := "scriptorium v1.2.3 (commit abcdef1, built 2026-09-02T00:00:00Z)\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

// ---------------------------------------------------------------------
// ResolveAppDir's fallback chain — the runtime counterpart of install.sh's
// own app-dir resolution, so a bare `scriptorium` invocation after a fresh
// install finds what install.sh just bootstrapped.
// ---------------------------------------------------------------------

func TestResolveAppDirFallbackChain(t *testing.T) {
	t.Setenv("SCRIPTORIUM_APP_DIR", "")
	t.Setenv("PSSCRIPTS_APP_DIR", "")

	t.Run("PSSCRIPTS_APP_DIR wins when SCRIPTORIUM_APP_DIR is unset", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PSSCRIPTS_APP_DIR", dir)
		if got := cli.ResolveAppDir(); got != dir {
			t.Errorf("ResolveAppDir() = %q, want %q", got, dir)
		}
	})

	t.Run("defaults to ~/scriptorium when nothing else applies", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir()) // no config.json here
		want := filepath.Join(home, "scriptorium")
		if got := cli.ResolveAppDir(); got != want {
			t.Errorf("ResolveAppDir() = %q, want %q", got, want)
		}
	})

	t.Run("cwd with config.json wins over the ~/scriptorium default (cron's cd)", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(cwd)
		if got := cli.ResolveAppDir(); got != cwd {
			t.Errorf("ResolveAppDir() = %q, want %q", got, cwd)
		}
	})
}

// ---------------------------------------------------------------------
// --migrate — the ONLY crontab-rewrite entry point (ruling 3)
// ---------------------------------------------------------------------

func TestMigrateFlow(t *testing.T) {
	_, dataDir := setupApp(t)
	shimMutableCrontab(t, psManagedBlock("fast-job", "*/10 * * * *"))

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--migrate"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	got := out.String()
	for _, want := range []string{
		"current managed block:",
		"block to write:",
		"crontab backed up to: " + dataDir,
		"migrated: the managed block now invokes this binary directly",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\ngot:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "-NoProfile -File") {
		t.Errorf("current block should still show the pre-migration PS spelling:\n%s", got)
	}
	if idx := strings.Index(got, "block to write:"); idx < 0 || strings.Contains(got[idx:], "-NoProfile -File") {
		t.Errorf("planned block should no longer show PS spelling:\n%s", got)
	}

	backups, err := filepath.Glob(filepath.Join(dataDir, "crontab.bak.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup files = %v, %v, want exactly one", backups, err)
	}

	// idempotent second run
	out.Reset()
	errw.Reset()
	if code := cli.Main([]string{"--migrate"}, &out, &errw); code != 0 {
		t.Fatalf("second run: exit = %d, stderr: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "already migrated — nothing to do") {
		t.Errorf("second run stdout = %q, want \"already migrated\"", out.String())
	}
	backups2, err := filepath.Glob(filepath.Join(dataDir, "crontab.bak.*"))
	if err != nil || len(backups2) != 1 {
		t.Errorf("backup files after second run = %v, %v, want still exactly one", backups2, err)
	}
}

func TestMigrateFailedReadIsWipeGuardError(t *testing.T) {
	setupApp(t)
	dir := t.TempDir()
	// exits nonzero WITH stdout content — cron.Crontab.Read's genuine
	// read-failure shape (empty stdout + nonzero exit reads as "no crontab
	// for user" instead, which is not what this test is proving).
	script := "#!/bin/sh\necho 'crontab: permission denied'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--migrate"}, &out, &errw); code != 1 {
		t.Fatalf("exit = %d, want 1; stdout: %s, stderr: %s", code, out.String(), errw.String())
	}
	want := "crontab read failed — refusing to migrate (unmanaged entries would be destroyed)"
	if !strings.Contains(errw.String(), want) {
		t.Errorf("stderr = %q, want to contain %q", errw.String(), want)
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want nothing printed on a wipe-guard failure", out.String())
	}
}

func TestMigrateWithNoManagedBlockReportsAlreadyMigrated(t *testing.T) {
	_, dataDir := setupApp(t)
	shimMutableCrontab(t, "MAILTO=someone@example.com\n15 3 * * * /usr/local/bin/certbot renew\n")

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--migrate"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "already migrated — nothing to do") {
		t.Errorf("stdout = %q, want \"already migrated\"", out.String())
	}
	if !strings.Contains(out.String(), "current managed block:\n  (none)") {
		t.Errorf("stdout = %q, want the current block shown as (none)", out.String())
	}
	if backups, _ := filepath.Glob(filepath.Join(dataDir, "crontab.bak.*")); len(backups) != 0 {
		t.Errorf("backup files = %v, want none — nothing to migrate", backups)
	}
}
