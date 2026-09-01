package webhook_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

// recorder is a test webhook endpoint: it records every body it receives and
// answers with the next status from a scripted list (the last status repeats).
type recorder struct {
	mu       sync.Mutex
	bodies   []string
	statuses []int
	onBody   func(body string) // optional side effect, runs while .flush exists
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	n := len(r.bodies)
	r.bodies = append(r.bodies, string(b))
	status := http.StatusOK
	if len(r.statuses) > 0 {
		if n < len(r.statuses) {
			status = r.statuses[n]
		} else {
			status = r.statuses[len(r.statuses)-1]
		}
	}
	onBody := r.onBody
	r.mu.Unlock()
	if onBody != nil {
		onBody(string(b))
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		status = http.StatusUnsupportedMediaType
	}
	w.WriteHeader(status)
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

// harness wires a client to a recorder and a temp queue file, with the 2s
// retry sleep captured instead of slept.
type harness struct {
	c      *webhook.Client
	rec    *recorder
	srv    *httptest.Server
	qf     string
	slept  []time.Duration
	sleepM sync.Mutex
}

func newHarness(t *testing.T, statuses ...int) *harness {
	t.Helper()
	h := &harness{rec: &recorder{statuses: statuses}}
	h.srv = httptest.NewServer(h.rec)
	t.Cleanup(h.srv.Close)
	h.qf = filepath.Join(t.TempDir(), "webhook-queue.jsonl")
	h.c = webhook.NewClient(h.srv.URL, 5*time.Second, h.qf, webhook.WithSleep(func(d time.Duration) {
		h.sleepM.Lock()
		h.slept = append(h.slept, d)
		h.sleepM.Unlock()
	}))
	return h
}

func (h *harness) sleeps() []time.Duration {
	h.sleepM.Lock()
	defer h.sleepM.Unlock()
	return append([]time.Duration(nil), h.slept...)
}

func writeQueue(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func queueLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists", path)
	}
}

// numbered builds n distinct queue lines.
func numbered(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = `{"event":"script_run","n":` + strconv.Itoa(i) + `}`
	}
	return out
}

func TestSendPostsTheMarshaledPayload(t *testing.T) {
	h := newHarness(t)
	payload := map[string]any{"event": "script_run", "script": "demo", "exitCode": 0}
	if !h.c.Send(payload, "script_run") {
		t.Fatal("Send = false, want true")
	}
	got := h.rec.got()
	if len(got) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(got))
	}
	want, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != string(want) {
		t.Fatalf("body = %s, want %s", got[0], want)
	}
	if len(h.sleeps()) != 0 {
		t.Errorf("slept %v on a first-try success", h.sleeps())
	}
	mustNotExist(t, h.qf)
}

func TestSendRetriesOnceAfterTwoSeconds(t *testing.T) {
	h := newHarness(t, http.StatusInternalServerError, http.StatusOK)
	if !h.c.Send(map[string]any{"a": 1}, "script_run") {
		t.Fatal("Send = false, want true after the retry")
	}
	if got := len(h.rec.got()); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	if want := []time.Duration{2 * time.Second}; !reflect.DeepEqual(h.sleeps(), want) {
		t.Fatalf("sleeps = %v, want %v", h.sleeps(), want)
	}
	mustNotExist(t, h.qf)
}

func TestSendFailureQueuesOneCompactLine(t *testing.T) {
	h := newHarness(t, http.StatusInternalServerError)
	payload := map[string]any{"event": "script_run", "script": "demo"}
	if h.c.Send(payload, "script_run") {
		t.Fatal("Send = true, want false")
	}
	lines := queueLines(t, h.qf)
	if len(lines) != 1 {
		t.Fatalf("queue has %d lines, want 1: %v", len(lines), lines)
	}
	want, _ := json.Marshal(payload)
	if lines[0] != string(want) {
		t.Fatalf("queued %s, want %s", lines[0], want)
	}
	if len(h.sleeps()) != 1 {
		t.Errorf("sleeps = %v, want exactly the one retry gap", h.sleeps())
	}
}

func TestSendTestEventIsNeverQueued(t *testing.T) {
	h := newHarness(t, http.StatusInternalServerError)
	if h.c.Send(map[string]any{"event": "test"}, "test") {
		t.Fatal("Send = true, want false")
	}
	mustNotExist(t, h.qf)
}

