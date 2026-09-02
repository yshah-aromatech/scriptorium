package textkit

import (
	"math"
	"strings"

	"github.com/rivo/uniseg"
)

// span is one wrapped row's byte extent inside its source line. The gap
// between one span's End and the next's Start is exactly what the wrap
// consumed — a single space at a word break, nothing at a hard break — which
// is what makes the rejoin below lossless.
type span struct{ Start, End int }

// wrapSpans is the §12.6 algorithm: find the largest grapheme prefix fitting
// width cells; if the last space inside it falls in the second half, break
// there and eat the space (avoids tiny leading fragments); otherwise hard-break
// exactly at the cell cut, mid-word, losing nothing. A prefix of zero (an
// unbreakable grapheme wider than the whole column) is forced to one grapheme
// so the loop always makes progress.
func wrapSpans(line string, width int) []span {
	if width < 1 {
		width = 1
	}
	if line == "" {
		return []span{{0, 0}}
	}
	var out []span
	base := 0
	s := line
	for {
		if Width(s) <= width {
			out = append(out, span{base, base + len(s)})
			return out
		}
		w, cut := 0, len(s)
		for rest, at := s, 0; rest != ""; {
			g, remainder, gw, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
			if w+gw > width {
				cut = at
				break
			}
			w += gw
			at += len(g)
			rest = remainder
		}
		if cut <= 0 {
			g, _, _, _ := uniseg.FirstGraphemeClusterInString(s, -1)
			cut = len(g)
		}
		seg := s[:cut]
		brk := strings.LastIndexByte(seg, ' ')
		// PS compares against [int]($cut / 2), and [int] on a double is
		// banker's rounding — for an odd cut that is not integer division
		// (7/2 rounds to 4, not 3). RoundToEven reproduces it, as everywhere
		// else a PS numeric cast is ported.
		if brk > int(math.RoundToEven(float64(cut)/2)) {
			out = append(out, span{base, base + brk})
			base += brk + 1
			s = s[brk+1:]
		} else {
			out = append(out, span{base, base + cut})
			base += cut
			s = s[cut:]
		}
		if s == "" {
			return out
		}
	}
}

// Wrap breaks one line to width display cells, word-aware (§12.6). An empty
// line yields one empty row so it still occupies a screen row.
func Wrap(line string, width int) []string {
	sp := wrapSpans(line, width)
	out := make([]string, len(sp))
	for i, s := range sp {
		out[i] = line[s.Start:s.End]
	}
	return out
}

// Src is a wrapped row's back-pointer: which source line it came from and the
// byte offset inside that line where it starts. The offset is what makes
// Rejoin exact — a rejoin that merely reinserted a space would corrupt every
// hard-broken row (and P11's drag-copy reads straight through this).
type Src struct {
	Line   int
	Offset int
}

// Buffer is the output scrollback: raw sanitised lines plus the wrap cache
// rendered from them. Wrapped and WrapSrc are parallel.
type Buffer struct {
	Lines   []string
	Wrapped []string
	WrapSrc []Src

	// Max is how many SOURCE lines to retain (config.maxOutputLines). Values
	// under PS's floor of 100 are raised to it. Trimming uses PS's hysteresis:
	// nothing happens until the buffer passes Max*1.1, because trimming exactly
	// at the cap re-wraps the whole buffer on every appended line once it is
	// full — thousands of times a second during a chatty run.
	Max int

	width int
}

// Width is the column count the cache is currently wrapped to.
func (b *Buffer) Width() int { return b.width }

func (b *Buffer) cap() int {
	if b.Max < 100 {
		return 100
	}
	return b.Max
}

// Rewrap re-derives the whole wrap cache at a new width. Cheap enough to call
// on every resize; O(total bytes).
func (b *Buffer) Rewrap(width int) {
	if width < 1 {
		width = 1
	}
	b.width = width
	b.Wrapped = b.Wrapped[:0]
	b.WrapSrc = b.WrapSrc[:0]
	for i := range b.Lines {
		b.wrapOne(i)
	}
}

func (b *Buffer) wrapOne(i int) {
	line := b.Lines[i]
	for _, s := range wrapSpans(line, b.width) {
		b.Wrapped = append(b.Wrapped, line[s.Start:s.End])
		b.WrapSrc = append(b.WrapSrc, Src{Line: i, Offset: s.Start})
	}
}

// Append sanitises each raw line (Clean), stores it and extends the wrap
// cache, trimming to Max once the hysteresis mark is passed.
func (b *Buffer) Append(raw ...string) {
	if b.width < 1 {
		b.width = 1
	}
	for _, l := range raw {
		b.Lines = append(b.Lines, Clean(l))
		b.wrapOne(len(b.Lines) - 1)
	}
	max := b.cap()
	if len(b.Lines) > max*11/10 {
		// copy rather than reslice: the dropped prefix has to become
		// collectable, which is the whole point of the cap.
		b.Lines = append([]string(nil), b.Lines[len(b.Lines)-max:]...)
		b.Rewrap(b.width)
	}
}

// Reset empties the buffer, keeping the current width.
func (b *Buffer) Reset() {
	b.Lines, b.Wrapped, b.WrapSrc = nil, nil, nil
}

// Rejoin turns a wrapped-row span into text: stream semantics (fromCell to the
// end of the first row, whole rows in between, up to and including toCell on
// the last). Rows folded from ONE source line are rejoined with exactly the
// bytes the wrap consumed; a real line break stays a newline.
//
// Rejoining a source line's full run of rows reproduces that line byte for
// byte, which is the contract P11's drag-copy is built on.
func (b *Buffer) Rejoin(fromRow, fromCell, toRow, toCell int) string {
	if len(b.Wrapped) == 0 {
		return ""
	}
	fromRow = clamp(fromRow, 0, len(b.Wrapped)-1)
	toRow = clamp(toRow, 0, len(b.Wrapped)-1)
	if toRow < fromRow {
		fromRow, toRow = toRow, fromRow
	}
	var sb strings.Builder
	for i := fromRow; i <= toRow; i++ {
		text := b.Wrapped[i]
		from, to := 0, len(text)
		if i == fromRow {
			from = ByteAtCell(text, fromCell)
		}
		if i == toRow {
			to = ByteAtCell(text, toCell+1)
		}
		if to > from {
			sb.WriteString(text[from:to])
		}
		if i == toRow {
			break
		}
		here, next := b.WrapSrc[i], b.WrapSrc[i+1]
		if here.Line == next.Line {
			sb.WriteString(b.Lines[here.Line][here.Offset+len(text) : next.Offset])
		} else {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
