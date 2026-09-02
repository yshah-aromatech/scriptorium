package format_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows[1:] // drop header
}

func TestDurationFixture(t *testing.T) {
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range readCSV(t, filepath.Join(dir, "duration-format.csv")) {
		seconds, err := strconv.ParseFloat(row[0], 64)
		if err != nil {
			t.Fatal(err)
		}
		want := row[1]
		if got := format.Duration(seconds); got != want {
			t.Errorf("Duration(%v) = %q, want %q", seconds, got, want)
		}
	}
}

func TestRelativeTimeFixture(t *testing.T) {
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range readCSV(t, filepath.Join(dir, "relative-time.csv")) {
		seconds, err := strconv.ParseFloat(row[0], 64)
		if err != nil {
			t.Fatal(err)
		}
		want := row[1]
		if got := format.RelativeTime(seconds); got != want {
			t.Errorf("RelativeTime(%v) = %q, want %q", seconds, got, want)
		}
	}
}

// Spot rows from tests/Core.Tests.ps1's 'Format-StoDuration /
// Format-StoRelativeTime' Describe block.
func TestDurationPesterSpotRows(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{5.26, "5.3s"},
		{90, "1m30s"},
		{3725, "1h02m05s"},
	}
	for _, c := range cases {
		if got := format.Duration(c.seconds); got != c.want {
			t.Errorf("Duration(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

func TestRelativeTimePesterSpotRows(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{45, "45s"},
		{300, "5m"},
		{90000, "1d"},
	}
	for _, c := range cases {
		if got := format.RelativeTime(c.seconds); got != c.want {
			t.Errorf("RelativeTime(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
}
