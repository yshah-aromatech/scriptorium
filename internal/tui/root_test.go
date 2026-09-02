package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func press(m *Model, keys ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range keys {
		_, last = m.Update(keyMsg(k))
	}
	return last
}

// 1-4 switch views, and the frame proves it: the pressed digit's tab is the
// active one and the body is that view's.
func TestViewSwitcher(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	cases := []struct {
		keyName string
		want    mode
		body    string
	}{
		{"3", modeHistory, "filterable forensics table"},
		{"4", modeSchedules, "agenda by next fire"},
		{"2", modeRun, "live output"},
		{"1", modeFleet, ""},
	}
	for _, c := range cases {
		press(m, c.keyName)
		if m.mode != c.want {
			t.Fatalf("after %q: mode = %v, want %v", c.keyName, m.mode, c.want)
		}
		plain := textkit.StripANSI(m.frame())
		if c.body != "" && !strings.Contains(plain, c.body) {
			t.Errorf("after %q the body does not mention %q:\n%s", c.keyName, c.body, plain)
		}
	}
}

// Views that do not exist yet say so, in the frame, rather than showing an
// empty pane that reads like a bug.
func TestUnbuiltViewsAreHonest(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, k := range []string{"3", "4"} {
		press(m, k)
		if plain := textkit.StripANSI(m.frame()); !strings.Contains(plain, "arrives in the next phase") {
			t.Errorf("view %s does not admit it is unbuilt:\n%s", k, plain)
		}
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := newFixtureModel(t, truecolorEnv)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		var cmd tea.Cmd
		if k == "ctrl+c" {
			_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		} else {
			cmd = press(m, k)
		}
		if cmd == nil {
			t.Fatalf("%s produced no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s did not quit", k)
		}
	}
}

// Below the floor the UI stops pretending. Pinned as a golden because it is
// the one frame a user sees when nothing else can be true.
func TestTooSmallGuard(t *testing.T) {
	for _, sz := range [][2]int{{39, 20}, {80, 9}, {30, 8}} {
		m := newFixtureModel(t, truecolorEnv)
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		plain := textkit.StripANSI(m.frame())
		if !strings.Contains(plain, "terminal too small") {
			t.Errorf("%dx%d rendered a normal frame:\n%s", sz[0], sz[1], plain)
		}
	}
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	checkGolden(t, "too-small-30x8.txt", textkit.StripANSI(m.frame()))
	checkGolden(t, "too-small-30x8.ansi", m.frame())
}

// The 60s ticker's job is to call app.MissedSweep and reschedule itself.
// Only the sweep child is executed here; running the tick child would sleep
// for a real minute. The proof that the sweep really reached the domain layer
// is its side effect — missed-state.json, stamped with the live schedules.
func TestMissedTickRunsTheSweepAndReschedules(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	stateFile := filepath.Join(m.app.Paths.DataDir, "missed-state.json")
	if _, err := os.Stat(stateFile); err == nil {
		t.Fatal("missed-state.json existed before the sweep")
	}

	children := batchCmds(func() tea.Cmd { _, c := m.Update(MissedTickMsg(frozen)); return c }())
	if len(children) != 2 {
		t.Fatalf("the missed tick scheduled %d commands, want the sweep + the next tick", len(children))
	}
	msg, ok := children[0]().(MissedMsg)
	if !ok {
		t.Fatalf("the first child produced %T, want MissedMsg", msg)
	}
	if msg.Err != nil {
		t.Fatalf("sweep failed: %v", msg.Err)
	}
	state, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("the sweep never reached internal/missed: %v", err)
	}
	for name := range fixtureSchedules {
		if !strings.Contains(string(state), name) {
			t.Errorf("missed-state.json did not stamp %s:\n%s", name, state)
		}
	}
}

// A sweep that turns something up records it and says so once — a second
// sweep reporting the same miss must not re-warn.
func TestMissedResultsWarnOnce(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.missed = map[string]missed.Miss{}
	found := MissedMsg{Misses: []missed.Miss{{Name: "nightly-report", Expression: "*/5 * * * *"}}}

	cmd := m.onMissed(found)
	if cmd == nil {
		t.Fatal("a fresh miss produced no warning")
	}
	if got := cmd().(StatusMsg); !strings.Contains(got.Text, "nightly-report") || got.Kind != StatusWarn {
		t.Errorf("warning = %+v", got)
	}
	if _, ok := m.missed["nightly-report"]; !ok {
		t.Error("the miss was not recorded")
	}
	if again := m.onMissed(found); again != nil {
		t.Error("the same miss warned twice")
	}
	// and it clears when the schedule catches up
	if m.onMissed(MissedMsg{}) != nil || len(m.missed) != 0 {
		t.Error("a clean sweep did not clear the misses")
	}
}

