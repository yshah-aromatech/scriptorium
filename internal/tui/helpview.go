package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// helpOverlay is `?` — the whole key set, grouped, straight from
// keyMap.groups(). Any key closes it (inventory §1.10).
//
// It renders the same bindings the footer and the palette do, so a key that
// works is documented and a key that is documented works.
type helpOverlay struct{ scroll int }

func (h *helpOverlay) kind() overlayKind { return overlayHelp }
func (h *helpOverlay) title() string     { return "keys" }

// layout is the whole card content at this width — one column, or two where
// the terminal can hold them apart. rows() windows it and height() measures
// it, so what the card asks for and what it draws can never disagree.
func (h *helpOverlay) layout(m *Model, w int) []string {
	th := m.th
	var cells []string
	for _, g := range m.keys.groups() {
		cells = append(cells, th.S.TitleOn.Render("# "+g.Title))
		for _, b := range g.Keys {
			cells = append(cells, " "+th.S.Key.Render(textkit.Fit(b.Help().Key, 7))+" "+
				th.S.Desc.Render(b.Help().Desc))
		}
		cells = append(cells, "")
	}
	if w < twoColumnWidth {
		return cells
	}

	// balance the two columns on a group boundary rather than mid-group
	colW := w / 2
	split := splitAt(cells, (len(cells)+1)/2)
	left, right := cells[:split], cells[split:]
	rows := make([]string, 0, max(len(left), len(right)))
	for i := range max(len(left), len(right)) {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		rows = append(rows, strings.TrimRight(
			fillTo(textkit.Truncate(l, colW-1), colW, nil)+textkit.Truncate(r, w-colW), " "))
	}
	return rows
}

// twoColumnWidth is where the key set stops being a tall list and starts
// being two readable columns.
const twoColumnWidth = 76

// height takes the whole body when the key set needs it. The card is the one
// overlay whose content is fixed and large, so leaving decorative rows of the
// view showing above and below it costs real content: at 120x40 those two rows
// are the difference between the whole key set and a scroll.
func (h *helpOverlay) height(m *Model, w, bodyH int) int {
	return min(len(h.layout(m, w)), max(bodyH-2, 3))
}

// hints has to be true: ↑/↓ scroll here rather than closing, and at the 80x24
// floor the card cannot show the whole key set at once, so saying "any key
// close" alone would be an instruction that loses you half the help.
func (h *helpOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{
		m.keys.Up, m.keys.Down,
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("any other key", "close")),
	}
}

// rows windows the layout and, when anything is below the fold, spends the last
// visible row saying so. A help screen that silently hides half of itself is
// worse than one that is honestly too small — which is exactly what the 80x24
// contract size is.
func (h *helpOverlay) rows(m *Model, w, hgt int) []string {
	all := h.layout(m, w)
	rows := h.window(all, hgt)
	if below := len(all) - h.scroll - len(rows); below > 0 && len(rows) > 0 {
		rows[len(rows)-1] = m.th.S.Warning.Render(
			" ▼ " + strconv.Itoa(below) + " more — ↑↓ scrolls")
	}
	return rows
}

// splitAt moves a column split to the next blank line at or after want, so a
// group's title never ends up in one column and its keys in the other.
func splitAt(cells []string, want int) int {
	for i := want; i < len(cells); i++ {
		if cells[i] == "" {
			return i + 1
		}
	}
	return len(cells)
}

// window is the visible slice of rows. It clamps the scroll offset IN PLACE so
// holding ↓ at the bottom does not build up an offset that then needs the same
// number of ↑ presses to undo.
func (h *helpOverlay) window(rows []string, hgt int) []string {
	if hgt <= 0 || len(rows) == 0 {
		return nil
	}
	h.scroll = min(max(h.scroll, 0), max(len(rows)-hgt, 0))
	return rows[h.scroll:min(h.scroll+hgt, len(rows))]
}

// key closes on anything (§1.10), except the two navigation keys — a help
// screen that scrolls beats one that closes when you try to read the rest.
func (h *helpOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Up):
		h.scroll--
		return nil, false
	case key.Matches(msg, m.keys.Down):
		h.scroll++
		return nil, false
	}
	return nil, true
}
