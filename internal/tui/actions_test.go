package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"

	"github.com/charmbracelet/colorprofile"
)

// ---------------------------------------------------------------------------
// A recording stub interpreter
// ---------------------------------------------------------------------------

// toolApp is a runnable fixture whose pwsh is a stub that RECORDS what it was
// asked to do: "scan" for a dependency scan, "cmd" for a generated -Command
// (install / lint / upgrade), "run" for an actual script run. The order of
// those calls is the evidence for the deps-modal sequence.
type toolApp struct {
	*app.App
	calls  string // the recording file
	scan   string // the JSON the stub answers a dependency scan with
	script scripts.Script
}

func (ta *toolApp) recorded(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(ta.calls)
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func seedToolApp(t *testing.T, body string) *toolApp {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	scriptDir := filepath.Join(dataDir, "scripts", "tool-test")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(scriptDir, "tool-test.ps1"), body)

	calls := filepath.Join(appDir, "calls.log")
	scan := filepath.Join(appDir, "scan.json")
	write(t, scan, `{"deps":[],"missing":[],"params":[]}`)

	stub := filepath.Join(appDir, "stub-pwsh")
	write(t, stub, fmt.Sprintf(`#!/bin/sh
file=
while [ $# -gt 0 ]; do
  case "$1" in
    -File) shift; file=$1; shift; break;;
    -Command) shift; printf 'cmd\n' >> %[1]q; printf 'stub: ran a generated command\n'; exit 0;;
    *) shift;;
  esac
done
case "$file" in
  *deps-scanner*) printf 'scan\n' >> %[1]q; cat %[2]q; exit 0;;
esac
printf 'run\n' >> %[1]q
exec /bin/sh "$file" "$@"
`, calls, scan))
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(appDir, "config.json"),
		fmt.Sprintf(`{"dataDir":%q,"pwshBin":%q}`, dataDir, stub))

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
	return &toolApp{App: a, calls: calls, scan: scan}
}

// toolModel is a root model over the recording fixture, on the Run view.
func toolModel(t *testing.T, ta *toolApp) *Model {
	t.Helper()
	m := New(ta.App, fixedNow)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(m.loadFleet()())
	m.mode = modeRun
	m.relayout()
	if len(m.scripts) != 1 {
		t.Fatalf("fixture discovered %d scripts", len(m.scripts))
	}
	ta.script = m.scripts[0]
	return m
}

// ---------------------------------------------------------------------------
// a — extra args
// ---------------------------------------------------------------------------

// `a` prompts, splits quote-aware (the runner's own splitter, so the TUI and
// the CLI agree), and starts the run with what it got.
func TestArgsPromptSplitsQuoteAware(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.selectByName(m, "backup-db")
	// something is already running, so the run queues instead of launching —
	// which is what lets this assert the ARGUMENTS without executing anything
	m.run.handle = fakeHandle("heartbeat")

	press(m, "a")
	if m.ov == nil || m.ov.kind() != overlayInput {
		t.Fatalf("`a` opened %T, want the input overlay", m.ov)
	}
	for _, k := range strings.Split(`--full "two words"`, "") {
		press(m, k)
	}
	send(m, keyMsg("enter"))
	if m.ov != nil {
		t.Error("enter left the prompt open")
	}
	if len(m.run.queue) != 1 {
		t.Fatalf("queue = %+v, want the args run", m.run.queue)
	}
	got := m.run.queue[0]
	if got.Name != "backup-db" || strings.Join(got.Args, "|") != "--full|two words" {
		t.Errorf("queued %+v, want backup-db with [--full, two words]", got)
	}
}

// ---------------------------------------------------------------------------
// i / the deps modal — controller resolution 3
// ---------------------------------------------------------------------------