func TestSendWithoutURLIsAFastFalse(t *testing.T) {
	qf := filepath.Join(t.TempDir(), "webhook-queue.jsonl")
	var slept []time.Duration
	c := webhook.NewClient("", time.Second, qf, webhook.WithSleep(func(d time.Duration) {
		slept = append(slept, d)
	}))
	if c.Send(map[string]any{"event": "script_run"}, "script_run") {
		t.Fatal("Send = true, want false")
	}
	if len(slept) != 0 {
		t.Errorf("slept %v with no URL configured", slept)
	}
	mustNotExist(t, qf)
	if c.SendRaw([]byte(`{}`)) {
		t.Error("SendRaw = true with no URL")
	}
	if n := c.FlushQueue(); n != 0 {
		t.Errorf("FlushQueue = %d with no queue file", n)
	}
}

func TestSendTestPayloadShape(t *testing.T) {
	h := newHarness(t)
	if !h.c.SendTest() {
		t.Fatal("SendTest = false")
	}
	got := h.rec.got()
	if len(got) != 1 {
		t.Fatalf("server saw %d requests", len(got))
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(got[0]), &p); err != nil {
		t.Fatal(err)
	}
	if p["event"] != "test" {
		t.Errorf("event = %v", p["event"])
	}
	if p["host"] == "" || p["host"] == nil {
		t.Errorf("host = %v", p["host"])
	}
	at, _ := p["at"].(string)
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", at); err != nil {
		t.Errorf("at = %q: %v", at, err)
	}
	if len(p) != 3 {
		t.Errorf("payload keys = %v, want exactly event/host/at", p)
	}
}

func TestFlushQueueSendsInOrderAndClearsTheFile(t *testing.T) {
	h := newHarness(t)
	lines := numbered(3)
	writeQueue(t, h.qf, lines...)
	if n := h.c.FlushQueue(); n != 3 {
		t.Fatalf("FlushQueue = %d, want 3", n)
	}
	if got := h.rec.got(); !reflect.DeepEqual(got, lines) {
		t.Fatalf("server saw %v, want %v in order", got, lines)
	}
	mustNotExist(t, h.qf)
	mustNotExist(t, h.qf+".flush")
}

func TestFlushQueueStopsAtFirstFailureAndKeepsOrder(t *testing.T) {
	h := newHarness(t, http.StatusOK, http.StatusInternalServerError)
	lines := numbered(3)
	writeQueue(t, h.qf, lines...)
	if n := h.c.FlushQueue(); n != 1 {
		t.Fatalf("FlushQueue = %d, want 1", n)
	}
	if got := queueLines(t, h.qf); !reflect.DeepEqual(got, lines[1:]) {
		t.Fatalf("queue = %v, want the unsent %v in order", got, lines[1:])
	}
	mustNotExist(t, h.qf+".flush")
}

func TestFlushQueueCapsAtFifty(t *testing.T) {
	h := newHarness(t)
	lines := numbered(60)
	writeQueue(t, h.qf, lines...)
	if n := h.c.FlushQueue(); n != 50 {
		t.Fatalf("FlushQueue = %d, want 50", n)
	}
	if got := queueLines(t, h.qf); !reflect.DeepEqual(got, lines[50:]) {
		t.Fatalf("queue = %v, want the last 10 in order", got)
	}
}

