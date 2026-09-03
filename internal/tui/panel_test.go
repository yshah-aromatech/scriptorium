package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

func testTheme() theme.Theme { return theme.New(theme.Default, colorprofile.TrueColor) }

func plainRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = textkit.StripANSI(r)
	}
	return out
}

// The primitive's shape contract: exactly h rows, every row exactly w cells,
// rounded corners in the corners, the title inset in the top border and the
// content between the side borders.
func TestPanelShape(t *testing.T) {
	th := testTheme()
	rows := plainRows(renderPanel(th, []string{"alpha", "beta"}, 20, 5, panelOpts{title: "Fleet"}))
	if len(rows) != 5 {
		t.Fatalf("panel is %d rows, want 5:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for i, r := range rows {
		if got := textkit.Width(r); got != 20 {
			t.Errorf("row %d is %d cells, want 20: %q", i, got, r)
		}
	}
	if rows[0] != "╭─ Fleet ──────────╮" {
		t.Errorf("top border = %q, want the title inset ╭─ Fleet ─…─╮", rows[0])
	}
	if rows[1] != "│alpha             │" {
		t.Errorf("content row = %q", rows[1])
	}
	if rows[4] != "╰──────────────────╯" {
		t.Errorf("bottom border = %q", rows[4])
	}
	// short content pads, long content clips — the frame never shears
	if rows[3] != "│                  │" {
		t.Errorf("missing content row did not pad: %q", rows[3])
	}
}

// pad gives content columns of breathing room inside the side borders
// (task 3's opencode pass at wide sizes).
func TestPanelPad(t *testing.T) {
	rows := plainRows(renderPanel(testTheme(), []string{"x"}, 12, 3, panelOpts{pad: 1}))
	if rows[1] != "│ x        │" {
		t.Errorf("padded content row = %q, want one cell of padding inside the border", rows[1])
	}
}

// A title too long for the border truncates instead of pushing the corner
// off the row — the marquee-inside-the-title seam.
func TestPanelTitleClipsInsideTheBorder(t *testing.T) {
	rows := plainRows(renderPanel(testTheme(), nil, 16, 3, panelOpts{title: "a-very-long-panel-title"}))
	if got := textkit.Width(rows[0]); got != 16 {
		t.Errorf("top border is %d cells, want 16: %q", got, rows[0])
	}
	if !strings.HasPrefix(rows[0], "╭─ ") || !strings.HasSuffix(rows[0], "─╮") {
		t.Errorf("the clipped title broke the border shape: %q", rows[0])
	}
}

// The hint tail renders in the bottom border, right-inset, FROM the given
// key.Binding set — key and description exactly as the bindings carry them.
func TestPanelHintTailComesFromTheBindings(t *testing.T) {
	k := defaultKeys()
	rows := plainRows(renderPanel(testTheme(), nil, 40, 3, panelOpts{
		hints: []key.Binding{k.Start, k.Kill},
	}))
	bottom := rows[2]
	if !strings.Contains(bottom, "r run script") || !strings.Contains(bottom, "x kill") {
		t.Errorf("the tail does not carry the bindings' own help text: %q", bottom)
	}
	if !strings.HasSuffix(bottom, "──╯") {
		t.Errorf("the tail is not inset in the border: %q", bottom)
	}
	if got := textkit.Width(bottom); got != 40 {
		t.Errorf("bottom border is %d cells, want 40", got)
	}
}

// Hints that do not fit drop WHOLE from the end — never a sheared hint.
func TestPanelHintTailDropsWholeHints(t *testing.T) {
	k := defaultKeys()
	rows := plainRows(renderPanel(testTheme(), nil, 24, 3, panelOpts{
		hints: []key.Binding{k.Start, k.Kill, k.Sync},
	}))
	bottom := rows[2]
	if strings.Contains(bottom, "s syn") && !strings.Contains(bottom, "s sync") {
		t.Errorf("a hint was sheared mid-word: %q", bottom)
	}
	if got := textkit.Width(bottom); got != 24 {
		t.Errorf("bottom border is %d cells, want 24: %q", got, bottom)
	}
}

// Focused panels speak Primary (border) with the bold title voice; unfocused
// stay in the quiet Border color — the two must not render identically.
func TestPanelFocusVoice(t *testing.T) {
	th := testTheme()
	on := renderPanel(th, []string{"x"}, 20, 3, panelOpts{title: "t", focused: true})
	off := renderPanel(th, []string{"x"}, 20, 3, panelOpts{title: "t"})
	if on[0] == off[0] {
		t.Error("focused and unfocused top borders render identically")
	}
	if !strings.Contains(on[0], "\x1b[1m") && !strings.Contains(on[0], ";1m") &&
		!strings.Contains(on[0], "\x1b[1;") {
		t.Errorf("the focused title is not bold: %q", on[0])
	}
}

// The ASCII fallback: a profile with no color support (TERM=dumb territory)
// gets +-| borders, so the frame survives charsets that lack box drawing.
func TestPanelASCIIFallback(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.Ascii)
	rows := plainRows(renderPanel(th, []string{"x"}, 10, 3, panelOpts{title: "t"}))
	joined := strings.Join(rows, "\n")
	if strings.ContainsAny(joined, "╭╮╰╯─│") {
		t.Errorf("box-drawing glyphs under the ASCII floor:\n%s", joined)
	}
	if rows[0] != "+- t ----+" {
		t.Errorf("ascii top border = %q", rows[0])
	}
	if rows[1] != "|x       |" {
		t.Errorf("ascii content row = %q", rows[1])
	}
}

