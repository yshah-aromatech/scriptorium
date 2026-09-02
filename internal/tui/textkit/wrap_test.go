package textkit_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// §12.6 by case: fits whole; word break when the last space falls in the second
// half; hard break (mid-word) when it does not; forced progress on an
// unbreakable wide char; empty line stays one empty row.
func TestWrap(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"empty", "", 10, []string{""}},
		{"fits", "hello", 10, []string{"hello"}},
		{"exact fit", "helloworld", 10, []string{"helloworld"}},
		// cut=10 ("the quick "), last space at 9 > 5 -> word break, space eaten
		{"word break", "the quick brown fox", 10, []string{"the quick", "brown fox"}},
		// cut=10, seg="a bcdefghi", last space at 1, not > 5 -> hard break
		{"space too early hard-breaks", "a bcdefghijkl", 10, []string{"a bcdefghi", "jkl"}},
		// no space at all -> pure hard break, nothing lost
		{"no spaces", "abcdefghijklmnopqrstuvwxy", 10, []string{"abcdefghij", "klmnopqrst", "uvwxy"}},
		// width 1 with a 2-cell char: cut would be 0, forced to one grapheme
		{"unbreakable wide char", "日本", 1, []string{"日", "本"}},
		{"wide pair per row", "日本語", 4, []string{"日本", "語"}},
		{"trailing space kept on last row", "abc def ", 4, []string{"abc", "def "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := textkit.Wrap(c.in, c.width)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Wrap(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
			}
			for _, row := range got {
				if w := textkit.Width(row); w > c.width && len([]rune(row)) > 1 {
					t.Errorf("row %q is %d cells, over width %d", row, w, c.width)
				}
			}
		})
	}
}

// Every wrapped row keeps a back-pointer to its source line AND the byte offset
// it starts at — the P11 drag-copy rejoin contract.
func TestBufferWrapSrc(t *testing.T) {
	var b textkit.Buffer
	b.Rewrap(10)
	b.Append("the quick brown fox", "short")

	wantRows := []string{"the quick", "brown fox", "short"}
	if !reflect.DeepEqual(b.Wrapped, wantRows) {
		t.Fatalf("Wrapped = %q, want %q", b.Wrapped, wantRows)
	}
	wantSrc := []textkit.Src{{Line: 0, Offset: 0}, {Line: 0, Offset: 10}, {Line: 1, Offset: 0}}
	if !reflect.DeepEqual(b.WrapSrc, wantSrc) {
		t.Fatalf("WrapSrc = %+v, want %+v", b.WrapSrc, wantSrc)
	}
}

// The gate: for EVERY source line, rejoining all of its wrapped rows end to end
// reproduces the source line byte for byte — across word breaks (a space was
// consumed) and hard breaks (nothing was). A naive "always reinsert a space"
// rejoin fails the hard-break lines here.
func TestRejoinRoundTrip(t *testing.T) {
	lines := []string{
		"the quick brown fox jumps over the lazy dog",
		"averyveryverylongunbreakabletokenwithnospacesatall",
		"a bcdefghijklmnopqrstuvwxyz mixed  double  spaces",
		"日本語のテキストと english mixed together in one line",
		"short",
		"",
		"trailing space   ",
	}
	for _, width := range []int{7, 10, 13, 24, 80} {
		t.Run(fmt.Sprintf("width%d", width), func(t *testing.T) {
			var b textkit.Buffer
			b.Rewrap(width)
			b.Append(lines...)
			for src := range b.Lines {
				first, last := -1, -1
				for i, s := range b.WrapSrc {
					if s.Line == src {
						if first < 0 {
							first = i
						}
						last = i
					}
				}
				if first < 0 {
					t.Fatalf("source line %d produced no wrapped rows", src)
				}
				got := b.Rejoin(first, 0, last, 1<<30)
				if got != b.Lines[src] {
					t.Errorf("rejoin of line %d = %q, want %q", src, got, b.Lines[src])
				}
			}
		})
	}
}

// Rejoin across source lines keeps a real newline; a partial selection honours
// the start/end display cells.
func TestRejoinSelection(t *testing.T) {
	var b textkit.Buffer
	b.Rewrap(10)
	b.Append("the quick brown fox", "second line here")
	// rows: 0 "the quick" 1 "brown fox" 2 "second" 3 "line here"
	if got, want := b.Rejoin(0, 4, 1, 4), "quick brown"; got != want {
		t.Errorf("cross-fold selection = %q, want %q", got, want)
	}
	if got, want := b.Rejoin(1, 6, 2, 5), "fox\nsecond"; got != want {
		t.Errorf("cross-line selection = %q, want %q", got, want)
	}
	if got, want := b.Rejoin(0, 0, 3, 1<<30), "the quick brown fox\nsecond line here"; got != want {
		t.Errorf("whole buffer = %q, want %q", got, want)
	}
}

// Rewrap at a new width re-derives Wrapped/WrapSrc from Lines and stays exact.
func TestBufferRewrap(t *testing.T) {
	var b textkit.Buffer
	b.Rewrap(10)
	b.Append("the quick brown fox jumps")
	b.Rewrap(40)
	if !reflect.DeepEqual(b.Wrapped, []string{"the quick brown fox jumps"}) {
		t.Fatalf("after widening: %q", b.Wrapped)
	}
	b.Rewrap(9)
	if got := b.Rejoin(0, 0, len(b.Wrapped)-1, 1<<30); got != b.Lines[0] {
		t.Errorf("rejoin after rewrap = %q, want %q", got, b.Lines[0])
	}
}

// Append sanitises exactly like Add-TuiOutput does.
func TestBufferAppendCleans(t *testing.T) {
	var b textkit.Buffer
	b.Rewrap(80)
	b.Append("\x1b[32ma\tb\x1b[0m\x00")
	if b.Lines[0] != "a       b" {
		t.Errorf("Lines[0] = %q", b.Lines[0])
	}
}

// Scrollback cap with PS's hysteresis: trims only once the buffer exceeds
// Max*1.1, and then keeps exactly the last Max source lines.
func TestBufferRetentionCap(t *testing.T) {
	var b textkit.Buffer
	b.Max = 100
	b.Rewrap(80)
	for i := range 110 {
		b.Append(fmt.Sprintf("line %d", i))
	}
	if len(b.Lines) != 110 {
		t.Fatalf("trimmed early at %d lines (hysteresis is Max*1.1)", len(b.Lines))
	}
	b.Append("line 110")
	if len(b.Lines) != 100 {
		t.Fatalf("after crossing the hysteresis mark: %d lines, want 100", len(b.Lines))
	}
	if b.Lines[0] != "line 11" || b.Lines[99] != "line 110" {
		t.Errorf("kept the wrong window: %q..%q", b.Lines[0], b.Lines[99])
	}
	if len(b.Wrapped) != 100 || len(b.WrapSrc) != 100 {
		t.Errorf("wrap cache not rebuilt: %d rows", len(b.Wrapped))
	}
	if b.WrapSrc[0].Line != 0 {
		t.Errorf("WrapSrc not rebased after trim: %+v", b.WrapSrc[0])
	}
	// zero Max still has PS's 100-line floor
	var c textkit.Buffer
	c.Rewrap(80)
	for i := range 200 {
		c.Append(strings.Repeat("x", 5) + fmt.Sprint(i))
	}
	if len(c.Lines) > 111 {
		t.Errorf("unbounded growth with Max=0: %d lines", len(c.Lines))
	}
}