// Lines queued WHILE a flush is in flight go behind the unsent backlog, not
// in front of it — the backlog is older.
func TestFlushQueueKeepsMidFlushAppendsAtTheTail(t *testing.T) {
	h := newHarness(t, http.StatusOK, http.StatusInternalServerError)
	lines := numbered(3)
	writeQueue(t, h.qf, lines...)
	const fresh = `{"event":"script_run","n":"mid-flush"}`
	var once sync.Once
	h.rec.onBody = func(string) {
		once.Do(func() {
			// the queue file is moved aside during a flush, so this append
			// creates a brand-new queue file, exactly like a concurrent run
			f, err := os.OpenFile(h.qf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = f.WriteString(fresh + "\n")
			_ = f.Close()
		})
	}
	if n := h.c.FlushQueue(); n != 1 {
		t.Fatalf("FlushQueue = %d, want 1", n)
	}
	want := []string{lines[1], lines[2], fresh}
	if got := queueLines(t, h.qf); !reflect.DeepEqual(got, want) {
		t.Fatalf("queue = %v, want %v", got, want)
	}
}

// A flusher that died mid-pass leaves a .flush behind. After 10 minutes the
// next flush reclaims it: PS does `Get-Content .flush | Add-Content queue`,
// so the reclaimed lines land AFTER whatever is already queued.
func TestFlushQueueReclaimsAStaleFlushFile(t *testing.T) {
	h := newHarness(t)
	stale := numbered(2)
	writeQueue(t, h.qf+".flush", stale...)
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(h.qf+".flush", old, old); err != nil {
		t.Fatal(err)
	}
	const freshLine = `{"event":"script_run","n":"fresh"}`
	writeQueue(t, h.qf, freshLine)

	if n := h.c.FlushQueue(); n != 0 {
		t.Fatalf("FlushQueue = %d, want 0 — a reclaim pass sends nothing", n)
	}
	if got := len(h.rec.got()); got != 0 {
		t.Fatalf("server saw %d requests during a reclaim", got)
	}
	want := append([]string{freshLine}, stale...)
	if got := queueLines(t, h.qf); !reflect.DeepEqual(got, want) {
		t.Fatalf("queue = %v, want %v (reclaimed lines appended)", got, want)
	}
	mustNotExist(t, h.qf+".flush")
}

// A .flush younger than 10 minutes belongs to a live flusher: leave it alone.
func TestFlushQueueLeavesAFreshFlushFileAlone(t *testing.T) {
	h := newHarness(t)
	writeQueue(t, h.qf+".flush", numbered(2)...)
	young := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(h.qf+".flush", young, young); err != nil {
		t.Fatal(err)
	}
	const freshLine = `{"event":"script_run","n":"fresh"}`
	writeQueue(t, h.qf, freshLine)

	if n := h.c.FlushQueue(); n != 0 {
		t.Fatalf("FlushQueue = %d, want 0", n)
	}
	if got := len(h.rec.got()); got != 0 {
		t.Fatalf("server saw %d requests", got)
	}
	if got := queueLines(t, h.qf); !reflect.DeepEqual(got, []string{freshLine}) {
		t.Fatalf("queue = %v, want it untouched", got)
	}
	if got := queueLines(t, h.qf+".flush"); !reflect.DeepEqual(got, numbered(2)) {
		t.Fatalf(".flush = %v, want it untouched", got)
	}
}

// A .flush that still shares its inode with the queue file is a claim caught
// between its link and its unlink, not an abandoned pass — and because the
// link preserves the mtime, such a claim looks arbitrarily old to the stale
// check. Reclaiming it would append the backlog to the very file it was
// linked from (duplicating every line) and then unlink the live claim.
func TestFlushQueueLeavesAMidClaimSharedInodeAlone(t *testing.T) {
	h := newHarness(t)
	lines := numbered(2)
	writeQueue(t, h.qf, lines...)
	if err := os.Link(h.qf, h.qf+".flush"); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(h.qf+".flush", old, old); err != nil {
		t.Fatal(err)
	}

	if n := h.c.FlushQueue(); n != 0 {
		t.Fatalf("FlushQueue = %d, want 0 — the claim is another flusher's", n)
	}
	if got := len(h.rec.got()); got != 0 {
		t.Fatalf("server saw %d requests", got)
	}
	if got := queueLines(t, h.qf); !reflect.DeepEqual(got, lines) {
		t.Fatalf("queue = %v, want %v — the backlog was duplicated into its own file", got, lines)
	}
	if got := queueLines(t, h.qf+".flush"); !reflect.DeepEqual(got, lines) {
		t.Fatalf(".flush = %v, want the live claim untouched", got)
	}
}

// A successful Send drains the backlog behind it.
func TestSendFlushesTheQueueAfterASuccess(t *testing.T) {
	h := newHarness(t)
	backlog := numbered(2)
	writeQueue(t, h.qf, backlog...)
	if !h.c.Send(map[string]any{"event": "script_run", "script": "now"}, "script_run") {
		t.Fatal("Send = false")
	}
	got := h.rec.got()
	if len(got) != 3 {
		t.Fatalf("server saw %d requests, want the live one plus 2 flushed", len(got))
	}
	if got[1] != backlog[0] || got[2] != backlog[1] {
		t.Fatalf("flushed %v, want %v in order", got[1:], backlog)
	}
	mustNotExist(t, h.qf)
}

func TestSendRawRejectsNon2xx(t *testing.T) {
	h := newHarness(t, http.StatusBadGateway)
	if h.c.SendRaw([]byte(`{"a":1}`)) {
		t.Fatal("SendRaw = true on a 502")
	}
}

func TestHostHasNoDomainSuffix(t *testing.T) {
	if got := webhook.Host(); strings.Contains(got, ".") {
		t.Fatalf("Host() = %q, want the machine name without a domain suffix", got)
	}
}
