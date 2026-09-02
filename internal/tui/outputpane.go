package tui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// outputPane is the viewport over the run's output buffer.
//
// It is a viewport over textkit.Buffer rather than a bubbles/viewport over a
// string. bubbles/viewport owns a copy of its content and its own idea of the
// scroll offset; the buffer here is ALREADY wrapped rows plus the back-pointers
// the drag-copy rejoin needs, so handing it to a viewport would mean joining
// five thousand rows into one string on every batch of output — a hundred times
// over during a chatty run — and then keeping two offsets in sync. Slicing the
// rows the buffer already has costs one screen's worth of work per frame, which
// is what makes the 50k-line stress test render rather than stall.
type outputPane struct {
	buf    textkit.Buffer
	scroll int  // index of the first visible wrapped row
	follow bool // pinned to the tail
	title  string
	w, h   int

	// Drag selection (inventory §1.11). Both ends are buffer coordinates —
	// a WRAPPED row index and a display cell — so a selection survives the
	// output scrolling underneath it, and rejoining it reads straight through
	// the buffer's WrapSrc back-pointers to the ORIGINAL source text.
	anchor *cell
	extent *cell
}

// cell is one position in the wrapped buffer.
type cell struct{ row, col int }

// selecting reports whether a drag has produced a selection worth copying.
func (o *outputPane) selecting() bool { return o.anchor != nil && o.extent != nil }

// beginDrag records where a press landed and drops any previous selection.
// A drag pins the view: text that scrolls out from under a selection is a
// selection of something the user cannot see (PS sets Follow=false here too).
func (o *outputPane) beginDrag(row, col int) {
	o.anchor, o.extent = &cell{row, col}, nil
	o.follow = false
}

// dragTo extends the live selection.
func (o *outputPane) dragTo(row, col int) {
	if o.anchor == nil {
		return
	}
	o.extent = &cell{row, col}
}

func (o *outputPane) clearSelection() { o.anchor, o.extent = nil, nil }

// selection is the drag normalised to (from <= to) and clamped to the buffer.
func (o *outputPane) selection() (from, to cell, ok bool) {
	if !o.selecting() || len(o.buf.Wrapped) == 0 {
		return from, to, false
	}
	from, to = *o.anchor, *o.extent
	if to.row < from.row || (to.row == from.row && to.col < from.col) {
		from, to = to, from
	}
	last := len(o.buf.Wrapped) - 1
	from.row, to.row = min(max(from.row, 0), last), min(max(to.row, 0), last)
	return from, to, true
}

// selectedText is the dragged text as it was BEFORE wrapping: rows folded out
// of one source line rejoin with exactly the bytes the wrap consumed, and a
// real line break stays a newline (textkit.Buffer.Rejoin, the P10 contract).
func (o *outputPane) selectedText() string {
	from, to, ok := o.selection()
	if !ok {
		return ""
	}
	return o.buf.Rejoin(from.row, from.col, to.row, to.col)
}

// visibleText is everything currently on screen, rejoined the same way — what
// `c` copies.
func (o *outputPane) visibleText() string {
	if len(o.buf.Wrapped) == 0 {
		return ""
	}
	last := min(o.scroll+o.rows()-1, len(o.buf.Wrapped)-1)
	return o.buf.Rejoin(o.scroll, 0, last, maxCell)
}

// maxCell is "to the end of the row" as a cell index: past any real line, and
// small enough that +1 cannot overflow.
const maxCell = 1 << 20

// wordAt is the click-to-copy probe (§1.11): the whitespace-delimited word
// under a cell, stripped of trailing punctuation.
func (o *outputPane) wordAt(row, col int) string {
	if row < 0 || row >= len(o.buf.Wrapped) {
		return ""
	}
	line := o.buf.Wrapped[row]
	at := textkit.ByteAtCell(line, col)
	if at >= len(line) || line[at] == ' ' {
		return ""
	}
	start := strings.LastIndexByte(line[:at], ' ') + 1
	end := strings.IndexByte(line[at:], ' ')
	if end < 0 {
		end = len(line)
	} else {
		end += at
	}
	return strings.Trim(line[start:end], `.,:;"'()`)
}

// deviceCodeRE is the shape of the login codes this exists for — Microsoft
// device-login and friends: 8-10 uppercase letters and digits, nothing else.
var deviceCodeRE = regexp.MustCompile(`^[A-Z0-9]{8,10}$`)

func (o *outputPane) reset(title string, maxLines int) {
	o.buf.Reset()
	o.buf.Max = maxLines
	o.title = title
	o.scroll, o.follow = 0, true
}

// begin retitles the pane for a new run and jumps back to the tail WITHOUT
// clearing it. One continuous scrollback across runs is the PS behaviour and
// the more useful one — you can still see what the previous run said, and the
// retention cap keeps it bounded. Clearing is an explicit action (phase 11's
// `c`), never a side effect of starting something.
func (o *outputPane) begin(title string) {
	o.title = title
	o.follow = true
	o.clamp()
}

// resize re-wraps the buffer only when the usable width actually changed —
// a resize that only changes height must not re-wrap 5000 lines.
func (o *outputPane) resize(w, h int) {
	o.w, o.h = w, h
	if cw := o.contentWidth(); cw != o.buf.Width() {
		o.buf.Rewrap(cw)
	}
	o.clamp()
}

// contentWidth leaves one column for the scrollbar, matching the PS wrap width
// (inventory §12.6) so a line breaks in the same place it always did.
func (o *outputPane) contentWidth() int { return max(o.w-1, 10) }

func (o *outputPane) append(lines ...string) {
	o.buf.Append(lines...)
	o.clamp()
}

// rows is how many wrapped rows fit under the pane's title rule.
func (o *outputPane) rows() int { return max(o.h-1, 0) }

