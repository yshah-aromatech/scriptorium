package textkit_test

import (
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// The three alternatives of the §12.8 regex, one case each plus the pathological
// ones the catch-all exists for.
func TestStripANSI(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "hello", "hello"},
		{"csi sgr", "\x1b[31mred\x1b[0m", "red"},
		{"csi private", "\x1b[?2026hsync\x1b[?2026l", "sync"},
		{"csi cursor move", "\x1b[12;1Hhere\x1b[K", "here"},
		{"osc bel", "\x1b]0;window title\atext", "text"},
		{"osc st", "\x1b]52;c;YWJj\x1b\\copied", "copied"},
		// the catch-all eats ESC *and the byte after it* — an unmatched lone
		// ESC would otherwise corrupt every downstream width computation.
		{"lone esc eats its follower", "a\x1bb", "a"},
		{"esc equals", "\x1b=app\x1b>", "app"},
		// no letter terminator, so only the catch-all fires: ESC + "[" go, the
		// params are left as visible text rather than swallowing the rest of
		// the line.
		{"unterminated csi", "\x1b[38;2;1;2", "38;2;1;2"},
		{"progress spam", "\x1b[2K\x1b[1G50%\x1b[2K\x1b[1G100%", "50%100%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textkit.StripANSI(c.in); got != c.want {
				t.Errorf("StripANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// §12.7: 8-column stops counted in DISPLAY cells, so a wide char before the tab
// consumes two columns of the run-up.
func TestExpand(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no tabs", "plain", "plain"},
		{"leading tab", "\tx", "        x"},
		{"one char then tab", "a\tb", "a       b"},
		{"exactly at a stop", "12345678\tb", "12345678        b"},
		{"seven then tab", "1234567\tb", "1234567 b"},
		{"two tabs", "a\tb\tc", "a       b       c"},
		{"wide char run-up", "日本\tx", "日本    x"}, // 4 cells used, 4 pad to col 8
		{"trailing tab", "a\t", "a       "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textkit.Expand(c.in); got != c.want {
				t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Clean is Add-TuiOutput's sanitizer in order: strip ANSI, expand tabs (only if
// a tab survived), then delete the remaining C0 controls outright.
func TestClean(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"strip then expand", "\x1b[32ma\tb\x1b[0m", "a       b"},
		{"drop c0", "a\x00b\x07c\x1fd\x7fe", "abcde"},
		{"keeps printable", "  ✓ done · 1.2s", "  ✓ done · 1.2s"},
		{"backspace progress bar", "50%\x08\x08\x0899%", "50%99%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textkit.Clean(c.in); got != c.want {
				t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	// no ESC left behind anywhere
	if strings.ContainsRune(textkit.Clean("\x1b[31m\x1bZ\x1b"), '\x1b') {
		t.Error("Clean left an ESC in the output")
	}
}
