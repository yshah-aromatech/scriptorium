package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/openrouter"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// schedAt is a fixture model on the Schedules view. Unlike History, the view
// needs no async load — it reads the same m.scripts/m.schedules/m.missed the
// fixture already loaded — so a plain mode switch is enough.
func schedAt(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	press(m, "4")
	return m
}

// Scheduled scripts sort by soonest fire first (the same ordering the Fleet
// agenda card uses: nightly-report 5m, heartbeat 10m, backup-db 11h);
// unscheduled scripts follow, alphabetically.
func TestScheduleAgendaSortsByNextFire(t *testing.T) {
	m := schedAt(t, 120, 40)
	rows := m.sched.rows(m)
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	want := []string{"nightly-report", "heartbeat", "backup-db", "cleanup-tmp", "sync-orders"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("agenda order = %v, want %v", names, want)
	}
	if rows[0].Expr == "" || !rows[0].HasNext {
		t.Errorf("nightly-report should be a scheduled row with a next fire: %+v", rows[0])
	}
	if rows[3].Expr != "" {
		t.Errorf("cleanup-tmp is unscheduled in the fixture: %+v", rows[3])
	}
}

// A missed schedule shows the missed note instead of a countdown — the same
// state Fleet's schedule glyph and the Run view's cron label read.
func TestScheduleMissedStatusShown(t *testing.T) {
	m := schedAt(t, 120, 40)
	frame := plainFrame(m)
	if !strings.Contains(frame, "⚠ missed") {
		t.Errorf("nightly-report's missed fire is not on screen:\n%s", frame)
	}
	if !strings.Contains(frame, "not scheduled — e adds one") {
		t.Errorf("an unscheduled script does not say it is addable:\n%s", frame)
	}
}

// ---------------------------------------------------------------------------
// The write path
// ---------------------------------------------------------------------------

// recordingCrontab is a stateful crontab fake: reads answer the current
// block, writes replace it and are recorded. No test in this package may
// touch a real crontab — this fake is what makes the write path testable at
// all without one.
type recordingCrontab struct {
	block    string
	writes   []string
	failRead bool
}

func (c *recordingCrontab) run(stdin string, args ...string) (string, bool) {
	switch {
	case len(args) == 1 && args[0] == "-l":
		if c.failRead {
			return "permission denied", false
		}
		return c.block, true
	case len(args) == 1 && args[0] == "-":
		c.writes = append(c.writes, stdin)
		c.block = stdin
		return "", true
	}
	return "", false
}

// seedSchedWriteApp is a small fixture with ONE already-scheduled script and
// ONE unscheduled one, over a crontab fake that actually accepts writes —
// the shared harness fixture's crontab refuses every write on purpose, which
// is right for every other test file and wrong for this one.
func seedSchedWriteApp(t *testing.T, tab *recordingCrontab) *app.App {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	scriptsDir := filepath.Join(dataDir, "scripts")
	for _, name := range []string{"backup-db", "no-schedule-yet"} {
		dir := filepath.Join(scriptsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, name+".ps1"), "# "+name+"\n")
	}
	write(t, filepath.Join(appDir, "config.json"), fmt.Sprintf(`{"dataDir":%q}`, dataDir))
	a, err := app.OpenWith(appDir, tab.run)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func schedWriteModel(t *testing.T, a *app.App) *Model {
	t.Helper()
	m := New(a, fixedNow)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(m.loadFleet()())
	m.mode = modeSchedules
	m.relayout()
	return m
}

func selectSchedRow(m *Model, name string) {
	for i, r := range m.sched.rows(m) {
		if r.Name == name {
			m.sched.sel = i
			return
		}
	}
}

// applyCmd runs cmd and feeds the message it produces back through Update,
// recursing through whatever command THAT produces (batched or not) — the
// edit flow is several hops deep (parse -> confirm -> write -> reload +
// status) and a test asserting the status line has to walk every one of them,
// the same way the real program's loop would.
//
// Each command is invoked EXACTLY once: unlike batchCmds (which calls cmd()
// just to check its type, then hands back the same unexecuted cmd for the
// non-batch case — fine for a re-run tick, wrong for a crontab write, which
// this would otherwise fire twice), this inspects the message it already
// has.
func applyCmd(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			applyCmd(m, c)
		}
		return
	}
	if msg == nil {
		return
	}
	_, next := m.Update(msg)
	applyCmd(m, next)
}

// A literal cron expression validates and writes with no network involved:
// the confirm names the exact expression, y writes it, and the crontab
// actually changes.
func TestScheduleEditLiteralCronConfirmsThenWrites(t *testing.T) {
	tab := &recordingCrontab{block: cron.BlockStart + "\n" + cron.BlockEnd + "\n"}
	m := schedWriteModel(t, seedSchedWriteApp(t, tab))
	selectSchedRow(m, "no-schedule-yet")

	press(m, "e")
	if m.ov == nil {
		t.Fatal("e did not open the schedule prompt")
	}
	for _, r := range "0 3 * * *" {
		press(m, string(r))
	}
	applyCmd(m, press(m, "enter"))
	if m.ov == nil || m.ov.kind() != overlayConfirm {
		t.Fatalf("a literal cron expression did not go straight to confirm: %T", m.ov)
	}
	if !strings.Contains(plainFrame(m), "schedule 'no-schedule-yet' as: 0 3 * * * ?") {
		t.Errorf("the confirm message is wrong:\n%s", plainFrame(m))
	}

	applyCmd(m, press(m, "y"))
	if !strings.Contains(m.statusText, "scheduled no-schedule-yet : 0 3 * * *") {
		t.Errorf("status = %q", m.statusText)
	}
	if len(tab.writes) != 1 || !strings.Contains(tab.writes[0], "no-schedule-yet") {
		t.Errorf("the crontab was not rewritten: %v", tab.writes)
	}
}

