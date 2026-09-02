package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/history"
)

// historyAt switches a fixture model into the History view and runs the load
// command it schedules, so tests see the same rows the running app would.
func historyAt(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	cmd := press(m, "3")
	if cmd == nil {
		t.Fatal("switching to History scheduled no load")
	}
	m.Update(cmdMsg(cmd))
	return m
}

// Newest first, and the 80-column floor's collapse rule (design §4:
// when/status/script/duration survive; age/cpu-peak/sparkline/mem/trigger do
// not).
func TestHistoryTableShowsFixtureRows(t *testing.T) {
	m := historyAt(t, 120, 40)
	if len(m.history.loaded) == 0 {
		t.Fatal("the History load never reached internal/history")
	}
	frame := plainFrame(m)
	// newest run (heartbeat, 40s ago) sorts above the oldest (backup-db, 50h ago)
	if strings.Index(frame, "heartbeat") > strings.Index(frame, "backup-db") {
		t.Errorf("history is not newest-first:\n%s", frame)
	}
	for _, want := range []string{"success", "failure", "cron"} {
		if !strings.Contains(frame, want) {
			t.Errorf("120x40 history is missing %q:\n%s", want, frame)
		}
	}

	narrow := plainFrame(historyAt(t, 80, 24))
	if !strings.Contains(narrow, "success") || !strings.Contains(narrow, "failure") {
		t.Errorf("80x24 history dropped status:\n%s", narrow)
	}
	if strings.Contains(narrow, "cron") {
		t.Errorf("80x24 history kept the trigger column past the floor:\n%s", narrow)
	}
}

// f scopes to the selected row's script, and back to everything — the same
// m.historyScope the Run view's h deep-link sets.
func TestHistoryScopeToggle(t *testing.T) {
	m := historyAt(t, 120, 40)
	m.history.sel = 0 // newest row: heartbeat

	press(m, "f")
	if m.historyScope != "heartbeat" {
		t.Fatalf("scope = %q, want heartbeat", m.historyScope)
	}
	for _, r := range m.history.filteredRows(m) {
		if r.Script != "heartbeat" {
			t.Errorf("a scoped table still shows %s", r.Script)
		}
	}
	if !strings.Contains(plainFrame(m), "history — heartbeat") {
		t.Errorf("the scoped title is not on screen:\n%s", plainFrame(m))
	}

	press(m, "f")
	if m.historyScope != "" {
		t.Errorf("a second f did not clear the scope: %q", m.historyScope)
	}
}

// The Run view's h deep-link and History's own f agree on one field.
func TestHistoryReadsTheDeepLinkScope(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.selectByName(m, "sync-orders")
	press(m, "h")
	if m.mode != modeHistory || m.historyScope != "sync-orders" {
		t.Fatalf("h did not land scoped on sync-orders: mode=%v scope=%q", m.mode, m.historyScope)
	}
	m.Update(cmdMsg(m.loadHistory()))
	for _, r := range m.history.filteredRows(m) {
		if r.Script != "sync-orders" {
			t.Errorf("scoped-by-deep-link table still shows %s", r.Script)
		}
	}
}

// r re-runs the selected row's script through the SAME flow every other run
// entry point uses — proven by watching it queue behind a live run, exactly
// as TestQueueSemantics proves it for the Run view's own r.
func TestHistoryRerunWiring(t *testing.T) {
	m := historyAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")
	m.history.sel = 0

	name := m.history.filteredRows(m)[0].Script
	msg := cmdMsg(press(m, "r"))
	q, ok := msg.(RunQueuedMsg)
	if !ok || q.Name != name {
		t.Fatalf("r on the selected row gave %#v, want it queued behind the live run", msg)
	}
}

// r on a script no longer in the repo says so rather than starting nothing
// silently.
func TestHistoryRerunOnVanishedScript(t *testing.T) {
	m := historyAt(t, 120, 40)
	m.history.loaded = append(m.history.loaded, doneRowNamed("ghost-script"))
	m.history.sel = indexOf(t, m, "ghost-script")
	msg := cmdMsg(press(m, "r"))
	got, ok := msg.(StatusMsg)
	if !ok || !strings.Contains(got.Text, "ghost-script") || !strings.Contains(got.Text, "not found") {
		t.Errorf("rerun on a vanished script gave %#v", msg)
	}
}