// THE GATE: y on the deps modal installs, THEN invalidates the scan cache,
// THEN runs. The stub interpreter records every call it is asked to make, so
// the order is observed rather than asserted about the code.
func TestDepsModalInstallsInvalidatesThenRuns(t *testing.T) {
	ta := seedToolApp(t, "echo 'the script ran'\nexit 0\n")
	write(t, ta.scan, `{"deps":[{"name":"Az","display":"Az"}],`+
		`"missing":[{"name":"Az","display":"Az"}],"params":[]}`)
	m := toolModel(t, ta)

	// r → dependency scan → the modal, because something is missing
	pump(t, m, press(m, "r"), 20*time.Second, nil)
	if m.ov == nil || m.ov.kind() != overlayDeps {
		t.Fatalf("a missing dependency opened %T, want the deps overlay", m.ov)
	}
	if !strings.Contains(plainFrame(m), "Az") {
		t.Errorf("the missing module is not named on screen:\n%s", plainFrame(m))
	}
	if got := ta.recorded(t); strings.Join(got, ",") != "scan" {
		t.Fatalf("calls before answering = %v, want just the scan", got)
	}

	// the modal is modal: the queue waits under it
	if m.queueUnblocked() {
		t.Error("the deps modal let the queue drain underneath it")
	}

	// y → install, then run
	pump(t, m, press(m, "y"), 60*time.Second, nil)
	if m.ov != nil {
		t.Errorf("y left %T open", m.ov)
	}
	if got := strings.Join(ta.recorded(t), ","); got != "scan,cmd,run" {
		t.Fatalf("call order = %q, want scan,cmd,run (install BEFORE the run)", got)
	}
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"stub: ran a generated command", "install deps: tool-test · done", "the script ran"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output pane is missing %q:\n%s", want, out)
		}
	}

	// …and the scan cache was invalidated: a fresh scan of the same unchanged
	// entry has to shell out again instead of serving the pre-install answer.
	before := len(ta.recorded(t))
	if _, err := ta.Scanner.ScanPS(ta.script.Entry, ta.script.Dir, ta.script.ModuleDir, ta.script.Loose); err != nil {
		t.Fatal(err)
	}
	after := ta.recorded(t)
	if len(after) != before+1 || after[len(after)-1] != "scan" {
		t.Errorf("the scan cache was NOT invalidated after the install: calls = %v", after)
	}
}

// n runs the script anyway; esc cancels and runs nothing.
func TestDepsModalRunAnywayAndCancel(t *testing.T) {
	ta := seedToolApp(t, "echo 'ran regardless'\nexit 0\n")
	write(t, ta.scan, `{"deps":[],"missing":[{"name":"Az","display":"Az"}],"params":[]}`)
	m := toolModel(t, ta)

	pump(t, m, press(m, "r"), 20*time.Second, nil)
	pump(t, m, press(m, "esc"), 20*time.Second, nil)
	if got := strings.Join(ta.recorded(t), ","); got != "scan" {
		t.Errorf("esc still ran something: %q", got)
	}
	if !strings.Contains(m.statusText, "cancelled") {
		t.Errorf("status = %q, want cancelled", m.statusText)
	}

	// the second `r` re-opens the modal WITHOUT a second scan: the scanner
	// caches on the entry's (size, mtime), which nothing has touched
	pump(t, m, press(m, "r"), 20*time.Second, nil)
	pump(t, m, press(m, "n"), 60*time.Second, nil)
	if got := strings.Join(ta.recorded(t), ","); got != "scan,run" {
		t.Errorf("call order = %q, want a cached scan then a run with no install", got)
	}
	if !strings.Contains(strings.Join(m.run.out.buf.Lines, "\n"), "ran regardless") {
		t.Error("n did not run the script anyway")
	}
}

