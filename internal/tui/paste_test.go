package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

func TestPasteReachesFocusedOverlay(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	e := newEnvEditor(m, scripts.Script{Name: "paste"})
	m.open(e)
	e.escArmed = true
	text := "TOKEN=secret\nOTHER=日本語"
	m.Update(tea.PasteMsg{Content: text})
	if e.ta.Value() != text || !e.dirty() || e.escArmed {
		t.Fatalf("env paste: value=%q dirty=%v armed=%v", e.ta.Value(), e.dirty(), e.escArmed)
	}
	e.saving = true
	m.Update(tea.PasteMsg{Content: "ignored"})
	if e.ta.Value() != text {
		t.Fatal("paste changed editor during save")
	}

	changed, submitted := "", false
	in := newInput(m, inputArgs, "args", "", func(*Model, string) tea.Cmd {
		submitted = true
		return nil
	}).live(func(_ *Model, value string) tea.Cmd {
		changed = value
		return nil
	})
	m.open(in)
	m.Update(tea.PasteMsg{Content: "q\nr"})
	if changed != in.ti.Value() || changed == "" || submitted || m.ov != in {
		t.Fatalf("prompt paste: value=%q changed=%q submitted=%v", in.ti.Value(), changed, submitted)
	}

	p := newPalette(m)
	m.open(p)
	m.Update(tea.PasteMsg{Content: "theme"})
	if p.ti.Value() != "theme" || len(p.shown) == 0 || len(p.shown) == len(p.items) {
		t.Fatal("palette paste did not filter")
	}

	picker := newThemeOverlay(m)
	m.open(picker)
	name := picker.items[0]
	m.Update(tea.PasteMsg{Content: name})
	if picker.ti.Value() != name || picker.selected() != name || m.th.Name != name {
		t.Fatal("theme paste did not filter and preview")
	}
	picker.saving = true
	m.Update(tea.PasteMsg{Content: "ignored"})
	if picker.ti.Value() != name {
		t.Fatal("paste changed theme during save")
	}

	m.closeOverlay()
	mode := m.mode
	m.Update(tea.PasteMsg{Content: "qr1234"})
	if m.mode != mode || m.ov != nil {
		t.Fatal("paste outside editor triggered shortcuts")
	}
}
