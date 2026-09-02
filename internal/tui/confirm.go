package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// confirmOverlay is the y/n modal (inventory §1.6): y or Enter runs the
// action, n or Esc cancels with "cancelled".
//
// The action is a tea.Cmd, not a closure over the model. The PS app had to
// pass its data through state to keep its scriptblocks resolvable; here the
// reason is the update loop — whatever the answer triggers has to be a command
// like everything else, so a confirm can kill a run (which blocks for the 3s
// grace) without stalling the frame.
type confirmOverlay struct {
	message string
	onYes   tea.Cmd
}

func confirmPrompt(message string, onYes tea.Cmd) *confirmOverlay {
	return &confirmOverlay{message: message, onYes: onYes}
}

func (c *confirmOverlay) kind() overlayKind           { return overlayConfirm }
func (c *confirmOverlay) title() string               { return "confirm" }
func (c *confirmOverlay) height(*Model, int, int) int { return 2 }

func (c *confirmOverlay) rows(m *Model, w, _ int) []string {
	th := m.th
	return []string{
		th.S.Warning.Render(c.message),
		th.S.Success.Render("y") + th.S.Desc.Render(" confirm · ") +
			th.S.Muted.Render("n / esc cancel"),
	}
}

func (c *confirmOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{m.keys.Accept, m.keys.Deny, m.keys.Close}
}

func (c *confirmOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Accept):
		return c.onYes, true
	case key.Matches(msg, m.keys.Deny), key.Matches(msg, m.keys.Close):
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
		return tea.Quit
	}
	m.open(confirmPrompt("a script is running — kill it and quit?",
		tea.Sequence(m.run.kill(m), tea.Quit)))
	return nil
}