// Enter opens the log as a split preview pane — the table stays on screen,
// which is the point of §4.3's "not a mode swap".
func TestHistoryPreviewOpensAsASplit(t *testing.T) {
	m := historyAt(t, 120, 40)
	logFile := filepath.Join(t.TempDir(), "backup-db.log")
	write(t, logFile, "connecting to postgres\ndump complete\n")
	patchLogFile(m, "backup-db", logFile)

	m.history.sel = indexOf(t, m, "backup-db")
	press(m, "enter")
	if m.history.preview == nil {
		t.Fatal("enter did not open a preview")
	}
	frame := plainFrame(m)
	if !strings.Contains(frame, "dump complete") {
		t.Errorf("the preview does not show the log tail:\n%s", frame)
	}
	if !strings.Contains(frame, "heartbeat") {
		t.Errorf("the table vanished when the preview opened — it should be a split, not a mode swap:\n%s", frame)
	}

	press(m, "esc")
	if m.history.preview != nil {
		t.Error("esc did not close the preview")
	}
}

// A row with no retained log says so instead of opening an empty pane.
func TestHistoryPreviewOnARowWithNoLog(t *testing.T) {
	m := historyAt(t, 120, 40)
	m.history.loaded = append(m.history.loaded, doneRowNamed("no-log-script"))
	m.history.loaded[len(m.history.loaded)-1].LogFile = nil
	m.history.sel = indexOf(t, m, "no-log-script")
	msg := cmdMsg(press(m, "enter"))
	got, ok := msg.(StatusMsg)
	if !ok || !strings.Contains(got.Text, "no log") {
		t.Errorf("enter on a logless row gave %#v", msg)
	}
	if m.history.preview != nil {
		t.Error("a logless row still opened a preview")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// indexOf is the position of the newest row for a script in the CURRENT
// scope — history.jsonl can carry several runs per script, and every test
// here means the newest one.
func indexOf(t *testing.T, m *Model, script string) int {
	t.Helper()
	for i, r := range m.history.filteredRows(m) {
		if r.Script == script {
			return i
		}
	}
	t.Fatalf("no history row for %s", script)
	return -1
}

// patchLogFile points every loaded row for one script at a real file, the
// same in-memory technique TestViewLogTailsTheNewestLog uses for the Run
// view's v key — the fixture's own rows claim /tmp paths that a hermetic test
// must not touch.
func patchLogFile(m *Model, script, path string) {
	for i := range m.history.loaded {
		if m.history.loaded[i].Script == script {
			m.history.loaded[i].LogFile = &path
		}
	}
}

// doneRowNamed is a minimal finished row for a script the fixture never
// seeded — used to test the guards for a run whose script has since vanished
// or never kept a log.
func doneRowNamed(script string) history.Row {
	dur := 1.0
	return history.Row{
		Script: script, Status: "success", Trigger: "manual",
		StartedAt: history.Stamp(frozen), FinishedAt: history.Stamp(frozen),
		DurationSec: &dur,
	}
}

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

func TestGoldensHistory(t *testing.T) {
	goldenFrames(t, "history", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.Update(cmdMsg(press(m, "3")))
		return m
	})

	goldenFrames(t, "history-scoped", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.Update(cmdMsg(press(m, "3")))
		m.history.sel = 0
		press(m, "f")
		return m
	})

	goldenFrames(t, "history-preview", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.Update(cmdMsg(press(m, "3")))
		logFile := filepath.Join(t.TempDir(), "backup-db.log")
		write(t, logFile, "connecting to postgres://db.internal:5432\ndumping schema public (18 tables)\n── ✓ backup-db · success · exit 0 · 42.5s\n")
		patchLogFile(m, "backup-db", logFile)
		m.history.sel = indexOf(t, m, "backup-db")
		press(m, "enter")
		return m
	})
}
