package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// confirmOverlay is the y/n modal (inventory §1.6): y or Enter runs the
// action, n or Esc cancels with "cancelled".
//
// onYes is asynchronous work. onAccept/onCancel run on the update loop when
// the answer itself must change ownership; they return any asynchronous work.
type confirmOverlay struct {
	message  string
	onYes    tea.Cmd
	onAccept func(*Model) tea.Cmd
	onCancel func(*Model) tea.Cmd
}

func confirmPrompt(message string, onYes tea.Cmd) *confirmOverlay {
	return &confirmOverlay{message: message, onYes: onYes}
}

func (c *confirmOverlay) kind() overlayKind           { return overlayConfirm }
func (c *confirmOverlay) title() string               { return "confirm" }
func (c *confirmOverlay) height(*Model, int, int) int { return 1 }

// rows is the question alone: the answers live in the modal's bottom border
// (v1.1.0 task 1 — the hint tail replaced the in-body hint row).
func (c *confirmOverlay) rows(m *Model, w, _ int) []string {
	return []string{m.th.S.Warning.Render(c.message)}
}

func (c *confirmOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{m.keys.Accept, m.keys.Deny, m.keys.Close}
}

func (c *confirmOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Accept):
		if c.onAccept != nil {
			return c.onAccept(m), true
		}
		return c.onYes, true
	case key.Matches(msg, m.keys.Deny), key.Matches(msg, m.keys.Close):
		if c.onCancel != nil {
			return c.onCancel(m), true
		}
		return status(StatusInfo, "cancelled"), true
	}
	return nil, false
}

// quitCmd is `q` and ctrl+c: quit, unless something is running — in which case
// the PS app asks first (inventory §1.2/§1.3), and so does this. Answering yes
// kills the run and then quits, in that order, which is what the question
// promises.
func (m *Model) quitCmd() tea.Cmd {
	if !m.run.active() {
		m.closeOverlay()
		return tea.Quit
	}
	previous := m.ov
	m.open(&confirmOverlay{onCancel: func(m *Model) tea.Cmd {
		m.open(previous)
		if p, ok := previous.(*themeOverlay); ok && p.settled {
			return nil
		}
		return status(StatusInfo, "cancelled")
	}, message: "a script is running — kill it and quit?", onAccept: func(m *Model) tea.Cmd {
		m.run.quitting = true
		m.run.queue = nil
		return tea.Batch(m.run.kill(m), m.run.settle(m))
	}})
	return nil
}
