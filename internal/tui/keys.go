package tui

import "charm.land/bubbles/v2/key"

// keyMap is the single source of truth for keys AND for the footer hints: the
// footer is rendered from these bindings' own help text (bubbles/help), so the
// two can never drift the way the PS app's hand-maintained hint list did.
//
// Only overlay-free actions are bound in phase 10. The keys that need an
// overlay to be honest — a e v i l u y c / , the command palette, the real help
// screen — arrive in phase 11 WITH their overlays, and are deliberately absent
// here rather than bound to a no-op: an advertised key that does nothing is
// worse than a key that is not advertised yet.
type keyMap struct {
	// global
	Fleet     key.Binding
	Run       key.Binding
	History   key.Binding
	Schedules key.Binding
	Quit      key.Binding

	// navigation (meaning depends on the focused pane)
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding

	// fleet
	Open       key.Binding
	FailFilter key.Binding

	// run
	Focus      key.Binding
	Start      key.Binding
	Kill       key.Binding
	ClearQueue key.Binding
	Sync       key.Binding
	Follow     key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Fleet:     key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "fleet")),
		Run:       key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "run")),
		History:   key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "history")),
		Schedules: key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "schedules")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:   key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),

		Open:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "open in run")),
		FailFilter: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "failures")),

		Focus:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Start:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run")),
		Kill:       key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "kill")),
		ClearQueue: key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "clear queue")),
		Sync:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
		Follow:     key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "follow")),
	}
}

// hints returns the bindings the footer shows for the current view and focused
// pane — the live keys, and only those. The view switcher and quit come last
// because they are always true and least interesting.
func (m *Model) hints() []key.Binding {
	k := m.keys
	var out []key.Binding
	switch m.mode {
	case modeFleet:
		out = []key.Binding{k.Up, k.Down, k.Open, k.FailFilter, k.Start, k.Sync}
	case modeRun:
		if m.focus == focusOutput {
			out = []key.Binding{k.Up, k.Down, k.PageUp, k.Follow, k.Focus, k.Start, k.Kill}
		} else {
			out = []key.Binding{k.Up, k.Down, k.Focus, k.Start, k.Kill, k.ClearQueue, k.Sync}
		}
	case modeHistory, modeSchedules:
		// nothing of their own yet — the placeholder pane says so.
	}
	return append(out, k.Fleet, k.Run, k.History, k.Schedules, k.Quit)
}
