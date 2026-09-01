// Package retention is the startup data prune (port of Clear-StoOldData,
// src/Core.psm1). It runs at every app start — TUI and headless alike, so
// every cron run boots it — and is throttled to once an hour. Policy:
//
//   - history is a rolling window of HistoryDays (default 30) days
//   - scripts cron-scheduled every <=10 minutes keep success rows only 1 day;
//     failures/killed/timeout/skipped keep the full window
//   - a pruned history row deletes its log file with it; orphaned logs fall
//     back to the LogRetentionDays age prune
//   - HistoryMaxLines is only a safety backstop against pathological growth,
//     and rows it drops do NOT take their logs
//   - the newest row of every script always survives: every status surface is
//     last-row-wins per script, so losing it would read as "never run"
package retention

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/history"
)

// Options is everything the prune needs, injected: no global config, and
// Schedules is handed in rather than read from crontab so the policy stays
// testable and the prune never shells out.
type Options struct {
	DataDir     string
	LogsDir     string
	HistoryFile string

	LogRetentionDays float64
	HistoryDays      float64
	HistoryMaxLines  int

	// Schedules maps script name -> cron expression (see FrequentScripts).
	Schedules map[string]string

	// Now defaults to time.Now.
	Now func() time.Time
}

// scriptKey pulls the script name out of a raw row without a full JSON parse —
// pass 1 runs over every line of the file (port of the PS regex).
var scriptKey = regexp.MustCompile(`"script"\s*:\s*"([^"]*)"`)

// throttle is how long a completed prune suppresses the next one.
const throttle = time.Hour

// Prune applies the retention policy. force bypasses the hourly throttle.
// Returns nil for every "nothing to do" case, including another process
// already pruning — this runs on every startup and must never be loud.
func Prune(o Options, force bool) error {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}

	stamp := filepath.Join(o.DataDir, ".last-prune")
	if !force {
		if st, err := os.Stat(stamp); err == nil && now().Sub(st.ModTime()) < throttle {
			return nil
		}
	}

	// One prune at a time across processes. Taken BEFORE the stamp is touched:
	// stamping a run that then backs off would suppress the real prune for an
	// hour. Non-blocking — a concurrent prune is doing the work anyway.
	lock, err := os.OpenFile(filepath.Join(o.DataDir, ".prune.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil // an unwritable data dir is not this prune's problem to report
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil // held: another prune is already doing the work
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	_ = os.WriteFile(stamp, nil, 0o644)

	sweepLogs(o, now())
	return pruneHistory(o, now())
}

// sweepLogs removes aged and orphaned log files by mtime.
func sweepLogs(o Options, now time.Time) {
	if o.LogRetentionDays <= 0 {
		return
	}
	cutoff := now.Add(-time.Duration(o.LogRetentionDays * float64(24*time.Hour)))
	entries, err := os.ReadDir(o.LogsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(o.LogsDir, e.Name()))
	}
}

// pruneHistory rewrites history.jsonl without the rows the policy drops, then
// deletes those rows' logs. The rewrite happens only when something was
// actually dropped, and lands with a single rename(2).
func pruneHistory(o Options, now time.Time) error {
	if _, err := os.Stat(o.HistoryFile); err != nil {
		return nil // no history yet
	}
	winDays := o.HistoryDays
	if winDays <= 0 {
		winDays = 30 // historyDays=0 only changes the tab view, not retention
	}
	nowUTC := now.UTC()
	histCutoff := nowUTC.Add(-time.Duration(winDays * float64(24*time.Hour)))
	successCutoff := nowUTC.AddDate(0, 0, -1)
	frequent := FrequentScripts(o.Schedules, nowUTC)

	store := history.NewStore(o.HistoryFile)
	lines, err := store.RawLines()
	if err != nil {
		return err
	}

	// pass 1: newest row index per script — that row survives unconditionally
	newest := make(map[string]int, len(lines))
	for i, line := range lines {
		if m := scriptKey.FindStringSubmatch(line); m != nil {
			newest[m[1]] = i
		}
	}

	// pass 2: keep or drop
	keep := make([]string, 0, len(lines))
	var dropLogs []string
	dropped := 0
	for i, line := range lines {
		row, ok := parseRow(line)
		at := row.StartedAt.Time()
		if !ok || at.IsZero() {
			dropped++ // blank/corrupt rows are dead weight
			continue
		}
		at = at.UTC()
		stale := at.Before(histCutoff) ||
			(row.Status == "success" && at.Before(successCutoff) && frequent[row.Script])
		if stale && newest[row.Script] != i {
			dropped++
			if row.LogFile != nil && *row.LogFile != "" {
				dropLogs = append(dropLogs, *row.LogFile)
			}
			continue
		}
		keep = append(keep, line)
	}

	// backstop only — its casualties keep their logs, same as the PS app
	if o.HistoryMaxLines > 0 && len(keep) > o.HistoryMaxLines {
		dropped += len(keep) - o.HistoryMaxLines
		keep = keep[len(keep)-o.HistoryMaxLines:]
	}
	if dropped == 0 {
		return nil
	}

	// ponytail: a history append racing this exact instant is lost; the hourly
	// throttle, the flock and the single rename keep the window tiny.
	tmp := o.HistoryFile + ".tmp"
	var buf strings.Builder
	for _, line := range keep {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, o.HistoryFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	deleteLogs(o.LogsDir, dropLogs)
	return nil
}

// parseRow mirrors what ConvertFrom-Json decides in the PS app: only a line
// that is not valid JSON is corrupt. Valid JSON whose field TYPES don't match
// Row is a row from an era we don't model — encoding/json still fills in every
// field it could decode, and the fields the policy reads are exactly those. A
// prune must never silently delete a row just because it is unfamiliar.
func parseRow(line string) (history.Row, bool) {
	var row history.Row
	if strings.TrimSpace(line) == "" || !json.Valid([]byte(line)) {
		return row, false
	}
	_ = json.Unmarshal([]byte(line), &row)
	return row, true
}

// deleteLogs removes the pruned rows' logs — and nothing that is not ours.
// The guard is a prefix match on LogsDir plus a separator, so a sibling
// directory sharing the prefix ("<logs>-archive") can never be touched.
func deleteLogs(logsDir string, logFiles []string) {
	if len(logFiles) == 0 {
		return
	}
	root := filepath.Clean(logsDir) + string(filepath.Separator)
	for _, lf := range logFiles {
		if strings.HasPrefix(filepath.Clean(lf), root) {
			_ = os.Remove(filepath.Clean(lf))
		}
	}
}

// FrequentScripts returns script name -> true for every cron-scheduled script
// firing at 10-minute intervals or tighter (gap between its next two firings
// <= 10 min). All cron math is UTC: in a DST-observing zone the engine's naive
// date arithmetic can skip a fire across a transition.
func FrequentScripts(schedules map[string]string, now time.Time) map[string]bool {
	m := make(map[string]bool, len(schedules))
	for name, expr := range schedules {
		n1, ok := cron.Next(expr, now.UTC())
		if !ok {
			continue
		}
		n2, ok := cron.Next(expr, n1)
		if ok && n2.Sub(n1) <= 10*time.Minute {
			m[name] = true
		}
	}
	return m
}
