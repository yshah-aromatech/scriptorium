package cron_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

const layout = "2006-01-02T15:04:05"

func readCSV(t *testing.T, name string) [][]string {
	t.Helper()
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// The frozen PS truth table is the gate: every row must match exactly.
func TestTruthTable(t *testing.T) {
	rows := readCSV(t, "cron-truth.csv")
	if len(rows) != 433 {
		t.Fatalf("truth table has %d rows, want 433", len(rows))
	}
	for _, row := range rows[1:] {
		expr, fromS, wantNext, wantPrev := row[0], row[1], row[2], row[3]
		t.Run(expr+"@"+fromS, func(t *testing.T) {
			from, err := time.Parse(layout, fromS)
			if err != nil {
				t.Fatal(err)
			}
			gotNext, okN := cron.Next(expr, from)
			assertFire(t, "next", wantNext, gotNext, okN)
			gotPrev, okP := cron.Prev(expr, from)
			assertFire(t, "prev", wantPrev, gotPrev, okP)
		})
	}
}

func assertFire(t *testing.T, which, want string, got time.Time, ok bool) {
	t.Helper()
	if want == "" {
		if ok {
			t.Errorf("%s: got %s, want none", which, got.Format(layout))
		}
		return
	}
	if !ok {
		t.Errorf("%s: got none, want %s", which, want)
		return
	}
	if g := got.Format(layout); g != want {
		t.Errorf("%s: got %s, want %s", which, g, want)
	}
}

func TestValidateTable(t *testing.T) {
	rows := readCSV(t, "cron-validate.csv")
	if len(rows) != 47 {
		t.Fatalf("validate table has %d rows, want 47", len(rows))
	}
	for _, row := range rows[1:] {
		expr, want := row[0], strings.EqualFold(row[1], "true")
		t.Run("validate:"+expr, func(t *testing.T) {
			if got := cron.Validate(expr); got != want {
				t.Errorf("Validate(%q) = %v, want %v", expr, got, want)
			}
		})
	}
}

// Targeted diagnosability cases (subset of tests/Cron.Tests.ps1) — when the
// table fails wholesale these name the broken rule directly.
func TestSemanticsSpot(t *testing.T) {
	from := time.Date(2026, 7, 3, 14, 30, 45, 0, time.UTC) // Friday
	spot := []struct {
		expr string
		next string // "" = none
	}{
		{"*/15 * * * *", "2026-07-03T14:45:00"},
		{"* * * * *", "2026-07-03T14:31:00"},
		{"0 9 * * 7", "2026-07-05T09:00:00"},    // 7 == sunday
		{"0 9 * * sun", "2026-07-05T09:00:00"},  // dow name
		{"0 8 1 jan *", "2027-01-01T08:00:00"},  // month name
		{"5/15 * * * *", "2026-07-03T14:35:00"}, // from-5-step-15: {5,20,35,50}
		{"0 8 15 * 3", "2026-07-08T08:00:00"},   // vixie OR: next Wed before the 15th
		{"0 8 15 * *", "2026-07-15T08:00:00"},   // dom only
		// dom */2 starts with '*' => UNrestricted; dow-only restriction (Wed).
		// Traced by the ported rules (implementer note): next Wednesday from
		// Fri Jul 3 2026 is Jul 8, not Jul 5 (a Sunday).
		{"0 8 */2 * 3", "2026-07-08T08:00:00"},
		{"@daily", "2026-07-04T00:00:00"},
		{"@reboot", ""},
		{"0 0 31 4 *", ""}, // impossible date
	}
	for _, c := range spot {
		got, ok := cron.Next(c.expr, from)
		if c.next == "" {
			if ok {
				t.Errorf("Next(%q): got %s, want none", c.expr, got.Format(layout))
			}
			continue
		}
		if !ok || got.Format(layout) != c.next {
			t.Errorf("Next(%q) = %v,%v want %s", c.expr, got.Format(layout), ok, c.next)
		}
	}
}

// Pinned by a live PS check (controller, 2026-09-01): PS's .Split('-',2)
// resolves to the char overload, so "1-2-3" parses as ["1","2-3"] and the
// expression is invalid — Go matches. The huge-step acceptance is a
// documented deliberate divergence (PS throws an uncaught OverflowException).
func TestValidatePinnedEdges(t *testing.T) {
	if cron.Validate("1-2-3 * * * *") {
		t.Error("multi-dash range must be invalid (PS parity)")
	}
	if !cron.Validate("*/3000000000 * * * *") {
		t.Error("huge step expands from base (documented divergence)")
	}
}
