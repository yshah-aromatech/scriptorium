package missed_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

func newHist(t *testing.T) *history.Store {
	t.Helper()
	return history.NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
}

func newLocks(t *testing.T) *lockfile.Dir {
	t.Helper()
	return lockfile.NewDir(t.TempDir())
}

func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func addCronRow(t *testing.T, hist *history.Store, script string, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age)
	if err := hist.Append(history.Row{Script: script, Status: "success", Trigger: "cron", StartedAt: history.Stamp(at)}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------
// Detect — case-for-case port of tests/Runner.Tests.ps1's
// 'missed-run detection' Describe block.
// ---------------------------------------------------------------------

func TestDetectFlagsMissingRun(t *testing.T) {
	now := missed.NaiveNow(time.Now())
	oldSeen := map[string]time.Time{"job": now.AddDate(0, 0, -2)}
	misses := missed.Detect(map[string]string{"job": "*/5 * * * *"}, now, 0, oldSeen, newLocks(t), newHist(t))
	if len(misses) != 1 {
		t.Fatalf("got %d misses, want 1: %+v", len(misses), misses)
	}
	if misses[0].Name != "job" {
		t.Errorf("Name = %q, want job", misses[0].Name)
	}
	if d := now.Sub(misses[0].ExpectedAt).Minutes(); d >= 6 {
		t.Errorf("ExpectedAt too far in the past: %v minutes", d)
	}
}

func TestDetectDoesNotFlagWhenRan(t *testing.T) {
	now := missed.NaiveNow(time.Now())
	oldSeen := map[string]time.Time{"job": now.AddDate(0, 0, -2)}
	hist := newHist(t)
	addCronRow(t, hist, "job", time.Minute)
	misses := missed.Detect(map[string]string{"job": "*/5 * * * *"}, now, 0, oldSeen, newLocks(t), hist)
	if len(misses) != 0 {
		t.Fatalf("got %d misses, want 0: %+v", len(misses), misses)
	}
}

func TestDetectGraceWindow(t *testing.T) {
	now := missed.NaiveNow(time.Now())
	oldSeen := map[string]time.Time{"job": now.AddDate(0, 0, -2)}
	misses := missed.Detect(map[string]string{"job": "* * * * *"}, now, 10, oldSeen, newLocks(t), newHist(t))
	if len(misses) != 0 {
		t.Fatalf("got %d misses, want 0: %+v", len(misses), misses)
	}
}

func TestDetectSkipsNewOrAbsentSchedule(t *testing.T) {
	now := missed.NaiveNow(time.Now())
	// firstSeen == now: the schedule just appeared, judged next sweep.
	misses := missed.Detect(map[string]string{"job": "*/5 * * * *"}, now, 5, map[string]time.Time{"job": now}, newLocks(t), newHist(t))
	if len(misses) != 0 {
		t.Fatalf("firstSeen==now: got %d misses, want 0", len(misses))
	}
	// absent from firstSeen entirely.
	misses = missed.Detect(map[string]string{"job": "*/5 * * * *"}, now, 5, map[string]time.Time{}, newLocks(t), newHist(t))
	if len(misses) != 0 {
		t.Fatalf("absent firstSeen: got %d misses, want 0", len(misses))
	}
}

func TestDetectSkipsLiveLock(t *testing.T) {
	now := missed.NaiveNow(time.Now())
	oldSeen := map[string]time.Time{"job": now.AddDate(0, 0, -2)}
	locks := newLocks(t)
	release, _, acquired := locks.Acquire("job")
	if !acquired {
		t.Fatal("expected to acquire the lock")
	}
	defer release()
	misses := missed.Detect(map[string]string{"job": "*/5 * * * *"}, now, 0, oldSeen, locks, newHist(t))
	if len(misses) != 0 {
		t.Fatalf("got %d misses, want 0 (live lock): %+v", len(misses), misses)
	}
}

// ---------------------------------------------------------------------
// Check — stateful wrapper.
// ---------------------------------------------------------------------

func newCheckServer(t *testing.T, count *atomic.Int32, payloads *[]missedWebhookPayload) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var p missedWebhookPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		*payloads = append(*payloads, p)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type missedWebhookPayload struct {
	Event      string `json:"event"`
	Script     string `json:"script"`
	Schedule   string `json:"schedule"`
	ExpectedAt string `json:"expectedAt"`
	DetectedAt string `json:"detectedAt"`
	Host       string `json:"host"`
}

func TestCheckStampsThenFlagsThenDedupesWebhook(t *testing.T) {
	dataDir := t.TempDir()
	var sendCount atomic.Int32
	var payloads []missedWebhookPayload
	srv := newCheckServer(t, &sendCount, &payloads)
	hook := webhook.NewClient(srv.URL, 2*time.Second, filepath.Join(dataDir, "webhook-queue.jsonl"))

	opts := missed.Options{
		DataDir:      dataDir,
		Schedules:    map[string]string{"job": "*/5 * * * *"},
		GraceMinutes: 5,
		Locks:        newLocks(t),
		Hist:         newHist(t),
		Hook:         hook,
	}

	// sweep 1: schedule just appeared -> stamped, nothing flagged.
	misses, err := missed.Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(misses) != 0 {
		t.Fatalf("sweep 1: got %d misses, want 0", len(misses))
	}

	// backdate firstSeen so the last */5 fire postdates it.
	stateFile := filepath.Join(dataDir, "missed-state.json")
	backdateFirstSeen(t, stateFile, "job", time.Now().Add(-2*time.Hour))

	// sweep 2: flagged + webhooked once.
	opts.GraceMinutes = 0
	misses, err = missed.Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(misses) != 1 {
		t.Fatalf("sweep 2: got %d misses, want 1", len(misses))
	}

	// sweep 3: still missed (returned for the UI) but NOT webhooked again.
	misses, err = missed.Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(misses) < 1 {
		t.Fatalf("sweep 3: got %d misses, want >= 1", len(misses))
	}
	if got := sendCount.Load(); got != 1 {
		t.Fatalf("webhook sent %d times, want exactly 1", got)
	}

	// wire timestamps are valid RFC3339 Z.
	if len(payloads) != 1 {
		t.Fatalf("got %d payloads, want 1", len(payloads))
	}
	p := payloads[0]
	if p.Event != "missed" || p.Script != "job" || p.Schedule != "*/5 * * * *" {
		t.Errorf("unexpected payload: %+v", p)
	}
	for _, s := range []string{p.ExpectedAt, p.DetectedAt} {
		if s == "" || s[len(s)-1] != 'Z' {
			t.Errorf("timestamp %q does not end in Z", s)
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Errorf("timestamp %q is not valid RFC3339: %v", s, err)
		}
	}
}

func backdateFirstSeen(t *testing.T, stateFile, name string, at time.Time) {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	entry, ok := state[name]
	if !ok {
		t.Fatalf("state file has no entry for %q: %s", name, data)
	}
	entry["firstSeen"] = missed.NaiveNow(at).Format(time.RFC3339)
	out, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDropsRemovedScheduleState(t *testing.T) {
	dataDir := t.TempDir()
	opts := missed.Options{
		DataDir:   dataDir,
		Schedules: map[string]string{"job": "*/5 * * * *"},
		Locks:     newLocks(t),
		Hist:      newHist(t),
	}
	if _, err := missed.Check(opts); err != nil {
		t.Fatal(err)
	}
	opts.Schedules = map[string]string{}
	if _, err := missed.Check(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "missed-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state) != 0 {
		t.Fatalf("state = %v, want empty after schedule removal", state)
	}
}

func TestCheckNilSchedulesReturnsImmediately(t *testing.T) {
	misses, err := missed.Check(missed.Options{DataDir: t.TempDir()})
	if misses != nil || err != nil {
		t.Fatalf("Check(nil Schedules) = %v, %v, want nil, nil", misses, err)
	}
}

func TestCheckFlockHeldReturnsNilAndLeavesStateUntouched(t *testing.T) {
	dataDir := t.TempDir()
	stateFile := filepath.Join(dataDir, "missed-state.json")
	if err := os.WriteFile(stateFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(stateFile, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		t.Fatal(err)
	}

	misses, err := missed.Check(missed.Options{
		DataDir:   dataDir,
		Schedules: map[string]string{"job": "*/5 * * * *"},
		Locks:     newLocks(t),
		Hist:      newHist(t),
	})
	if misses != nil || err != nil {
		t.Fatalf("Check under a held lock = %v, %v, want nil, nil", misses, err)
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Fatalf("state file changed under a held lock: %s", data)
	}
}

func TestCheckCorruptStateIsFreshStart(t *testing.T) {
	dataDir := t.TempDir()
	stateFile := filepath.Join(dataDir, "missed-state.json")
	if err := os.WriteFile(stateFile, []byte("not json at all {{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	misses, err := missed.Check(missed.Options{
		DataDir:   dataDir,
		Schedules: map[string]string{"job": "*/5 * * * *"},
		Locks:     newLocks(t),
		Hist:      newHist(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(misses) != 0 {
		t.Fatalf("got %d misses on a fresh-started schedule, want 0", len(misses))
	}
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state file is not valid JSON after recovering from corruption: %v\n%s", err, data)
	}
	if _, ok := state["job"]; !ok {
		t.Fatalf("expected 'job' to be freshly stamped, got %s", data)
	}
}