// `i` is the install-only path: it reports, and never runs the script.
func TestDepScanKeyReportsAndNeverRuns(t *testing.T) {
	ta := seedToolApp(t, "echo 'must not run'\n")
	m := toolModel(t, ta)

	pump(t, m, press(m, "i"), 20*time.Second, nil)
	if m.ov != nil {
		t.Errorf("nothing is missing, but %T opened", m.ov)
	}
	if got := strings.Join(ta.recorded(t), ","); got != "scan" {
		t.Errorf("calls = %q, want just the scan", got)
	}
	out := strings.Join(m.run.out.buf.Lines, "\n")
	if !strings.Contains(out, "dependency scan: tool-test") || !strings.Contains(out, "declared missing: (none)") {
		t.Errorf("the scan said nothing useful:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// e — the .env editor
// ---------------------------------------------------------------------------

// The editor writes the buffer back verbatim (comments, order and blank lines
// survive because nothing re-serialises them), and every saved value is
// registered as a secret BEFORE anything can render it.
func TestEnvEditorSavesVerbatimAndRegistersSecrets(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)
	envFile := m.scripts[0].EnvFile
	write(t, envFile, "# the API this talks to\nAPI_TOKEN=old-value-here\n\nDEBUG=1\n")

	press(m, "e")
	ed, ok := m.ov.(*envOverlay)
	if !ok {
		t.Fatalf("`e` opened %T, want the env editor", m.ov)
	}
	if ed.dirty() {
		t.Error("a freshly opened editor is already dirty")
	}

	// esc with unsaved changes warns first and only discards on the second
	ed.ta.SetValue("# the API this talks to\nAPI_TOKEN=fresh-secret-value\n\nDEBUG=1")
	send(m, keyMsg("esc"))
	if m.ov == nil {
		t.Fatal("the first esc discarded unsaved changes without warning")
	}
	if !strings.Contains(m.statusText, "esc again to discard") {
		t.Errorf("status = %q", m.statusText)
	}

	send(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.ov != nil {
		t.Error("ctrl+s left the editor open")
	}
	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "# the API this talks to\nAPI_TOKEN=fresh-secret-value\n\nDEBUG=1\n"
	if string(got) != want {
		t.Errorf("saved file = %q, want %q", got, want)
	}
	if !strings.Contains(m.statusText, "saved .env for tool-test") {
		t.Errorf("status = %q", m.statusText)
	}
	// the redaction registry has the new value, FORCED — its key name would
	// not have passed the secret-ish name gate on its own
	if red := ta.Sec.Redact("token is fresh-secret-value ok"); strings.Contains(red, "fresh-secret-value") {
		t.Errorf("a value saved in the .env editor is not redacted: %q", red)
	}
}

// ---------------------------------------------------------------------------
// l / u / v — streamed tools and the log
// ---------------------------------------------------------------------------

func TestLintStreamsIntoThePane(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)

	pump(t, m, press(m, "l"), 30*time.Second, nil)
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"▶ lint: tool-test", "stub: ran a generated command", "lint: tool-test · done"} {
		if !strings.Contains(out, want) {
			t.Errorf("the lint output is missing %q:\n%s", want, out)
		}
	}
	if got := strings.Join(ta.recorded(t), ","); got != "cmd" {
		t.Errorf("lint made calls %q, want one generated command", got)
	}
}

// u chains the two upgrade stages: modules, then venvs.
func TestSystemUpdateChainsBothStages(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)
	// u now probes for passwordless sudo first (wave B3's restored apt
	// stage); an unshimmed PATH would ask the REAL sudo on whatever machine
	// runs this test, which is exactly the non-hermetic outcome the floor
	// tests (floor_test.go) exist to prevent. No "sudo" in this shim dir at
	// all — the probe fails to resolve it, same as TestSystemUpdateSkips
	// AptStageWithoutSudo, so this test's own assertions (modules, venvs)
	// stay focused on what it was already testing.
	shimPath(t, map[string]string{})

	pump(t, m, press(m, "u"), 30*time.Second, nil)
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"upgrade script modules · done", "upgrade python venvs · done"} {
		if !strings.Contains(out, want) {
			t.Errorf("the upgrade output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(m.statusText, "modules and venvs upgraded") {
		t.Errorf("status = %q", m.statusText)
	}
}

// v reads the selected script's newest log back into the pane.
func TestViewLogTailsTheNewestLog(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.selectByName(m, "backup-db")

	logFile := filepath.Join(t.TempDir(), "backup-db.log")
	var b strings.Builder
	for i := range 40 {
		fmt.Fprintf(&b, "log line %d\n", i)
	}
	write(t, logFile, b.String())
	for i := range m.recent {
		if m.recent[i].Script == "backup-db" {
			m.recent[i].LogFile = &logFile
		}
	}

	send(m, keyMsg("v"))
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"▶ log: " + logFile, "log line 0", "log line 39"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log tail is missing %q:\n%s", want, out)
		}
	}

	// a script with no log says so rather than opening an empty pane
	m.run.selectByName(m, "cleanup-tmp")
	if msg, ok := cmdMsg(press(m, "v")).(StatusMsg); !ok || !strings.Contains(msg.Text, "no log for cleanup-tmp") {
		t.Errorf("v on a never-run script gave %#v", msg)
	}
}

