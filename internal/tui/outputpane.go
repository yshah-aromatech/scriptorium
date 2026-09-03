package tui

import (
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

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

	// search is the current output-search term (ctrl+f, n/N, floor item).
	// Matches are found fresh against the buffer on every render rather than
	// cached, so newly arrived output is always searchable — the same choice
	// Move-TuiSearch makes.
	search string
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

// allText is the whole retained buffer — what `y` copies, and exactly what
// Invoke-TuiCopy copies (`$S.Lines -join "\n"`, src/Tui.psm1:913). The SOURCE
// lines, not the wrapped rows: a copy must not carry the pane's line breaks
// into someone's paste buffer. Redaction already happened on the way in.
func (o *outputPane) allText() string { return strings.Join(o.buf.Lines, "\n") }

// clear empties the panel — Clear-TuiOutput (src/Tui.psm1:597): the buffer,
// the scroll, the selection, and back to following.
func (o *outputPane) clear() {
	o.buf.Reset()
	o.scroll, o.follow = 0, true
	o.clearSelection()
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
// retention cap keeps it bounded. Clearing is an explicit action (`c` → clear
// above), never a side effect of starting something.
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

// rows is how many wrapped rows the pane shows. o.h is the CONTENT height:
// the title rule (floor) or panel borders (paneled) are the caller's rows.
func (o *outputPane) rows() int { return max(o.h, 0) }

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

// viewTitle is what the pane's frame is labelled: the run's title with the
// spinner ahead of it while something is in flight.
func (o *outputPane) viewTitle(spin string) string {
	title := o.title
	if title == "" {
		title = "output"
	}
	if spin != "" {
		title = spin + " " + title
	}
	return title
}

// view renders the floor pane: a title rule over contentRows.
func (o *outputPane) view(th theme.Theme, spin string, focused bool) []string {
	return append([]string{sectionRule(th, o.viewTitle(spin), o.w, focused)},
		o.contentRows(th)...)
}

// contentRows is the visible slice of wrapped rows, each colored by what it
// says, plus the scrollbar column. The last row becomes the "N more — end
// follows" hint whenever output is arriving off-screen, because a
// stalled-looking pane during a live run is the worst thing this view can do.
func (o *outputPane) contentRows(th theme.Theme) []string {
	var rows []string
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
			} else if o.search != "" {
				if spans := searchSpans(text, o.search); len(spans) > 0 {
					// ponytail: reverse-video the matches and drop content
					// colouring on this row rather than composing them (PS
					// resumes the row's colour after each match, which needs
					// tracking which SGR was already open); upgrade if a
					// row's own colour turns out to matter with a search live.
					line = highlightSpans(text, spans)
				} else {
					line = colorLine(th, text)
				}
			} else {
				line = colorLine(th, text)
			}
		}
		// scrollbar: quiet Border track, Muted thumb — chrome, not data
		// (v1.0.1: the Accent thumb was one more purple element per frame)
		bar := th.S.Border.Render("│")
		switch {
		case visible <= n:
			bar = " "
		case i >= thumbAt && i < thumbAt+thumb:
			bar = th.S.Muted.Render("█")
		}
		rows = append(rows, fillTo(line, o.contentWidth(), nil)+bar)
	}
	if behind := o.behind(); behind > 0 && len(rows) > 0 {
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

// searchSpans returns the byte ranges of every case-insensitive occurrence of
// term in text (n/N's match highlight — inventory §1.12).
func searchSpans(text, term string) [][2]int {
	if term == "" {
		return nil
	}
	lower, needle := strings.ToLower(text), strings.ToLower(term)
	var spans [][2]int
	for at := 0; ; {
		i := strings.Index(lower[at:], needle)
		if i < 0 {
			return spans
		}
		start := at + i
		end := start + len(needle)
		spans = append(spans, [2]int{start, end})
		at = end
	}
}

// highlightSpans reverse-videos every [start,end) byte range — plain \e[7m,
// the same device inverseSpan uses, so it composes safely with whatever
// colour a caller wraps the result in afterwards (7/27 only toggles the
// reverse attribute; it never touches a surrounding SGR colour).
func highlightSpans(text string, spans [][2]int) string {
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(text[prev:sp[0]])
		b.WriteString("\x1b[7m")
		b.WriteString(text[sp[0]:sp[1]])
		b.WriteString("\x1b[27m")
		prev = sp[1]
	}
	b.WriteString(text[prev:])
	return b.String()
}

// searchResult is one n/N jump's outcome, for the status line.
type searchResult struct {
	found            bool
	term             string
	matchIdx, matchN int // 1-based
}

// jumpToMatch is Move-TuiSearch: matches are recomputed against the CURRENT
// buffer on every call (new output is always searchable), the jump lands on
// the match nearest the vertical center of the viewport, and running past
// either end wraps to the other one.
func (o *outputPane) jumpToMatch(term string, dir int) searchResult {
	if term == "" {
		return searchResult{}
	}
	lower := strings.ToLower(term)
	var hits []int
	for i, line := range o.buf.Wrapped {
		if strings.Contains(strings.ToLower(line), lower) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		return searchResult{term: term}
	}

	body := o.rows()
	cur := o.scroll
	if o.follow {
		cur = o.maxScroll()
	}
	anchor := cur + body/2

	target, idx := -1, -1
	if dir > 0 {
		for i, h := range hits {
			if h > anchor {
				target, idx = h, i
				break
			}
		}
		if target < 0 {
			target, idx = hits[0], 0 // wrap to the first match
		}
	} else {
		for i := len(hits) - 1; i >= 0; i-- {
			if hits[i] < anchor {
				target, idx = hits[i], i
				break
			}
		}
		if target < 0 {
			target, idx = hits[len(hits)-1], len(hits)-1 // wrap to the last
		}
	}

	o.scroll = min(max(target-body/2, 0), o.maxScroll())
	o.follow = false
	return searchResult{found: true, term: term, matchIdx: idx + 1, matchN: len(hits)}
}

// searchJump is n (dir>0) / N (dir<0): jump the output pane to the next or
// previous match of r.searchTerm, wrapping at either end — Move-TuiSearch's
// own status line, verbatim.
func (r *runModel) searchJump(dir int) tea.Cmd {
	if r.searchTerm == "" {
		return status(StatusWarn, "no search term — ctrl+f to search the output")
	}
	res := r.out.jumpToMatch(r.searchTerm, dir)
	if !res.found {
		return status(StatusWarn, "no matches for '"+res.term+"'")
	}
	return status(StatusInfo, "match "+strconv.Itoa(res.matchIdx)+"/"+strconv.Itoa(res.matchN)+
		" for '"+res.term+"' — n next · N prev")
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
