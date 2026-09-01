// Package psfixtures verifies the committed PowerShell-generated parity
// fixtures are present and structurally sane. Later phases consume them.
package psfixtures_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dir(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(d, "testdata", "psfixtures")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("testdata/psfixtures not found")
		}
		d = parent
	}
}

func TestCronTruthTableShape(t *testing.T) {
	f, err := os.Open(filepath.Join(dir(t), "cron-truth.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 300 {
		t.Fatalf("cron truth table suspiciously small: %d rows", len(rows))
	}
	if got := strings.Join(rows[0], ","); got != "expression,from,next,prev" {
		t.Fatalf("header = %q", got)
	}
}

func TestRequiredFixturesExist(t *testing.T) {
	d := dir(t)
	for _, name := range []string{
		"rounding.csv", "duration-format.csv", "relative-time.csv",
		"display-width.csv", "history-mixed.jsonl", "webhook-payload.json",
		"env-corpus/expected.json", "config-corpus/warnings.txt",
		"crontab/current.txt", "crontab/legacy.txt", "crontab/interleaved.txt",
		"crontab/expected-schedules.json",
		"mcp/01-initialize.response.json", "mcp/04-notification.response.json",
	} {
		if fi, err := os.Stat(filepath.Join(d, name)); err != nil || fi.Size() == 0 {
			t.Errorf("missing or empty fixture: %s (%v)", name, err)
		}
	}
}
