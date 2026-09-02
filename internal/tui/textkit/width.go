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

// VisibleWidth is Width ignoring escape sequences — the width of a string that
// has already been styled. Every layout decision in the TUI is made on already
// styled text, so counting an SGR's bytes as cells (which Width does, and must)
// silently shortens every row it touches.
func VisibleWidth(s string) int {
	if !strings.ContainsRune(s, '\x1b') {
		return Width(s)
	}
	return Width(StripANSI(s))
}

// Truncate cuts s to at most w visible cells, spending the last cell on an
// ellipsis when anything was dropped. Escape sequences are copied through
// without consuming width, and a cut that lands inside a styled span is closed
// with a reset so the style cannot bleed across the rest of the row.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if VisibleWidth(s) <= w {
		return s
	}
	var b strings.Builder
	cells, styled := 0, false
	for i := 0; i < len(s); {
		if n := ansiHeadRE.FindStringIndex(s[i:]); n != nil {
			b.WriteString(s[i : i+n[1]])
			i += n[1]
			styled = true
			continue
		}
		g, _, gw, _ := uniseg.FirstGraphemeClusterInString(s[i:], -1)
		if cells+gw > w-1 {
			break
		}
		b.WriteString(g)
		cells += gw
		i += len(g)
	}
	b.WriteString(Ellipsis)
	if styled {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// Pad right-pads s with spaces to w visible cells. Wider input is returned
// untouched — Fit is the one that also cuts.
func Pad(s string, w int) string {
	if gap := w - VisibleWidth(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// Fit makes s occupy exactly w visible cells: truncated with an ellipsis when too
// wide, space-padded when too narrow. Every column of a rendered row is laid
// out through this, which is why rows stay aligned under CJK and emoji.
func Fit(s string, w int) string { return Pad(Truncate(s, w), w) }
