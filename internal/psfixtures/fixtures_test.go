// Package psfixtures verifies the committed PowerShell-generated parity
// fixtures are present and structurally sane. Later phases consume them.
package psfixtures_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

func dir(t *testing.T) string {
	t.Helper()
	d, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

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
	return rows
}

func TestCronTruthTableShape(t *testing.T) {
	rows := readCSV(t, filepath.Join(dir(t), "cron-truth.csv"))
	const wantRows = 36*12 + 1 // 36 expressions x 12 timestamps + header
	if len(rows) != wantRows {
		t.Fatalf("cron truth table row count = %d, want %d", len(rows), wantRows)
	}
	if got := strings.Join(rows[0], ","); got != "expression,from,next,prev" {
		t.Fatalf("header = %q", got)
	}
}

func TestCronValidateTableShape(t *testing.T) {
	rows := readCSV(t, filepath.Join(dir(t), "cron-validate.csv"))
	const wantRows = 46 + 1 // 36 truth-table expressions + 10 invalid + header
	if len(rows) != wantRows {
		t.Fatalf("cron validate table row count = %d, want %d", len(rows), wantRows)
	}
	if got := strings.Join(rows[0], ","); got != "expression,valid" {
		t.Fatalf("header = %q", got)
	}
}

func TestRequiredFixturesExist(t *testing.T) {
	d := dir(t)
	for _, name := range []string{
		"rounding.csv", "duration-format.csv", "relative-time.csv",
		"history-mixed.jsonl", "webhook-payload.json",
		"webhook-queue.jsonl", "missed-state.json", "run.log",
		"cron-validate.csv",
		"env-corpus/expected.json", "config-corpus/warnings.txt",
		"crontab/current.txt", "crontab/interleaved.txt",
		"crontab/expected-schedules.json",
		"mcp/01-initialize.response.json", "mcp/04-notification.response.json",
		"mcp/10-unauthorized.response.json", "mcp/11-batch-rejected.response.json",
		"mcp/12-initialize-alt-version.response.json",
	} {
		if fi, err := os.Stat(filepath.Join(d, name)); err != nil || fi.Size() == 0 {
			t.Errorf("missing or empty fixture: %s (%v)", name, err)
		}
	}
}
