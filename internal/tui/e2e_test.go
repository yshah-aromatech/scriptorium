package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// stubPwsh writes a fake `pwsh` that understands just enough of the runner's
// invocation to run the script body with /bin/sh: -NoProfile -NonInteractive
// -File <entry> [args...]. Real PowerShell is not needed to prove the TUI's run
// wiring, and a test that needed it would not run on CI.
func stubPwsh(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "stub-pwsh")
	body := "#!/bin/sh\n" +
		"file=\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"-File\" ]; then shift; file=$1; shift; break; fi\n" +
		"  shift\n" +
		"done\n" +
		"exec /bin/sh \"$file\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedRunnableApp is a second, minimal fixture whose scripts actually execute:
// one app dir, a stub interpreter, an injected crontab runner that serves an
// empty block and refuses writes, and no webhook URL.
func seedRunnableApp(t *testing.T, scripts map[string]string) *app.App {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	scriptsDir := filepath.Join(dataDir, "scripts")

	for name, body := range scripts {
		dir := filepath.Join(scriptsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, name+".ps1"), body)
	}
	write(t, filepath.Join(appDir, "config.json"), fmt.Sprintf(
		`{"dataDir":%q,"pwshBin":%q}`, dataDir, stubPwsh(t, appDir)))

	a, err := app.OpenWith(appDir, func(_ string, args ...string) (string, bool) {
		if len(args) == 1 && args[0] == "-l" {
			return cron.BlockStart + "\n" + cron.BlockEnd + "\n", true
		}
		t.Errorf("the TUI must not write the crontab (args %v)", args)
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// THE GATE: a headless key sequence that runs a real script end to end.
// Navigate to the Run view, pick the script, press r, wait for the run to
// finish, and check all three places the result has to land — the output pane,
// history on disk, and the status bar.
func TestHeadlessFullRun(t *testing.T) {
	a := seedRunnableApp(t, map[string]string{
		"greeter": "echo 'hello from the stub'\necho 'second line'\nexit 0\n",
	})
	m := New(a, time.Now)
	// ASCII profile so the assertions read the frame's text rather than its SGR
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	// Apply the fleet load before the program starts. Init() does it again, but
	// doing it here removes the one race a key-sequence test cannot control:
	// a keystroke that arrives before discovery has nothing to select, and the
	// run silently never happens.
	m.Update(m.loadFleet()())

	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(120, 40),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)))

	tm.Send(keyMsg("2")) // Run view
	tm.Send(keyMsg("r")) // run the selected script

	// One wait, on the last thing the run produces: chaining several WaitFors
	// over the same output stream races with its own reader.
	//
	// Matched against the ESCAPE-STRIPPED stream. The renderer repaints only
	// the cells that changed and puts a cursor move between runs of them, so
	// whether a given string lands contiguously in the raw bytes depends on
	// what the previous frame looked like — and that depends on the colour
	// profile, i.e. on the environment. Stripping first is what makes this
	// assertion about the UI rather than about the terminal it was drawn for.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(textkit.StripANSI(string(b)), "greeter: success")
	}, teatest.WithDuration(60*time.Second), teatest.WithCheckInterval(20*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(10*time.Second))

	final, ok := tm.FinalModel(t).(*Model)
	if !ok {
		t.Fatal("final model is not *Model")
	}

	// 1. the output pane holds the script's lines and the completion banner
	out := strings.Join(final.run.out.buf.Lines, "\n")
	for _, want := range []string{"hello from the stub", "second line", "greeter · success · exit 0", "log:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output pane is missing %q:\n%s", want, out)
		}
	}

	// 2. history has the row, on disk, with the fields the pipeline promises
	rows, err := a.Hist.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		if row.Script != "greeter" {
			continue
		}
		found = true
		if row.Status != "success" || row.ExitCode == nil || *row.ExitCode != 0 {
			t.Errorf("history row = %+v, want success/exit 0", row)
		}
		if row.Trigger != "manual" {
			t.Errorf("trigger = %q, want manual", row.Trigger)
		}
		if row.LogFile == nil || *row.LogFile == "" {
			t.Error("history row has no log file")
		}
	}
	if !found {
		t.Fatalf("no history row for greeter: %+v", rows)
	}

	// 3. the status bar reported it, and the list badge caught up
	if !strings.Contains(final.statusText, "greeter: success") {
		t.Errorf("status = %q", final.statusText)
	}
	if got := final.statuses["greeter"].Status; got != "success" {
		t.Errorf("the fleet refresh did not land: status = %q", got)
	}
	if final.run.handle != nil {
		t.Error("the run handle was not released")
	}
}

