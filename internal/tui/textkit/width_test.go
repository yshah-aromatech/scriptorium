package textkit_test

import (
	"encoding/base64"
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// widthDivergences are the display-width.csv rows where the Go rebuild is
// DELIBERATELY not the PS app: spec §2 pins rivo/uniseg (grapheme-aware), and
// testdata/psfixtures/README.md ("display-width.csv is informational") records
// ZWJ sequences as the expected divergence — PS sums the codepoints of a
// ZWJ-joined family emoji (4×2 = 8 cells), a grapheme-aware terminal draws one
// 2-cell cluster. Every other row of the table is a hard gate.
var widthDivergences = map[string]int{
	"8J+RqOKAjfCfkanigI3wn5Gn4oCN8J+Rpg==": 2, // 👨‍👩‍👧‍👦 — PS 8, uniseg 2
}

func TestWidthAgainstPSFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "psfixtures", "display-width.csv")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("%s: no data rows", path)
	}
	for _, row := range rows[1:] {
		b64, wantStr := row[0], row[1]
		text, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil {
			t.Fatalf("row %q: %v", b64, derr)
		}
		want, cerr := strconv.Atoi(wantStr)
		if cerr != nil {
			t.Fatalf("row %q: %v", b64, cerr)
		}
		if d, ok := widthDivergences[b64]; ok {
			want = d
		}
		if got := textkit.Width(string(text)); got != want {
			t.Errorf("Width(%q) = %d, want %d (fixture row %s)", string(text), got, want, b64)
		}
	}
}

func TestByteAtCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cell int
		want int
	}{
		{"zero", "abc", 0, 0},
		{"negative clamps", "abc", -3, 0},
		{"ascii mid", "abcdef", 3, 3},
		{"past end", "abc", 99, 3},
		// a cell landing INSIDE a wide grapheme resolves past it (the PS rule)
		{"inside a wide char rounds forward", "日本語x", 1, 3},
		{"wide after one", "日本語x", 2, 3},
		{"wide after two", "日本語x", 4, 6},
		{"combining stays one cluster", "e\u0301x", 1, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textkit.ByteAtCell(c.in, c.cell); got != c.want {
				t.Errorf("ByteAtCell(%q, %d) = %d, want %d", c.in, c.cell, got, c.want)
			}
		})
	}
}

func TestTruncateAndFit(t *testing.T) {
	cases := []struct {
		name, in string
		w        int
		want     string
	}{
		{"fits", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"cut", "abcdefgh", 5, "abcd…"},
		{"width one", "abcdefgh", 1, "…"},
		{"width zero", "abcdefgh", 0, ""},
		{"wide cut keeps cells", "日本語日本語", 5, "日本…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := textkit.Truncate(c.in, c.w)
			if got != c.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
			}
			if w := textkit.Width(got); w > c.w {
				t.Errorf("Truncate(%q, %d) = %q is %d cells wide", c.in, c.w, got, w)
			}
		})
	}
	if got := textkit.Fit("ab", 5); got != "ab   " {
		t.Errorf("Fit(ab,5) = %q", got)
	}
	if got := textkit.Fit("abcdefg", 5); got != "abcd…" {
		t.Errorf("Fit(abcdefg,5) = %q", got)
	}
	if got := textkit.Pad("日本", 6); got != "日本  " {
		t.Errorf("Pad = %q", got)
	}
}
