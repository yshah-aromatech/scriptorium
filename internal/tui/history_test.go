package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/history"
)

func TestHistoryCopyFullViewedLog(t *testing.T) {
	m := historyAt(t, 120, 40)
	noTools(t)
	t.Setenv("TMUX", "test")
	path := filepath.Join(t.TempDir(), "run.log")
	text := "first 日本語\r\n" + strings.Repeat("long log line\r\n", 200) + "last\r\n"
	write(t, path, text)
	m.app.Cfg.LogTailKb = 1
	m.historyScope = "sync-orders"
	m.history.onLoaded(m, HistoryLoadedMsg{Rows: []history.Row{
		{Script: "sync-orders"}, {Script: "sync-orders", LogFile: &path},
	}})
	press(m, "enter")
	if m.history.preview == nil || strings.Contains(strings.Join(m.history.preview.lines, "\n"), "first") {
		t.Fatal("fixture must open a truncated preview")
	}
	press(m, "down") // The selected row is no longer the viewed run.
	cmd := press(m, "y")
	m.history.preview = nil // The command must retain the viewed path.
	got := findMsg[tea.RawMsg](t, cmd)
	if fmt.Sprint(got.Msg) != tmuxWrap(osc52(text)) {
		t.Fatal("copy did not preserve the entire viewed file verbatim")
	}
	for _, width := range []int{80, 120} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m.history.preview = &historyPreview{path: path, script: "sync-orders"}
		if !strings.Contains(plainFrame(m), "y copy full log") {
			t.Fatalf("copy hint missing at width %d", width)
		}
	}
}

func TestHistoryCopyFeedback(t *testing.T) {
	m := historyAt(t, 120, 40)
	noTools(t)
	path := filepath.Join(t.TempDir(), "run.log")
	for _, tc := range []struct {
		name, text, want string
		preview, exists  bool
	}{
		{name: "closed", want: "open a log with enter"},
		{name: "missing", preview: true, want: "log copy failed:"},
		{name: "empty", preview: true, exists: true, want: "nothing to copy"},
		{name: "oversize", preview: true, exists: true, text: strings.Repeat("x", clipboardCap+1), want: "full log copy failed:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.history.preview = nil
			if tc.preview {
				m.history.preview = &historyPreview{path: path}
			}
			if tc.exists {
				write(t, path, tc.text)
			}
			msg := findMsg[StatusMsg](t, press(m, "y"))
			if !strings.Contains(msg.Text, tc.want) {
				t.Fatalf("status = %+v, want %q", msg, tc.want)
			}
		})
	}
}

func TestHistoryCopyLargeLogToLocalClipboard(t *testing.T) {
	m := historyAt(t, 120, 40)
	dir := t.TempDir()
	record := filepath.Join(dir, "clipboard")
	tool := filepath.Join(dir, "wl-copy")
	write(t, tool, fmt.Sprintf("#!/bin/sh\n/bin/cat > %q\n", record))
	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "run.log")
	text := strings.Repeat("日本語\r\n", clipboardCap/4)
	write(t, path, text)
	m.history.preview = &historyPreview{path: path}
	report := findMsg[ClipboardMsg](t, press(m, "y"))
	got, err := os.ReadFile(record)
	if err != nil || string(got) != text {
		t.Fatalf("full clipboard payload mismatch: %v", err)
	}
	msg := findMsg[StatusMsg](t, m.onClipboard(report))
	if msg.Kind != StatusOK || !strings.Contains(msg.Text, "wl-copy") {
		t.Fatalf("success feedback = %+v", msg)
	}
}

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
	if !strings.Contains(plainFrame(m), "history — heartbeat · 1/6") {
		t.Errorf("the scoped title does not carry the fzf-style count:\n%s", plainFrame(m))
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
	// Starting animation may accompany the queue message after a static view.
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			if next := cmd(); next != nil {
				if _, ok := next.(RunQueuedMsg); ok {
					msg = next
					break
				}
			}
		}
	}
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
	m.history.onLoaded(m, HistoryLoadedMsg{Rows: m.history.loaded})
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

func TestHistoryOrderingCacheReloadAndScope(t *testing.T) {
	m := historyAt(t, 120, 40)
	first := m.history.filteredRows(m)
	if allocs := testing.AllocsPerRun(10, func() { _ = m.history.filteredRows(m) }); allocs != 0 {
		t.Errorf("unchanged History ordering allocates %.0f times", allocs)
	}
	m.historyScope = first[0].Script
	if rows := m.history.filteredRows(m); len(rows) != 1 || rows[0].Script != m.historyScope {
		t.Fatalf("scope: %v", rows)
	}
	m.historyScope = ""
	// Reloading the same backing slice still refreshes ordering and width;
	// an offscreen long name must size every visible row consistently.
	m.history.loaded[0].Script = strings.Repeat("wide", 10)
	m.history.onLoaded(m, HistoryLoadedMsg{Rows: m.history.loaded})
	if m.history.nameW != nameColMax {
		t.Fatalf("offscreen name width = %d", m.history.nameW)
	}
	m.historyScope = first[0].Script
	_ = m.history.filteredRows(m)
	if m.history.nameW != len(first[0].Script) {
		t.Fatalf("scope kept old column width %d", m.history.nameW)
	}
	m.historyScope = ""
	m.history.sel = 100
	m.history.onLoaded(m, HistoryLoadedMsg{Rows: []history.Row{doneRowNamed("replacement")}})
	if rows := m.history.filteredRows(m); len(rows) != 1 || rows[0].Script != "replacement" || m.history.sel != 0 {
		t.Fatalf("reload: %v selection %d", rows, m.history.sel)
	}
	m.history.onLoaded(m, HistoryLoadedMsg{})
	if rows := m.history.filteredRows(m); len(rows) != 0 {
		t.Fatalf("empty reload: %v", rows)
	}
}

func BenchmarkHistoryRender(b *testing.B) {
	for _, n := range []int{200, 50000} {
		for _, warm := range []bool{false, true} {
			b.Run(fmt.Sprintf("rows=%d/warm=%t", n, warm), func(b *testing.B) {
				m := newFixtureModel(b, truecolorEnv)
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				m.mode = modeHistory
				for range n {
					m.history.loaded = append(m.history.loaded, m.recent[0])
				}
				if warm {
					_ = m.frame()
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_ = m.frame()
				}
			})
		}
	}
}