// Ruling 6: a sweep that fails is a line in the status bar, never a crash.
func TestMissedSweepErrorBecomesStatus(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	cmd := m.onMissed(MissedMsg{Err: errBoom{}})
	if cmd == nil {
		t.Fatal("a failed sweep produced no status")
	}
	m.Update(cmd())
	if !strings.Contains(m.statusText, "missed-run sweep failed") {
		t.Errorf("status = %q", m.statusText)
	}
	if !strings.Contains(textkit.StripANSI(m.frame()), "missed-run sweep failed") {
		t.Error("the failure never reached the frame")
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "crontab unreadable" }

// The 1Hz and 2s tickers reschedule themselves, and the lock poll rescans.
func TestTickersReschedule(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	if _, c := m.Update(TickMsg(frozen)); c == nil {
		t.Error("the 1Hz tick did not reschedule itself")
	}
	if got := len(batchCmds(func() tea.Cmd { _, c := m.Update(LockPollMsg(frozen)); return c }())); got != 2 {
		t.Errorf("the lock poll scheduled %d commands, want scan + reschedule", got)
	}
}

// A transient status line expires on a 1Hz tick rather than sticking forever.
func TestStatusExpires(t *testing.T) {
	now := frozen
	m := newFixtureModel(t, truecolorEnv)
	m.now = func() time.Time { return now }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(StatusMsg{Text: "synced 2 repos", Kind: StatusOK})
	if !strings.Contains(textkit.StripANSI(m.frame()), "synced 2 repos") {
		t.Fatal("the status never showed")
	}
	now = frozen.Add(statusTTL + time.Second)
	m.Update(TickMsg(now))
	if strings.Contains(textkit.StripANSI(m.frame()), "synced 2 repos") {
		t.Error("the status outlived its TTL")
	}
}

// The footer is rendered from the keymap, so it can only ever advertise keys
// that are actually bound. The phase-11 keys must not appear anywhere.
func TestFooterShowsOnlyLiveKeys(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})

	bound := map[string]bool{}
	for _, b := range allBindings(m.keys) {
		for _, k := range b.Keys() {
			bound[k] = true
		}
	}
	for _, p11 := range []string{"a", "e", "v", "i", "l", "u", "U", "t", "y", "c", "/", "?", "ctrl+f", "n", "N", "h"} {
		if bound[p11] {
			t.Errorf("%q is bound in phase 10 but its overlay does not exist yet", p11)
		}
	}

	for _, tc := range []struct {
		mode  mode
		focus focus
		want  []string
	}{
		{modeFleet, focusList, []string{"↑/k", "↓/j", "↵", "f", "r", "s"}},
		{modeRun, focusList, []string{"↑/k", "↓/j", "tab", "r", "x", "X", "s"}},
		{modeRun, focusOutput, []string{"↑/k", "↓/j", "pgup", "end", "tab", "r", "x"}},
		{modeHistory, focusList, nil},
	} {
		m.mode, m.focus = tc.mode, tc.focus
		var keys []string
		for _, b := range m.hints() {
			keys = append(keys, b.Help().Key)
		}
		want := append(append([]string{}, tc.want...), "1", "2", "3", "4", "q")
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Errorf("mode %v focus %v hints = %v, want %v", tc.mode, tc.focus, keys, want)
		}
		footer := textkit.StripANSI(m.help.ShortHelpView(m.hints()))
		for _, k := range tc.want {
			if !strings.Contains(footer, k) {
				t.Errorf("footer %q is missing %q", footer, k)
			}
		}
	}
}

func allBindings(k keyMap) []key.Binding {
	return []key.Binding{
		k.Fleet, k.Run, k.History, k.Schedules, k.Quit,
		k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom,
		k.Open, k.FailFilter,
		k.Focus, k.Start, k.Kill, k.ClearQueue, k.Sync, k.Follow,
	}
}

// The header degrades in steps instead of losing the switcher: at every width
// down to the floor, all four view digits are still on screen.
func TestHeaderKeepsTheSwitcher(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	for w := minWidth; w <= 200; w += 7 {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		header := textkit.StripANSI(m.header())
		for _, d := range []string{"1", "2", "3", "4"} {
			if !strings.Contains(header, d) {
				t.Fatalf("width %d dropped tab %s: %q", w, d, header)
			}
		}
		if got := textkit.Width(header); got > w {
			t.Fatalf("width %d: header is %d cells: %q", w, got, header)
		}
	}
}

// The frame chrome — header, status bar, key hints — at all three sizes.
func TestChromeGoldens(t *testing.T) {
	goldenFrames(t, "chrome-history", func(t *testing.T, env []string) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeHistory
		m.Update(StatusMsg{Text: "missed run: nightly-report — the scheduled fire never arrived", Kind: StatusWarn})
		return m
	})
}
