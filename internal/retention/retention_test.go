package retention_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/retention"
)

// opts mirrors what Initialize-Sto hands Clear-StoOldData in the Pester
// fixtures: a fresh data dir with the app's default retention numbers
// (logRetentionDays 30, historyDays 30, historyMaxLines 50000).
func opts(t *testing.T) retention.Options {
	t.Helper()
	data := t.TempDir()
	logs := filepath.Join(data, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	return retention.Options{
		DataDir:          data,
		LogsDir:          logs,
		HistoryFile:      filepath.Join(data, "history.jsonl"),
		LogRetentionDays: 30,
		HistoryDays:      30,
		HistoryMaxLines:  50000,
	}
}

// addRow is the Go twin of the Pester Add-Row helper: a compact history row
// with startedAt = now - ageDays, in the app's 'fffZ' UTC format.
func addRow(t *testing.T, file, script, status string, ageDays float64, logFile string) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Duration(ageDays * float64(24*time.Hour)))
	row := map[string]string{
		"script":    script,
		"status":    status,
		"startedAt": at.Format("2006-01-02T15:04:05.000Z"),
	}
	if logFile != "" {
		row["logFile"] = logFile
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	appendLine(t, file, string(b))
}

func appendLine(t *testing.T, file, line string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func lines(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func writeLog(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func mustPrune(t *testing.T, o retention.Options, force bool) {
	t.Helper()
	if err := retention.Prune(o, force); err != nil {
		t.Fatalf("Prune(force=%v): %v", force, err)
	}
}

// 1. Pester: 'keeps 30 days, drops older, and deletes pruned rows' logs'
func TestKeepsThirtyDaysAndDeletesPrunedLogs(t *testing.T) {
	o := opts(t)
	oldLog := writeLog(t, filepath.Join(o.LogsDir, "gone.log"))
	addRow(t, o.HistoryFile, "slow", "success", 40, oldLog)
	addRow(t, o.HistoryFile, "slow", "failure", 20, "")
	addRow(t, o.HistoryFile, "slow", "success", 2, "")

	mustPrune(t, o, true)

	if got := lines(t, o.HistoryFile); len(got) != 2 {
		t.Fatalf("rows = %d, want 2:\n%s", len(got), strings.Join(got, "\n"))
	}
	if exists(oldLog) {
		t.Fatal("the pruned row's log must be deleted with it")
	}
}

// 2. Pester: 'keeps successes of frequent scripts only 1 day, failures 30 days'
func TestFrequentSuccessesKeptOneDay(t *testing.T) {
	o := opts(t)
	o.Schedules = map[string]string{"fast": "*/5 * * * *"}
	staleLog := writeLog(t, filepath.Join(o.LogsDir, "stale-ok.log"))
	addRow(t, o.HistoryFile, "fast", "success", 2, staleLog) // frequent + stale success -> pruned
	addRow(t, o.HistoryFile, "fast", "success", 0.5, "")     // fresh success -> kept
	addRow(t, o.HistoryFile, "fast", "failure", 20, "")      // failure -> kept
	addRow(t, o.HistoryFile, "fast", "killed", 20, "")       // interrupted -> kept
	addRow(t, o.HistoryFile, "slow", "success", 20, "")      // not frequent -> kept

	mustPrune(t, o, true)

	got := lines(t, o.HistoryFile)
	if len(got) != 4 {
		t.Fatalf("rows = %d, want 4:\n%s", len(got), strings.Join(got, "\n"))
	}
	if strings.Contains(strings.Join(got, ""), "stale-ok") {
		t.Fatal("the stale frequent success must be gone")
	}
	if exists(staleLog) {
		t.Fatal("its log must be deleted with it")
	}
}

// 3. Pester: 'drops corrupt rows and skips the prune when re-run within the hour'
func TestCorruptRowsDroppedAndThrottleSkips(t *testing.T) {
	o := opts(t)
	addRow(t, o.HistoryFile, "slow", "success", 2, "")
	appendLine(t, o.HistoryFile, "not json")

	mustPrune(t, o, true)
	if got := lines(t, o.HistoryFile); len(got) != 1 {
		t.Fatalf("after force prune rows = %d, want 1:\n%s", len(got), strings.Join(got, "\n"))
	}

	addRow(t, o.HistoryFile, "slow", "success", 40, "") // would be pruned, but the stamp throttles
	mustPrune(t, o, false)
	if got := lines(t, o.HistoryFile); len(got) != 2 {
		t.Fatalf("throttled prune rows = %d, want 2 (nothing pruned):\n%s", len(got), strings.Join(got, "\n"))
	}
}

// 4. Pester: 'never deletes log files outside the logs dir'
func TestNeverDeletesOutsideLogsDir(t *testing.T) {
	o := opts(t)
	outside := writeLog(t, filepath.Join(o.DataDir, "precious.log"))
	addRow(t, o.HistoryFile, "slow", "success", 40, outside)
	addRow(t, o.HistoryFile, "slow", "success", 1, "") // keeps the 40d row from being 'newest'

	mustPrune(t, o, true)

	if !exists(outside) {
		t.Fatal("a log outside LogsDir must never be deleted")
	}
}

// 5. Pester: 'never deletes from a sibling dir sharing the logs-dir prefix'
func TestSiblingDirSharingLogsPrefixSurvives(t *testing.T) {
	o := opts(t)
	sibDir := o.LogsDir + "-archive"
	if err := os.MkdirAll(sibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sib := writeLog(t, filepath.Join(sibDir, "keepme.log"))
	addRow(t, o.HistoryFile, "slow", "success", 40, sib)
	addRow(t, o.HistoryFile, "slow", "success", 1, "")

	mustPrune(t, o, true)

	if !exists(sib) {
		t.Fatal("a sibling dir sharing the LogsDir prefix must never be pruned")
	}
}

// 6. Pester: 'always keeps the newest row per script, even a stale success'
func TestNewestRowPerScriptAlwaysSurvives(t *testing.T) {
	o := opts(t)
	o.Schedules = map[string]string{"fast": "*/5 * * * *"}
	log := writeLog(t, filepath.Join(o.LogsDir, "newest.log"))
	addRow(t, o.HistoryFile, "fast", "success", 5, log) // stale by the 1-day rule, but its only row

	mustPrune(t, o, true)

	if got := lines(t, o.HistoryFile); len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	if !exists(log) {
		t.Fatal("the surviving row's log must survive with it")
	}
}

// 7. The maxLines backstop drops the oldest rows — and, unlike the policy
// prune, does NOT take their logs (parity with Clear-StoOldData).
func TestMaxLinesBackstopKeepsLogs(t *testing.T) {
	o := opts(t)
	o.HistoryMaxLines = 2
	logs := make([]string, 3)
	for i := range logs {
		name := fmt.Sprintf("s%d", i+1)
		logs[i] = writeLog(t, filepath.Join(o.LogsDir, name+".log"))
		addRow(t, o.HistoryFile, name, "success", 0, logs[i])
	}

	mustPrune(t, o, true)

	got := lines(t, o.HistoryFile)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (capped):\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], `"script":"s2"`) {
		t.Fatalf("backstop must drop the OLDEST rows; first kept row = %s", got[0])
	}
	if !exists(logs[0]) {
		t.Fatal("backstop-dropped rows must not take their logs")
	}
}

// 8. A prune already running elsewhere holds the flock — we skip, silently.
func TestHeldFlockSkipsSilently(t *testing.T) {
	o := opts(t)
	addRow(t, o.HistoryFile, "slow", "success", 40, "")
	addRow(t, o.HistoryFile, "slow", "success", 1, "") // the 40d row is prunable

	f, err := os.OpenFile(filepath.Join(o.DataDir, ".prune.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}()

	mustPrune(t, o, true) // must be a silent no-op, not an error

	if got := lines(t, o.HistoryFile); len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (history untouched while the flock is held)", len(got))
	}
}

// 9. Pester: 'prunes old logs and caps history at startup' (Initialize-Sto
// config handling) — the aged-log sweep plus the maxLines cap in one run.
func TestStartupPrunesOldLogsAndCapsHistory(t *testing.T) {
	o := opts(t)
	o.LogRetentionDays = 7
	o.HistoryMaxLines = 5

	old := writeLog(t, filepath.Join(o.LogsDir, "old.log"))
	newLog := writeLog(t, filepath.Join(o.LogsDir, "new.log"))
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(old, thirtyDaysAgo, thirtyDaysAgo); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		addRow(t, o.HistoryFile, fmt.Sprintf("s%d", i), "success", 0, "")
	}

	mustPrune(t, o, true)

	if exists(old) {
		t.Fatal("a log older than logRetentionDays must be swept")
	}
	if !exists(newLog) {
		t.Fatal("a fresh log must survive the sweep")
	}
	got := lines(t, o.HistoryFile)
	if len(got) != 5 {
		t.Fatalf("rows = %d, want 5 (historyMaxLines)", len(got))
	}
	if !strings.Contains(got[len(got)-1], `"script":"s20"`) {
		t.Fatalf("the newest row must survive the cap; last = %s", got[len(got)-1])
	}
}

// A row that is valid JSON but carries a field type Row does not model is
// from an era we don't know — it is NOT corrupt, and the prune must not
// silently delete it (ConvertFrom-Json keeps it too).
func TestUnfamiliarButValidRowSurvives(t *testing.T) {
	o := opts(t)
	at := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	appendLine(t, o.HistoryFile, `{"script":"odd","status":"success","startedAt":"`+at+`","durationSec":"60","futureKey":{"a":1}}`)
	appendLine(t, o.HistoryFile, "not json")

	mustPrune(t, o, true)

	got := lines(t, o.HistoryFile)
	if len(got) != 1 || !strings.Contains(got[0], "futureKey") {
		t.Fatalf("rows = %v, want only the unfamiliar-but-valid row", got)
	}
}

// FrequentScripts is the 10-minute boundary the frequent-success rule hangs
// on: <= 10 minutes between the next two fires is frequent, more is not.
func TestFrequentScripts(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	got := retention.FrequentScripts(map[string]string{
		"five":    "*/5 * * * *",
		"ten":     "*/10 * * * *",
		"fifteen": "*/15 * * * *",
		"hourly":  "0 * * * *",
		"bogus":   "not a cron expression",
	}, now)
	want := map[string]bool{"five": true, "ten": true}
	if len(got) != len(want) {
		t.Fatalf("FrequentScripts = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("FrequentScripts = %v, want %v", got, want)
		}
	}
}
