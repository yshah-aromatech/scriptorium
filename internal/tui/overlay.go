package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// The modal layer (design §4). Exactly ONE overlay is open at a time, over
// whichever view is behind it; Esc closes it; while a MODAL one is open the run
// queue stops draining (inventory §4.11) and the mouse is ignored (§1.11).
//
// The contract, for everything built on this later:
//
//   - implement [overlay]; open it with Model.open, close it with
//     Model.closeOverlay (or by returning close=true from key)
//   - key() sees every keypress except ctrl+c, which always belongs to the
//     terminal. Esc MUST close (the .env editor arms first and closes on the
//     second press — that is the one allowed variation)
//   - rows() renders the card's CONTENT only; the manager draws the rules,
//     pads every row to the full width and centres the card in the body
//   - hints() replaces the footer while the overlay is open, and comes from
//     the same key.Binding set as everything else
//   - kind() decides whether the queue drains underneath: a read-only overlay
//     (help, palette) never blocks it, a modal one always does
type overlay interface {
	kind() overlayKind
	title() string
	// rows renders the card's content at w cells, at most h rows.
	rows(m *Model, w, h int) []string
	// height is how many content rows the card wants, given the frame's
	// width and the body's height.
	height(m *Model, w, h int) int
	// key handles one keypress; close=true pops the overlay afterwards.
	key(m *Model, msg tea.KeyPressMsg) (cmd tea.Cmd, close bool)
	hints(m *Model) []key.Binding
}

// overlayKind is what an overlay IS, for the two decisions the manager makes
// on its behalf: whether the run queue drains underneath it, and (through
// that) whether it is a modal interruption or a reference card.
type overlayKind int

const (
	// read-only overlays: the app keeps working underneath
	overlayHelp overlayKind = iota
	overlayPalette

	// modal overlays: they own the next keystroke, and the queue waits
	overlayConfirm
	overlayInput
	overlayDeps
	overlayEnv
)

// blocking is inventory §4.11's rule: the PS app drains its run queue only
// while no MODAL overlay is open (deps/confirm/input/env) and keeps draining
// under the read-only ones (help, and now the palette).
func (k overlayKind) blocking() bool { return k >= overlayConfirm }

// open puts an overlay up, replacing any that was already open. One layer,
// never a stack: two modals over each other is how a TUI acquires a state
// nobody can Esc out of.
func (m *Model) open(o overlay) { m.ov = o }

func (m *Model) closeOverlay() { m.ov = nil }

// onOverlayKey routes a keypress into the open overlay and pops it when the
// overlay says it is done.
func (m *Model) onOverlayKey(msg tea.KeyPressMsg) tea.Cmd {
	cmd, done := m.ov.key(m, msg)
	if done {
		m.closeOverlay()
	}
	return cmd
}

// queueUnblocked is the modal gate (inventory §4.11). It must never return
// false for something that cannot be dismissed, or the queue dead-locks.
func (m *Model) queueUnblocked() bool {
	return m.ov == nil || !m.ov.kind().blocking()
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// overlayCard is the drawn card: a titled rule, the overlay's own rows padded
// to the full width, and a closing rule. Full-width bands rather than a boxed
// window — the same "rules, not boxes" grammar the panes use, and it needs no
// mid-row surgery on already-styled text underneath.
func (m *Model) overlayCard(w, h int) []string {
	o := m.ov
	inner := min(max(o.height(m, w, h), 1), max(h-2, 1))
	content := o.rows(m, w-2, inner)

	rows := make([]string, 0, inner+2)
	rows = append(rows, sectionRule(m.th, o.title(), w, true))
	for i := range inner {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		rows = append(rows, fillTo(" "+line, w, nil))
	}
	return append(rows, m.th.S.Border.Render(strings.Repeat("─", max(w, 0))))
}

// applyOverlay lays the card over the middle of the body. Rows the card does
// not cover keep showing the view underneath, which is what makes it read as
// an overlay rather than a fifth screen.
func (m *Model) applyOverlay(body []string, w, h int) []string {
	card := m.overlayCard(w, h)
	top := max((h-len(card))/2, 0)
	for i, row := range card {
		if top+i >= len(body) {
			break
		}
		body[top+i] = row
	}
	return body
}
