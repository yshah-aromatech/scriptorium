package history_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

// Era tolerance + no-invented-fields: every parseable fixture row must
// round-trip to a JSON object whose key set EQUALS the original's.
func TestFixtureRoundTrip(t *testing.T) {
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "history-mixed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	parsed := 0
	for i, line := range lines {
		var orig map[string]any
		if json.Unmarshal([]byte(line), &orig) != nil {
			continue // the deliberately-corrupt line
		}
		var row history.Row
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d: Row unmarshal: %v", i, err)
		}
		out, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var re map[string]any
		if err := json.Unmarshal(out, &re); err != nil {
			t.Fatal(err)
		}
		for k := range re {
			if _, ok := orig[k]; !ok {
				t.Errorf("line %d: re-marshal INVENTED field %q", i, k)
			}
		}
		for k := range orig {
			if _, ok := re[k]; !ok {
				t.Errorf("line %d: re-marshal DROPPED field %q", i, k)
			}
		}
		parsed++
	}
	if parsed < 5 {
		t.Fatalf("only %d fixture rows parsed", parsed)
	}
}

func TestStampFormats(t *testing.T) {
	for _, s := range []string{`"2026-05-01T12:00:00.000Z"`, `"2026-05-01T12:00:00Z"`, `"2026-08-01T07:00:00+02:00"`} {
		var st history.Stamp
		if err := json.Unmarshal([]byte(s), &st); err != nil {
			t.Errorf("unmarshal %s: %v", s, err)
		}
	}
	st := history.Stamp(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	b, _ := json.Marshal(st)
	if string(b) != `"2026-05-01T12:00:00.000Z"` {
		t.Errorf("marshal = %s", b)
	}
}

func TestAppendAndReaders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	s := history.NewStore(p)
	now := time.Now().UTC()
	for i, name := range []string{"a", "b", "a"} {
		st := "success"
		if i == 2 {
			st = "failure"
		}
		if err := s.Append(history.Row{Event: "script_run", Script: name, Status: st,
			StartedAt:  history.Stamp(now.Add(time.Duration(i) * time.Minute)),
			FinishedAt: history.Stamp(now.Add(time.Duration(i)*time.Minute + 30*time.Second))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(p, append(mustRead(t, p), []byte("corrupt line\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Last(10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("Last: %d rows err %v, want 3 (corrupt skipped)", len(rows), err)
	}
	last := history.LastStatuses(rows)
	if last["a"].Status != "failure" || last["b"].Status != "success" {
		t.Fatalf("LastStatuses last-row-wins broken: %+v", last)
	}
	if got, _ := s.Last(2); len(got) != 2 || got[0].Script != "b" {
		t.Fatalf("Last(2) = %+v", got)
	}
	since, _ := s.SinceDays(1)
	if len(since) != 3 {
		t.Fatalf("SinceDays(1) = %d rows", len(since))
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLogTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	big := strings.Repeat("0123456789abcdef", 8192) // 128KB
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := history.LogTail(p, 64)
	if len(tail) != 64*1024 || !strings.HasSuffix(big, tail) {
		t.Fatalf("LogTail wrong: len=%d", len(tail))
	}
	if history.LogTail(filepath.Join(t.TempDir(), "missing.log"), 64) != "" {
		t.Fatal("missing file must yield empty string")
	}
	if got := history.LogTail(p, 1024); got != big {
		t.Fatal("tail larger than file must return whole file")
	}
}

// ParseRow tolerates field-type mismatches (eras we don't model): a STRING
// durationSec field decodes to the zero value, the row still appears.
func TestParseRowToleratesOddEra(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	s := history.NewStore(p)
	// durationSec is a STRING "60", not a number — an era we don't model
	line := `{"script":"odd","status":"success","startedAt":"2026-05-01T12:00:00.000Z","durationSec":"60","host":"h","logFile":"/tmp/z.log"}`
	if err := os.WriteFile(p, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Last(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("Last: %d rows err %v, want 1", len(rows), err)
	}
	if rows[0].Script != "odd" || rows[0].Status != "success" {
		t.Fatalf("parsed row = %+v, want Script=odd Status=success", rows[0])
	}
}
