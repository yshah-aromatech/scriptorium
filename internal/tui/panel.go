package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// The rounded panel system (v1.1.0 task 1). One primitive frames every pane,
// card and modal: rounded corners, the title inset in the top border
// (`╭─ Fleet ─────╮`), the pane's key hints inset in the bottom border
// (`╰── r run · x kill ──╯`, rendered from the same key.Binding set the
// footer reads — never a second list), focused = Primary border with the
// bold title voice.
//
// The 80×24 floor keeps the v1.0.1 rule grammar instead: below panelMinWidth
// a full frame would cost two columns per pane side and one row per pane
// bottom, which is exactly the budget the floor exists to protect (§4
// amendment, per-view floor rules). ASCII terminals get `+-|` borders.
const (
	// panelMinWidth is where the views switch from the floor's rule grammar
	// to full rounded panels.
	panelMinWidth = 100

	// padWideWidth is task 3's breathing-room threshold: at and above it,
	// panel content gets one extra cell of horizontal padding inside the
	// side borders (opencode roominess); the floor stays dense.
	padWideWidth = 120
)

func paneled(w int) bool { return w >= panelMinWidth }

func panelPad(w int) int {
	if w >= padWideWidth {
		return 1
	}
	return 0
}

// borderSet is the six glyphs a frame is drawn with.
type borderSet struct{ tl, tr, bl, br, h, v string }

var (
	roundedBorder = borderSet{"╭", "╮", "╰", "╯", "─", "│"}
	asciiBorder   = borderSet{"+", "+", "+", "+", "-", "|"}
)

// borderFor picks the glyph set: a profile at or below Ascii is the
// TERM=dumb floor, where box-drawing glyphs cannot be assumed either.
func borderFor(th theme.Theme) borderSet {
	if th.Profile <= colorprofile.Ascii {
		return asciiBorder
	}
	return roundedBorder
}

// panelOpts is everything a call site can say about a frame.
type panelOpts struct {
	title   string
	focused bool
	// hints renders into the bottom border, right-inset. The bindings come
	// straight from the keymap (tailHints) — the border can therefore never
	// advertise a key the footer and help do not know.
	hints []key.Binding
	// pad is extra content padding inside the side borders (panelPad).
	pad int
	// titleStyle overrides the title voice (the activity pulse); nil uses
	// Title / TitleOn by focus.
	titleStyle *lipgloss.Style
}

// renderPanel frames content in a panel exactly w cells wide and h rows tall
// (h includes both border rows). Content rows render at w-2-2*pad cells:
// short rows pad, long rows clip — the frame never shears.
func renderPanel(th theme.Theme, content []string, w, h int, o panelOpts) []string {
	if w < 4 || h < 2 {
		return fitRows(content, max(h, 0))
	}
	b := borderFor(th)
	line := th.S.Border
	titleSt := th.S.Title
	if o.focused {
		line, titleSt = th.S.BorderOn, th.S.TitleOn
	}
	if o.titleStyle != nil {
		titleSt = *o.titleStyle
	}

	inner := w - 2 - 2*o.pad
	pad := strings.Repeat(" ", o.pad)
	side := line.Render(b.v)

	rows := make([]string, 0, h)
	rows = append(rows, panelTop(b, line, titleSt, o.title, w))
	for i := range h - 2 {
		row := ""
		if i < len(content) {
			row = content[i]
		}
		if lipgloss.Width(row) > inner {
			row = textkit.Truncate(row, inner)
		}
		rows = append(rows, side+pad+fillTo(row, inner, nil)+pad+side)
	}
	rows = append(rows, panelBottom(th, b, line, o.hints, w))
	return rows
}

// panelTop is `╭─ title ────╮`: the title inset after one leading dash,
// clipped so the closing corner always lands on the last cell.
func panelTop(b borderSet, line, titleSt lipgloss.Style, title string, w int) string {
	if title == "" {
		return line.Render(b.tl + strings.Repeat(b.h, w-2) + b.tr)
	}
	title = textkit.Truncate(title, max(w-6, 1))
	fill := w - 5 - textkit.VisibleWidth(title)
	return line.Render(b.tl+b.h+" ") + titleSt.Render(title) +
		line.Render(" "+strings.Repeat(b.h, max(fill, 1))+b.tr)
}

// panelBottom is `╰──── r run · x kill ──╯`: the pane's own keys, right-inset
// in the border. Hints that do not fit drop WHOLE from the end, exactly like
// the footer's truncation strategy; when none fit the border closes plain.
func panelBottom(th theme.Theme, b borderSet, line lipgloss.Style, hints []key.Binding, w int) string {
	for n := len(hints); n > 0; n-- {
		tail, cells := hintTail(th, hints[:n])
		fill := w - 6 - cells // corners + spaces around the tail + 2 closing dashes
		if fill < 2 {
			continue
		}
		return line.Render(b.bl+strings.Repeat(b.h, fill)+" ") + tail +
			line.Render(" "+b.h+b.h+b.br)
	}
	return line.Render(b.bl + strings.Repeat(b.h, w-2) + b.br)
}

// hintTail renders bindings the way the footer does — key in the key voice,
// description muted — and reports the cell width of the result.
func hintTail(th theme.Theme, hints []key.Binding) (string, int) {
	var parts []string
	cells := 0
	for _, h := range hints {
		k, d := h.Help().Key, h.Help().Desc
		parts = append(parts, th.S.Key.Render(k)+" "+th.S.Desc.Render(d))
		cells += textkit.Width(k) + 1 + textkit.Width(d)
	}
	cells += 3 * max(len(parts)-1, 0)
	return strings.Join(parts, th.S.Border.Render(" · ")), cells
}
