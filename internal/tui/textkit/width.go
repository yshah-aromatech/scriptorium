package textkit

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Ellipsis is the single cell Truncate spends to say "there was more".
const Ellipsis = "…"

// Width is how many terminal cells a string occupies. Grapheme-aware via
// rivo/uniseg (design §2), which is also what lipgloss v2 counts with — so the
// number here and the number the renderer pads to are the same number.
//
// This is the one deliberate divergence from the PS app's Get-StoDisplayWidth
// (inventory §12.5, and testdata/psfixtures/README.md): PS sums codepoints, so
// a ZWJ-joined family emoji measures 8 cells where the terminal draws 2.
func Width(s string) int { return uniseg.StringWidth(s) }

// ByteAtCell maps a display column to a byte index — the port of
// Get-TuiCellCharIndex, and the primitive a mouse selection turns into text.
// It returns the first index whose accumulated width has already REACHED cell,
// so a column landing inside a wide grapheme resolves past it; a cell beyond
// the end resolves to len(s). Used as [ByteAtCell(from), ByteAtCell(to+1)) that
// rule includes a wide grapheme the selection touches at its end and excludes
// one it only clips at its start.
func ByteAtCell(s string, cell int) int {
	if cell <= 0 {
		return 0
	}
	w, at := 0, 0
	for rest := s; rest != ""; {
		if w >= cell {
			return at
		}
		g, remainder, gw, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		w += gw
		at += len(g)
		rest = remainder
	}
	return len(s)
}

// Truncate cuts s to at most w display cells, spending the last cell on an
// ellipsis when anything was dropped.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	if w == 1 {
		return Ellipsis
	}
	return s[:ByteAtCell(s, w-1)] + Ellipsis
}

// Pad right-pads s with spaces to w display cells. Wider input is returned
// untouched — Fit is the one that also cuts.
func Pad(s string, w int) string {
	if gap := w - Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// Fit makes s occupy exactly w cells: truncated with an ellipsis when too
// wide, space-padded when too narrow. Every column of a rendered row is laid
// out through this, which is why rows stay aligned under CJK and emoji.
func Fit(s string, w int) string { return Pad(Truncate(s, w), w) }
