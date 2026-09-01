// Package history is the append-only run log: one compact JSON object per
// line in history.jsonl, written by Complete-StoRun in the PowerShell app
// (src/Runner.psm1) and read by every status surface. Rows from every era of
// the app must round-trip through Row without gaining or losing a key, so
// keys that only some eras emit are pointers with omitempty and logFile — the
// one key an era wrote as an explicit null — is a pointer WITHOUT omitempty.
package history

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Resources is the per-run CPU/memory summary. Every field except the series
// is always emitted: a run with zero samples still writes zeros.
type Resources struct {
	CPUAvgPercent float64   `json:"cpuAvgPercent"`
	CPUMaxPercent float64   `json:"cpuMaxPercent"`
	MemAvgMb      float64   `json:"memAvgMb"`
	MemMaxMb      float64   `json:"memMaxMb"`
	Samples       int       `json:"samples"`
	CPUSeries     []float64 `json:"cpuSeries,omitempty"`
	MemSeries     []float64 `json:"memSeries,omitempty"`
}

// Row is one history line. Field order matches the PowerShell result object so
// rows the Go app appends are byte-comparable with the ones it replaces.
type Row struct {
	Event       string     `json:"event"`
	RunID       string     `json:"runId,omitempty"`
	Script      string     `json:"script"`
	Runtime     string     `json:"runtime,omitempty"`
	Repo        string     `json:"repo,omitempty"`
	Trigger     string     `json:"trigger,omitempty"`
	Status      string     `json:"status"`
	Success     *bool      `json:"success,omitempty"`
	ExitCode    *int       `json:"exitCode,omitempty"`
	StartedAt   Stamp      `json:"startedAt"`
	FinishedAt  Stamp      `json:"finishedAt,omitzero"`
	DurationSec *float64   `json:"durationSec,omitempty"`
	Host        string     `json:"host,omitempty"`
	Resources   *Resources `json:"resources,omitempty"`
	// LogFile has no omitempty on purpose: the app always emits the key, and
	// a skipped run emits it as null. Dropping it would rewrite history.
	LogFile *string `json:"logFile"`
}

// Store is the history file. Concurrent writers in OTHER processes are handled
// by the single-write append (below); the mutex only orders this process's own.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore returns the store for a history.jsonl path (config.Paths.HistoryFile).
func NewStore(path string) *Store { return &Store{path: path} }

// Append writes one row. The marshalled row and its newline go out in a single
// Write call on an O_APPEND fd (never interleaves mid-row with concurrent appenders),
// so a row can never interleave with a row another process (cron, MCP, TUI) is
// appending at the same instant.
func (s *Store) Append(row Row) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// RawLines returns the file's lines untouched — blank and corrupt ones
// included. Retention rewrites the file from these, so it must not lose a row
// it could not itself parse. A missing file is not an error: it is no history.
func (s *Store) RawLines() ([]string, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), nil
}

// ParseRow parses one history.jsonl line the way ConvertFrom-Json does:
// any valid-JSON object is a row (type errors on individual fields are
// eras we don't model — the other fields still decode); blank or
// invalid-JSON lines are not rows.
func ParseRow(line string) (Row, bool) {
	var r Row
	if strings.TrimSpace(line) == "" || !json.Valid([]byte(line)) {
		return r, false
	}
	_ = json.Unmarshal([]byte(line), &r)
	return r, true
}

// rows parses every line it can, skipping blank and corrupt ones — a torn
// write must not take the whole history down with it.
func (s *Store) rows() ([]Row, error) {
	lines, err := s.RawLines()
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(lines))
	for _, line := range lines {
		r, ok := ParseRow(line)
		if !ok {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Last returns the newest n parseable rows, oldest first.
func (s *Store) Last(n int) ([]Row, error) {
	rows, err := s.rows()
	if err != nil || n <= 0 || len(rows) <= n {
		return rows, err
	}
	return rows[len(rows)-n:], nil
}

// SinceDays returns every row started within the last d days. The cutoff is
// UTC on both sides, so rows written with an offset compare correctly
// (parity with Get-StoHistory -SinceDays).
func (s *Store) SinceDays(d float64) ([]Row, error) {
	rows, err := s.rows()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(d * float64(24*time.Hour)))
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		at := r.StartedAt.Time()
		if !at.IsZero() && !at.UTC().Before(cutoff) {
			out = append(out, r)
		}
	}
	return out, nil
}

// Last is the most recent run of one script.
type Last struct {
	Status      string
	At          time.Time
	DurationSec float64
	Resources   *Resources
}

// LastStatuses collapses rows to the newest per script — last row wins, the
// invariant every status surface (list badges, --list, MCP list_scripts) and
// the retention newest-row rule are built on. At is the finish time in local
// time, zero when the row has none.
func LastStatuses(rows []Row) map[string]Last {
	m := make(map[string]Last, len(rows))
	for _, r := range rows {
		if r.Script == "" {
			continue
		}
		at := r.FinishedAt.Time()
		if !at.IsZero() {
			at = at.Local()
		}
		var dur float64
		if r.DurationSec != nil {
			dur = *r.DurationSec
		}
		m[r.Script] = Last{Status: r.Status, At: at, DurationSec: dur, Resources: r.Resources}
	}
	return m
}

// LogTail returns the last tailKb KB of a log file, or "" on any error — a
// missing or unreadable log is a normal state, never a failure (port of
// Get-StoLogTail). The file is opened read-only, so a run still writing to it
// is never disturbed.
func LogTail(path string, tailKb int) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	if max := int64(tailKb) * 1024; st.Size() > max {
		if _, err := f.Seek(st.Size()-max, io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(b)
}