// The zebra/selection seam: a selected row's background band stays INSIDE the
// side borders — the border cells themselves carry no selection background.
func TestPanelSelectionStaysInsideTheBorder(t *testing.T) {
	th := testTheme()
	inner := 18
	sel := fillTo(tint(th.S.Base, th.C.SelBg).Render("selected row"), inner, th.C.SelBg)
	rows := renderPanel(th, []string{sel}, 20, 3, panelOpts{})
	row := rows[1]
	// the row must start and end with a border glyph OUTSIDE the SelBg span:
	// find the selection SGR and confirm a reset comes before the closing │
	if !strings.Contains(row, "selected row") {
		t.Fatalf("content lost: %q", row)
	}
	p := textkit.StripANSI(row)
	if !strings.HasPrefix(p, "│") || !strings.HasSuffix(p, "│") {
		t.Fatalf("borders missing around a selected row: %q", p)
	}
	// the final border glyph is rendered by the border style, which carries no
	// background — the SelBg span must be closed before it
	at := strings.LastIndex(row, "│")
	if !strings.Contains(row[:at], "\x1b[m") && !strings.Contains(row[:at], "\x1b[0m") {
		t.Errorf("the selection band runs into the closing border: %q", row)
	}
}

// The scrollbar seam: an overflowing output pane draws its scrollbar column
// INSIDE the panel frame — every content row ends scrollbar-cell, border.
func TestPanelScrollbarInsideTheFrame(t *testing.T) {
	m := runAt(t, 120, 40)
	for i := range 200 {
		m.run.out.append(fmt.Sprintf("line %d", i))
	}
	frame := strings.Split(plainFrame(m), "\n")
	sawThumb := false
	for _, row := range frame[headerRows+1 : headerRows+m.bodyHeight()-1] {
		if !strings.HasSuffix(row, "│") {
			t.Fatalf("a paneled output row does not end on the border: %q", row)
		}
		cells := []rune(row)
		if len(cells) > 3 && cells[len(cells)-3] == '█' {
			sawThumb = true
		}
	}
	if !sawThumb {
		t.Error("no scrollbar thumb inside the frame of an overflowing pane")
	}
}

// The wrap seam: output wraps at the pane's INNER width (frame and padding
// accounted for), so no wrapped row ever collides with the border.
func TestPanelWrapWidthAccountsForTheFrame(t *testing.T) {
	m := runAt(t, 120, 40)
	long := strings.Repeat("x", 300)
	m.run.out.append(long)
	l := runLayoutFor(120, m.bodyHeight())
	wantWidth := max(l.outW-2-2*l.pad, 10) - 1 // inner minus the scrollbar column
	if got := m.run.out.buf.Width(); got != wantWidth {
		t.Fatalf("wrap width = %d, want the inner content width %d", got, wantWidth)
	}
	frame := plainFrame(m)
	if !strings.Contains(frame, strings.Repeat("x", wantWidth)) {
		t.Errorf("the wrapped line does not fill the inner width %d", wantWidth)
	}
	if strings.Contains(frame, strings.Repeat("x", wantWidth)+"x") {
		t.Errorf("a wrapped row overran the inner width into the frame")
	}
}

// The whole frame under the ASCII floor: no box-drawing glyph anywhere.
func TestPaneledFrameASCIIFallback(t *testing.T) {
	m := runAt(t, 120, 40)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	for _, view := range []string{"1", "2", "3", "4"} {
		press(m, view)
		if frame := plainFrame(m); strings.ContainsAny(frame, "╭╮╰╯") {
			t.Errorf("view %s renders rounded corners under the ASCII profile:\n%s", view, frame)
		}
	}
}
