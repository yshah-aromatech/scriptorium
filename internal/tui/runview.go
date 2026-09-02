package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// runModel is the Run view (design §4.2). Phase 10 task 4 fills it in.
type runModel struct {
	w, h int
	sel  int
}

func (r *runModel) init(*Model)               {}
func (r *runModel) initCmd() tea.Cmd          { return nil }
func (r *runModel) reload(*Model)             {}
func (r *runModel) resize(_ *Model, w, h int) { r.w, r.h = w, h }

func (r *runModel) update(*Model, tea.Msg) tea.Cmd { return nil }

func (r *runModel) selected(m *Model) *scripts.Script {
	if r.sel < 0 || r.sel >= len(m.scripts) {
		return nil
	}
	return &m.scripts[r.sel]
}

// statusLine reports the live run, if any. The bool is what lets the status bar
// know a run outranks a transient message.
func (r *runModel) statusLine(*Model, int) (string, bool) { return "", false }

func (r *runModel) view(m *Model, w, h int) []string {
	return placeholderPane(m.th, w, h, "Run",
		"script list · live output · details · ETA · queue",
		"arrives in the next task")
}
