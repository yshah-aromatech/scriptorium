package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// v1.0.1 task 3 — the live theme cycler: `]` / `[` walk the full set (curated
// palettes, then every bubbletint ID), session-only, with the status line
// naming the theme and how to keep it.

func TestThemeCyclerWalksTheFullSet(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	names := theme.CycleNames()

	if m.th.Name != theme.Default {
		t.Fatalf("starting theme = %q", m.th.Name)
	}
	start := 0
	for i, n := range names {
		if n == theme.Default {
			start = i
		}
	}

	cmd := press(m, "]")
	if want := names[(start+1)%len(names)]; m.th.Name != want {
		t.Errorf("] moved to %q, want %q", m.th.Name, want)
	}
	msg, ok := cmdMsg(cmd).(StatusMsg)
	if !ok {
		t.Fatalf("] returned %T, want a status message", cmdMsg(cmd))
	}
	if !strings.Contains(msg.Text, m.th.Name) {
		t.Errorf("status %q does not name the theme", msg.Text)
	}
	if !strings.Contains(msg.Text, `"theme": "`+m.th.Name+`"`) || !strings.Contains(msg.Text, "config.json") {
		t.Errorf("status %q does not say how to persist the choice", msg.Text)
	}

	press(m, "[")
	if m.th.Name != theme.Default {
		t.Errorf("[ did not walk back to %q (got %q)", theme.Default, m.th.Name)
	}

	// walking backward from the first name wraps to the last tint
	m.useTheme(theme.New(names[0], m.th.Profile))
	press(m, "[")
	if want := names[len(names)-1]; m.th.Name != want {
		t.Errorf("[ from the first theme = %q, want the wrap to %q", m.th.Name, want)
	}
}

// The cycle is session-only: config.json on disk is never rewritten.
func TestThemeCyclerDoesNotTouchConfig(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	before := m.app.Cfg.Theme
	press(m, "]", "]", "[")
	if m.app.Cfg.Theme != before {
		t.Errorf("cycling rewrote the in-memory config theme %q -> %q", before, m.app.Cfg.Theme)
	}
}

// A cycled theme takes effect on the very next frame — the whole point of a
// live cycler. Dracula's ground replaces Night Owl's everywhere.
func TestThemeCyclerRepaintsLive(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.useTheme(theme.New("dracula", m.th.Profile))
	frame := m.frame()
	if !strings.Contains(frame, "48;2;30;31;40") {
		t.Error("the dracula ground is not painted after a live switch")
	}
	if strings.Contains(frame, "48;2;1;22;39") {
		t.Error("the Night Owl ground survived a live switch")
	}
}

// The palette overlay lists both cycler commands — `:` then "theme" finds
// them, which is the discoverability path the briefs promise.
func TestPaletteListsThemeCommands(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, ":")
	if m.ov == nil {
		t.Fatal("the palette did not open")
	}
	for _, c := range "theme" {
		press(m, string(c))
	}
	frame := textkit.StripANSI(m.frame())
	if !strings.Contains(frame, "theme: next") || !strings.Contains(frame, "theme: previous") {
		t.Errorf("palette filtered to 'theme' does not list both cycler commands:\n%s", frame)
	}
}

// A tint name in config.json resolves end-to-end, and the dracula fleet frame
// is pinned as the one-size tint golden the release is judged on.
func TestDraculaGolden(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.useTheme(theme.New("dracula", theme.Profile("auto", truecolorEnv)))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	frame := m.frame()
	checkFrameShape(t, "fleet-dracula", frame, 120, 40)
	checkGolden(t, "fleet-dracula-120x40.ansi", frame)
	checkGolden(t, "fleet-dracula-120x40.txt", plainGolden(frame))
}