func (o *outputPane) maxScroll() int { return max(len(o.buf.Wrapped)-o.rows(), 0) }

func (o *outputPane) clamp() {
	if o.follow {
		o.scroll = o.maxScroll()
		return
	}
	o.scroll = min(max(o.scroll, 0), o.maxScroll())
}

// scrollBy moves the view and, when the user moves it away from the tail,
// disengages follow — the tail keeps arriving, it just stops dragging the eye.
// Reaching the bottom re-engages it, so scrolling back down does the obvious
// thing without a second keystroke.
func (o *outputPane) scrollBy(delta int) {
	o.scroll = min(max(o.scroll+delta, 0), o.maxScroll())
	o.follow = o.scroll >= o.maxScroll()
}

func (o *outputPane) toTop() {
	o.follow = false
	o.scroll = 0
}

func (o *outputPane) toBottom() {
	o.follow = true
	o.clamp()
}

// behind is how many rows have arrived below the viewport while it was held
// back — the number the "more below" hint reports.
func (o *outputPane) behind() int { return o.maxScroll() - o.scroll }

// view renders the pane: a title rule, the visible slice of wrapped rows each
// colored by what it says, and a scrollbar column. The last row becomes the
// "N more — end follows" hint whenever output is arriving off-screen, because
// a stalled-looking pane during a live run is the worst thing this view can do.
func (o *outputPane) view(th theme.Theme, spin string, focused bool) []string {
	title := o.title
	if title == "" {
		title = "output"
	}
	if spin != "" {
		title = spin + " " + title
	}
	rows := []string{sectionRule(th, title, o.w, focused)}

	n := o.rows()
	visible := len(o.buf.Wrapped)
	thumb, thumbAt := scrollbar(o.scroll, n, visible)
	from, to, selecting := o.selection()
	for i := range n {
		idx := o.scroll + i
		line := ""
		if idx < visible {
			text := o.extend(o.buf.Wrapped[idx])
			// a selected row is drawn in reverse video instead of its content
			// colour: two meanings on one row is one too many, and reverse is
			// what every terminal selection looks like
			if selecting && idx >= from.row && idx <= to.row {
				lo, hi := 0, maxCell
				if idx == from.row {
					lo = from.col
				}
				if idx == to.row {
					hi = to.col
				}
				line = inverseSpan(text, lo, hi)
			} else {
				line = colorLine(th, text)
			}
		}
		bar := th.S.Muted.Render("│")
		switch {
		case visible <= n:
			bar = " "
		case i >= thumbAt && i < thumbAt+thumb:
			bar = th.S.Accent.Render("█")
		}
		rows = append(rows, fillTo(line, o.contentWidth(), nil)+bar)
	}
	if behind := o.behind(); behind > 0 && len(rows) > 1 {
		rows[len(rows)-1] = th.S.Warning.Render(" ▼ " + strconv.Itoa(behind) + " more — end follows")
	}
	return rows
}

// extend re-fills a banner rule to the current pane width. The banner is
// stored as text, written at whatever width the pane had at the time; without
// this a resize leaves every earlier banner stopping short of the edge.
func (o *outputPane) extend(line string) string {
	if !strings.HasPrefix(line, "───") {
		return line
	}
	if gap := o.contentWidth() - textkit.Width(line); gap > 0 {
		return line + strings.Repeat("─", gap)
	}
	return line
}

// inverseSpan renders text with cells [lo, hi] in reverse video — the same
// \e[7m span the PS panel drew, and the reason a selection is legible on every
// terminal including the ones with no colour at all.
func inverseSpan(text string, lo, hi int) string {
	a := textkit.ByteAtCell(text, lo)
	b := textkit.ByteAtCell(text, hi+1)
	if b <= a {
		return text
	}
	return text[:a] + "\x1b[7m" + text[a:b] + "\x1b[27m" + text[b:]
}

// scrollbar returns the thumb's length and position for a viewport of h rows
// over total rows.
func scrollbar(scroll, h, total int) (length, at int) {
	if total <= h || h <= 0 {
		return 0, 0
	}
	length = max(h*h/total, 1)
	span := max(total-h, 1)
	at = min(scroll*(h-length)/span, h-length)
	return length, at
}

// Output colouring, ported from the PS panel (inventory §1.12): the banner
// icons the run pipeline emits, then the words that mean something went wrong.
// Everything else is left alone — a log that is mostly red reads as noise.
var (
	errWordRE = regexp.MustCompile(`(?i)\b(error|exception|failed|failure|fatal)\b`)
	okPhrase  = " success "
)

func colorLine(th theme.Theme, s string) string {
	switch {
	case s == "":
		return ""
	case strings.HasPrefix(s, "─── ✓"), strings.Contains(s, okPhrase):
		return th.S.Success.Render(s)
	case strings.HasPrefix(s, "─── ✗"):
		return th.S.Danger.Render(s)
	case strings.HasPrefix(s, "─── ⊘"), strings.HasPrefix(s, "─── ◷"),
		strings.HasPrefix(s, "─── ◇"), strings.HasPrefix(s, "─── ⚠"):
		return th.S.Warning.Render(s)
	case strings.HasPrefix(s, "───"), strings.HasPrefix(s, "▶"):
		return th.S.Info.Render(s)
	case strings.HasPrefix(s, "WARNING:"):
		return th.S.Muted.Render(s)
	case errWordRE.MatchString(s):
		return th.S.Danger.Render(s)
	}
	return s
}

// banner is the full-width rule with inset text the run pipeline writes into
// the buffer at the start and end of every run.
func banner(text string, w int) string {
	inset := "─── " + text + " "
	return inset + strings.Repeat("─", max(w-textkit.Width(inset), 0))
}
