package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// fleetModel is the Fleet view (design §4.1). Phase 10 task 3 fills it in;
// this is the seat it sits in.
type fleetModel struct {
	w, h int
	sel  int
}

func (f *fleetModel) init(*Model)               {}
func (f *fleetModel) reload(*Model)             {}
func (f *fleetModel) resize(_ *Model, w, h int) { f.w, f.h = w, h }

func (f *fleetModel) update(*Model, tea.Msg) tea.Cmd { return nil }

func (f *fleetModel) selected(m *Model) *scripts.Script {
	if f.sel < 0 || f.sel >= len(m.scripts) {
		return nil
	}
	return &m.scripts[f.sel]
}

func (f *fleetModel) view(m *Model, w, h int) []string {
	return placeholderPane(m.th, w, h, "Fleet",
		"summary strip · per-script rows · agenda · live activity",
		"arrives in the next task")
}

// selected is the script the whole frame is talking about — the status bar's
// context line and the Run view's deep-link both read it.
func (m *Model) selected() *scripts.Script {
	switch m.mode {
	case modeRun:
		return m.run.selected(m)
	default:
		return m.fleet.selected(m)
	}
}
