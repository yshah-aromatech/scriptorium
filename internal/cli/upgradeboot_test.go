// upgradeboot_test.go is the "upgrade-VM" gate (hermetic approximation,
// design §6 P12's gate row): a data dir built from the PS-era fixtures
// under testdata/psfixtures/, with a LIVE foreign lock (a real spawned
// process, PID written in the PS bare-ASCII format) standing in for a
// script that is still running when this binary boots for the first time
// against real production data. Every read path has to see it correctly:
// --list, --history, a run attempt against the locked script, and the
// TUI's own boot. Plus the opposite case — a completely fresh, empty app
// dir must boot with no error at all.
package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cli"
	"github.com/yshah-aromatech/scriptorium/internal/tui"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

func psFixturesDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "testdata", "psfixtures")
}

// buildUpgradeDataDir seeds a data dir the way a real PS-era ~/.scriptorium
// looks on first Go boot: mixed-era history (five rows spanning old PS
// field shapes through a fresh Go-written one, plus one deliberately
// malformed line), a queued webhook, missed-fire state, and a crontab
// (served by a shim, never the real crontab) with a schedule for
// "fast-job" — which also gets a real discoverable script folder, so every
// read path (discovery, history, cron) has something to agree on.
func buildUpgradeDataDir(t *testing.T) (appDir, dataDir string) {
	t.Helper()
	appDir, dataDir = setupApp(t)
	fx := psFixturesDir(t)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFixture := func(name, dest string) {
		b, err := os.ReadFile(filepath.Join(fx, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, dest), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyFixture("history-mixed.jsonl", "history.jsonl")
	copyFixture("webhook-queue.jsonl", "webhook-queue.jsonl")
	copyFixture("missed-state.json", "missed-state.json")

	writeScript(t, dataDir, "fast-job", "exit 0")

	cronFixture, err := os.ReadFile(filepath.Join(fx, "crontab", "current.txt"))
	if err != nil {
		t.Fatal(err)
	}
	shimCrontab(t, string(cronFixture))
	return appDir, dataDir
}

// spawnForeignLock starts a real process and writes its PID in the PS lock
// format (bare ASCII, nothing else) — a live foreign lock, the same fixture
// shape as internal/lockfile's own TestForeignLiveLockBlocks, reused here
// at the CLI/TUI boundary instead of the package boundary.
func spawnForeignLock(t *testing.T, dataDir, name string) {
	t.Helper()
	locksDir := filepath.Join(dataDir, "locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(locksDir, name+".lock"), []byte(pid), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeBootGate(t *testing.T) {
	appDir, dataDir := buildUpgradeDataDir(t)
	spawnForeignLock(t, dataDir, "fast-job")

	t.Run("--list shows the running, scheduled script", func(t *testing.T) {
		var out, errw bytes.Buffer
		if code := cli.Main([]string{"--list"}, &out, &errw); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, errw.String())
		}
		if !strings.Contains(out.String(), "fast-job") {
			t.Errorf("--list is missing the locked script:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "[*/10 * * * *]") {
			t.Errorf("--list is missing fast-job's crontab-fixture schedule:\n%s", out.String())
		}
	})

	t.Run("--history renders the mixed-era rows", func(t *testing.T) {
		var out, errw bytes.Buffer
		if code := cli.Main([]string{"--history"}, &out, &errw); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, errw.String())
		}
		for _, want := range []string{"old-a", "old-b", "old-c", "old-d", "fixture-script"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("--history is missing the mixed-era row %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("a run attempt against the locked script is skipped", func(t *testing.T) {
		var out, errw bytes.Buffer
		code := cli.Main([]string{"--run", "fast-job"}, &out, &errw)
		if code != 3 {
			t.Fatalf("exit = %d, want 3 (skipped)\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
		}
		if !strings.Contains(out.String(), "skipped: fast-job is already running (pid ") {
			t.Errorf("stdout is missing the skip note:\n%s", out.String())
		}
	})

	t.Run("TUI boot shows the running badge", func(t *testing.T) {
		a, err := app.Open(appDir)
		if err != nil {
			t.Fatal(err)
		}
		m := tui.New(a, time.Now)

		tm := teatest.NewTestModel(t, m,
			teatest.WithInitialTermSize(120, 40),
			teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))

		teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
			frame := textkit.StripANSI(string(b))
			return strings.Contains(frame, "fast-job") && strings.Contains(frame, "▶")
		}, teatest.WithDuration(10*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

		tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
		tm.WaitFinished(t, teatest.WithFinalTimeout(10*time.Second))
	})
}

// TestFreshBootNoError is the gate's other half: a brand new, empty app dir
// (no config.json, no history, no crontab block, nothing) must boot clean —
// the states this exercises are already golden-tested (TestFleetGoldens'
// "fleet-empty" case) and install.sh's own happy path (hack/install-test)
// covers the config-bootstrap message; this is the headless CLI's own
// confirmation that a fresh boot produces no error and no spurious output.
func TestFreshBootNoError(t *testing.T) {
	appDir := t.TempDir()
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)
	t.Setenv("N8N_WEBHOOK_URL", "")
	shimCrontab(t, "")

	var out, errw bytes.Buffer
	if code := cli.Main([]string{"--list"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.String())
	}
	if out.String() != "" {
		t.Errorf("a fresh boot with no scripts should print nothing, got:\n%s", out.String())
	}
	if errw.String() != "" {
		t.Errorf("a fresh boot must not warn about anything, got:\n%s", errw.String())
	}
}