// A failing script lands as a failure everywhere, not as an error dialog.
func TestHeadlessFailingRun(t *testing.T) {
	a := seedRunnableApp(t, map[string]string{
		"breaker": "echo 'about to fail'\nexit 3\n",
	})
	m := New(a, time.Now)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(m.loadFleet()())
	m.mode = modeRun

	drive(t, m, m.run.start(m, m.scripts[0]), 30*time.Second)

	if got := m.statusText; !strings.Contains(got, "breaker: failure") {
		t.Errorf("status = %q", got)
	}
	out := strings.Join(m.run.out.buf.Lines, "\n")
	if !strings.Contains(out, "breaker · failure · exit 3") {
		t.Errorf("output pane:\n%s", out)
	}
}

// A run whose lock is already held is a SKIPPED run — classified, appended to
// history, reported — not an error.
func TestHeadlessLockSkip(t *testing.T) {
	a := seedRunnableApp(t, map[string]string{"slowpoke": "sleep 5\n"})
	m := New(a, time.Now)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(m.loadFleet()())
	m.mode = modeRun

	// plant a foreign lock the same way another process would
	if err := os.MkdirAll(a.Paths.LocksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(a.Paths.LocksDir, "slowpoke.lock"), fmt.Sprint(os.Getpid()))

	drive(t, m, m.run.start(m, m.scripts[0]), 15*time.Second)

	out := strings.Join(m.run.out.buf.Lines, "\n")
	if !strings.Contains(out, "already running") {
		t.Errorf("a lock skip should say so in the output pane:\n%s", out)
	}
	if !strings.Contains(out, "slowpoke · skipped") {
		t.Errorf("a lock skip should be classified as skipped:\n%s", out)
	}
}

// x kills a long run, and the result is classified as killed within the grace.
func TestHeadlessKill(t *testing.T) {
	a := seedRunnableApp(t, map[string]string{"sleeper": "echo started\nsleep 120\n"})
	m := New(a, time.Now)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(m.loadFleet()())
	m.mode = modeRun

	// let it start and prove it is alive, then kill it from the same path the
	// x key uses — the command wrapping Handle.Kill, never a bare call.
	killed := false
	pump(t, m, m.run.start(m, m.scripts[0]), 60*time.Second, func() {
		if killed || !strings.Contains(strings.Join(m.run.out.buf.Lines, "\n"), "started") {
			return
		}
		killed = true
		start := time.Now()
		if kc := m.run.kill(m); kc != nil {
			kc()
		}
		if took := time.Since(start); took > 8*time.Second {
			t.Errorf("kill blocked for %v, well past the 3s grace", took)
		}
	})
	if !killed {
		t.Fatal("the run never produced output, so it was never killed")
	}

	out := strings.Join(m.run.out.buf.Lines, "\n")
	if !strings.Contains(out, "sleeper · killed") {
		t.Errorf("the killed run was not classified as killed:\n%s", out)
	}
	if m.run.handle != nil {
		t.Error("the handle survived the kill")
	}
	if !strings.Contains(m.statusText, "sleeper: killed") {
		t.Errorf("status = %q", m.statusText)
	}
}

// ---------------------------------------------------------------------------
// A minimal update loop, for tests that need a real run without a terminal.
// ---------------------------------------------------------------------------

// pump is Bubble Tea's loop, minus the terminal: a work queue of commands, each
// executed and its message fed back to Update, with batches flattened into more
// work. It renders a frame per turn so a render panic surfaces here rather than
// in production, and it steps past spinner ticks (chasing a 10 Hz animation
// would only make the test sleep).
//
// It runs until the queue drains or the budget expires; each is called after
// every Update, for tests that need to act mid-run.
func pump(t *testing.T, m *Model, first tea.Cmd, budget time.Duration, each func()) {
	t.Helper()
	deadline := time.Now().Add(budget)
	pending := []tea.Cmd{first}
	for len(pending) > 0 && time.Now().Before(deadline) {
		cmd := pending[0]
		pending = pending[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			pending = append(pending, batch...)
			continue
		}
		if _, isFrame := msg.(FrameMsg); isFrame {
			continue // executing the 16 ms clock would sleep and self-perpetuate
		}
		_, next := m.Update(msg)
		pending = append(pending, next)
		_ = m.frame()
		if each != nil {
			each()
		}
	}
}

// drive pumps one run to completion.
func drive(t *testing.T, m *Model, cmd tea.Cmd, budget time.Duration) {
	t.Helper()
	pump(t, m, cmd, budget, nil)
	if m.run.handle != nil {
		t.Fatalf("the run did not finish within %v", budget)
	}
}