// Text that is neither a cron expression nor answerable (no API key
// configured) surfaces the PS app's own error string — controller ruling:
// "absent → the PS error string as a status message".
func TestScheduleEditNoAPIKeySurfacesThePSError(t *testing.T) {
	tab := &recordingCrontab{block: cron.BlockStart + "\n" + cron.BlockEnd + "\n"}
	m := schedWriteModel(t, seedSchedWriteApp(t, tab))
	selectSchedRow(m, "no-schedule-yet")

	press(m, "e")
	for _, r := range "every day at three am" {
		press(m, string(r))
	}
	applyCmd(m, press(m, "enter"))
	if !strings.Contains(m.statusText, "OPENROUTER_API_KEY is not set") {
		t.Errorf("status = %q", m.statusText)
	}
	if m.ov != nil {
		t.Error("a failed parse should not open a confirm")
	}
	if len(tab.writes) != 0 {
		t.Error("a failed parse must never write the crontab")
	}
}

// Natural language routes through cron.ToCron -> the injected ai function ->
// a real openrouter.Client against an httptest server, fenced reply and all.
func TestScheduleEditNaturalLanguageViaOpenRouter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` +
			"```\\n0 3 * * *\\n```" + `"}}]}`))
	}))
	defer srv.Close()

	tab := &recordingCrontab{block: cron.BlockStart + "\n" + cron.BlockEnd + "\n"}
	m := schedWriteModel(t, seedSchedWriteApp(t, tab))
	m.aiConvert = openrouter.New("test-key", "test-model").WithBaseURL(srv.URL).Convert
	selectSchedRow(m, "no-schedule-yet")

	press(m, "e")
	for _, r := range "every day at 3am" {
		press(m, string(r))
	}
	applyCmd(m, press(m, "enter"))
	if m.ov == nil || m.ov.kind() != overlayConfirm {
		t.Fatalf("the AI reply did not reach confirm: %T (status %q)", m.ov, m.statusText)
	}
	if !strings.Contains(plainFrame(m), "schedule 'no-schedule-yet' as: 0 3 * * * ?") {
		t.Errorf("the confirm message is wrong:\n%s", plainFrame(m))
	}
	applyCmd(m, press(m, "y"))
	if len(tab.writes) != 1 || !strings.Contains(tab.writes[0], "0 3 * * *") {
		t.Errorf("the crontab was not written with the AI's expression: %v", tab.writes)
	}
}

// Empty input removes WITHOUT a confirm (floor semantics) — the crontab
// changes immediately.
func TestScheduleEditEmptyRemovesWithoutConfirm(t *testing.T) {
	block := cron.BlockStart + "\n" +
		"0 2 * * * cd '/opt/scriptorium' && '/usr/local/bin/scriptorium' --run 'backup-db' --cron >> '/var/log/backup-db.log' 2>&1\n" +
		cron.BlockEnd + "\n"
	tab := &recordingCrontab{block: block}
	m := schedWriteModel(t, seedSchedWriteApp(t, tab))
	selectSchedRow(m, "backup-db")

	press(m, "e")
	in, ok := m.ov.(*inputOverlay)
	if !ok {
		t.Fatal("e did not open the input overlay")
	}
	if in.ti.Value() != "0 2 * * *" {
		t.Fatalf("the prompt was not pre-filled with the current expression: %q", in.ti.Value())
	}
	for range in.ti.Value() {
		press(m, "backspace")
	}
	applyCmd(m, press(m, "enter"))
	if m.ov != nil {
		t.Errorf("an empty submit opened %T instead of removing directly", m.ov)
	}
	if !strings.Contains(m.statusText, "schedule removed for backup-db") {
		t.Errorf("status = %q", m.statusText)
	}
	if len(tab.writes) != 1 || strings.Contains(tab.writes[0], "backup-db") {
		t.Errorf("backup-db is still in the rewritten crontab: %v", tab.writes)
	}
}

// A crontab that cannot be read is the wipe guard's job to refuse — Set and
// Remove both abort rather than writing over unknown contents, and the UI
// says so with PS's own status text.
func TestScheduleWriteGuardSurfacesAsStatus(t *testing.T) {
	tab := &recordingCrontab{failRead: true}
	m := schedWriteModel(t, seedSchedWriteApp(t, tab))
	selectSchedRow(m, "no-schedule-yet")

	press(m, "e")
	for _, r := range "0 3 * * *" {
		press(m, string(r))
	}
	applyCmd(m, press(m, "enter"))
	applyCmd(m, press(m, "y"))
	if !strings.Contains(m.statusText, "failed to update crontab") {
		t.Errorf("status = %q", m.statusText)
	}
	if len(tab.writes) != 0 {
		t.Error("a failed read must never be followed by a write")
	}
}

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

func TestGoldensSchedules(t *testing.T) {
	goldenFrames(t, "schedules", func(t *testing.T, env []string, w, h int) *Model {
		return schedAt(t, w, h)
	})
}
