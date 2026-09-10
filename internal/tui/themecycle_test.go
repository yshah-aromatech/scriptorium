package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/deps"
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

func TestThemePickerPreviewsAndSavesAtomically(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	press(m, ":")
	p, ok := m.ov.(*paletteOverlay)
	if !ok {
		t.Fatal("palette did not open")
	}
	for i, shown := range p.shown {
		if strings.Join(p.items[shown].b.Keys(), ",") == "T" {
			p.sel = i
			break
		}
	}
	replay := cmdMsg(press(m, "enter"))
	m.Update(replay)
	picker, ok := m.ov.(*themeOverlay)
	if !ok {
		t.Fatalf("palette replay opened %T, want theme picker", m.ov)
	}
	picker.ti.SetValue("gbd")
	picker.filter()
	picker.preview(m)
	if got := picker.selected(); got == "" || m.th.Name != "gruvbox-dark" {
		t.Fatalf("picker preview = %q / %q", got, m.th.Name)
	}
	press(m, "enter")
	if m.ov != nil || m.th.Name != "gruvbox-dark" {
		t.Fatal("Enter did not apply and close the picker")
	}

	m.open(newThemeOverlay(m))
	picker = m.ov.(*themeOverlay)
	picker.ti.SetValue("dracula")
	picker.filter()
	picker.sel = 0
	picker.preview(m)
	save, closed := picker.key(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if closed {
		t.Fatal("save closed before it completed")
	}
	m.Update(cmdMsg(save))
	if m.ov != nil || m.app.Cfg.Theme != "dracula" {
		t.Fatalf("successful theme save did not close and update config: overlay %T theme %q status %q", m.ov, m.app.Cfg.Theme, m.statusText)
	}
	cfg, _, _, err := config.Load(m.app.Paths.AppDir)
	if err != nil || cfg.Theme != "dracula" {
		t.Fatalf("saved theme did not reload: %q, %v", cfg.Theme, err)
	}

	original := []byte(`{"unknown":{"nested":true},"theme":"dracula"}`)
	path := filepath.Join(m.app.Paths.AppDir, "config.json")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(m.app.Paths.AppDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(m.app.Paths.AppDir, 0700) })
	m.app.Cfg.Theme = "dracula"
	m.open(newThemeOverlay(m))
	picker = m.ov.(*themeOverlay)
	picker.ti.SetValue("terminal")
	picker.filter()
	picker.sel = 0
	picker.preview(m)
	save, closed = picker.key(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if closed {
		t.Fatal("failed save closed before it completed")
	}
	m.Update(cmdMsg(save))
	if m.ov != picker || m.app.Cfg.Theme != "dracula" || picker.ti.Value() != "terminal" {
		t.Fatal("failed save discarded the picker or changed config")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(original) {
		t.Fatalf("failed save changed config: %q, %v", got, err)
	}
}

func TestThemePickerGolden(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.open(newThemeOverlay(m))
	frame := m.frame()
	checkFrameShape(t, "overlay-theme-80x24", frame, 80, 24)
	checkGolden(t, "overlay-theme-80x24.txt", plainGolden(frame))
	checkGolden(t, "overlay-theme-80x24.ansi", frame)
}

func TestThemeSaveSettlesBehindQuitConfirmation(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")
	p := newThemeOverlay(m)
	m.open(p)
	p.saving = true
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyEscape}, {Code: tea.KeyUp}, {Code: 'x', Text: "x"}} {
		if cmd, close := p.key(m, msg); cmd != nil || close {
			t.Fatalf("pending save accepted %#v", msg)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if _, ok := m.ov.(*confirmOverlay); !ok {
		t.Fatalf("ctrl+c opened %T, want quit confirmation", m.ov)
	}
	m.Update(ThemeSavedMsg{Picker: p, Name: "dracula"})
	if p.saving || m.app.Cfg.Theme != "dracula" {
		t.Fatal("successful save was not settled behind confirmation")
	}
	press(m, "n")
	if m.ov != nil {
		t.Fatalf("declining quit resurrected completed picker: %T", m.ov)
	}

	p = newThemeOverlay(m)
	m.open(p)
	p.saving = true
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m.Update(ThemeSavedMsg{Picker: p, Name: "terminal", Err: errors.New("disk full")})
	if p.saving || m.app.Cfg.Theme != "dracula" {
		t.Fatal("failed save was not settled behind confirmation")
	}
	press(m, "n")
	if m.ov != p || !strings.Contains(m.statusText, "disk full") {
		t.Fatalf("declining quit did not restore editable picker: %T %q", m.ov, m.statusText)
	}
	if cmd, close := p.key(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); cmd == nil || close {
		t.Fatal("failed save did not permit retry")
	}
	// A dependency scan must queue behind a pending write rather than replace it.
	m.run.releaseAttempt()
	p.ti.SetValue("dracula")
	p.filter()
	p.saving = true
	attempt := m.run.reserve(m.scripts[0].Name)
	m.run.onDepsScanned(m, DepsScannedMsg{Attempt: attempt, Script: m.scripts[0], Missing: []deps.Dep{{Name: "Az", Display: "Az"}}})
	if m.ov != p || attempt.phase != "waiting" {
		t.Fatalf("dependency completion replaced pending picker: %T phase %q", m.ov, attempt.phase)
	}
	m.Update(ThemeSavedMsg{Picker: p, Name: "terminal", Err: errors.New("retry failed")})
	m.Update(TickMsg(frozen))
	if m.ov != p || p.ti.Value() != "dracula" || attempt.phase != "waiting" || !strings.Contains(m.statusText, "retry failed") {
		t.Fatalf("failed save let preparation replace picker: %T %q %q %q", m.ov, p.ti.Value(), attempt.phase, m.statusText)
	}
	if cmd, close := p.key(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); cmd == nil || close {
		t.Fatal("failed queued save did not permit retry")
	}
	m.Update(ThemeSavedMsg{Picker: p, Name: "terminal", Err: errors.New("retry failed")})
	press(m, "esc")
	if m.ov != nil {
		t.Fatalf("editable failed picker did not dismiss: %T", m.ov)
	}
	m.run.releaseAttempt()
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