// The tail keeps the LAST n lines, in order, however many the file has — a run
// log is unbounded and the pane only ever shows n of them.
func TestTailLogKeepsTheLastLinesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	write(t, path, b.String())

	msg, ok := cmdMsg(tailLog(path, 5)).(LogLoadedMsg)
	if !ok || msg.Err != nil {
		t.Fatalf("tailLog gave %#v", msg)
	}
	if got := strings.Join(msg.Lines, ","); got != "line 395,line 396,line 397,line 398,line 399" {
		t.Errorf("tail = %q", got)
	}
	// a file shorter than the tail comes back whole
	short, _ := cmdMsg(tailLog(path, 1000)).(LogLoadedMsg)
	if len(short.Lines) != 400 || short.Lines[0] != "line 0" {
		t.Errorf("short tail = %d lines starting %q", len(short.Lines), short.Lines[0])
	}
}

// h deep-links into History scoped to the selected script (the scope wave B's
// view reads).
func TestHistoryDeepLinkCarriesTheScope(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.selectByName(m, "heartbeat")
	press(m, "h")
	if m.mode != modeHistory {
		t.Errorf("h did not switch to History: mode = %v", m.mode)
	}
	if m.historyScope != "heartbeat" {
		t.Errorf("history scope = %q, want heartbeat", m.historyScope)
	}
}

// ---------------------------------------------------------------------------
// x — killing a task, not just a run
// ---------------------------------------------------------------------------

// A sync is killable exactly like a run: `x` cancels the task's context, the
// child dies with it, and the drain runs on to the channel's close so nothing
// leaks.
func TestTaskIsKillable(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)

	started := make(chan struct{})
	cmd := m.run.startTask(m, "slow thing", func(ctx context.Context, emit func(string)) bool {
		emit("working…")
		close(started)
		<-ctx.Done() // a real child would be killed by CommandContext here
		emit("cancelled")
		return false
	}, nil)
	if m.run.task == nil {
		t.Fatal("the task did not start")
	}

	killed := false
	pump(t, m, cmd, 20*time.Second, func() {
		if killed {
			return
		}
		select {
		case <-started:
			killed = true
			m.Update(cmdMsg(m.run.kill(m)))
		default:
		}
	})
	if !killed {
		t.Fatal("the task never reported starting")
	}
	if m.run.task != nil {
		t.Error("the killed task never released its slot")
	}
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"working…", "cancelled", "slow thing · killed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the killed task's output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(m.statusText, "killed slow thing") {
		t.Errorf("status = %q", m.statusText)
	}
}

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

func TestGoldensActionOverlays(t *testing.T) {
	goldenFrames(t, "overlay-deps", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.run.selectByName(m, "backup-db")
		m.Update(DepsScannedMsg{
			Script: m.scripts[0],
			Missing: []deps.Dep{
				{Name: "Az.Accounts", Display: "Az.Accounts (>=2.0.0)"},
				{Name: "PSSQLite", Display: "PSSQLite"},
			},
		})
		return m
	})

	goldenFrames(t, "overlay-env", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.run.selectByName(m, "backup-db")
		s := m.scripts[0]
		write(t, s.EnvFile, "# where the dump goes\nS3_BUCKET=ops-backups\n\n# rotate weekly\nRETENTION_DAYS=7\n")
		press(m, "e")
		return m
	})
}
