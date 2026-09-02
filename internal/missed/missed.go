// Package missed is the missed-run detector: the failure plain cron is
// structurally blind to — a schedule that silently stops firing (crontab
// lost, cron dead, VM was off). Detection = "the last expected fire came
// and went, and neither a cron history row nor a live lock accounts for
// it." Port of Get-StoMissedRuns / Invoke-StoMissedRunCheck
// (src/Runner.psm1 lines 700-798).
package missed

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

// Miss is one detected missed fire. ExpectedAt is in naive-local labeling
// (see NaiveNow) — callers that need the real instant must convert it back.
type Miss struct {
	Name       string
	Expression string
	ExpectedAt time.Time
}

// NaiveNow re-labels t's LOCAL wall-clock fields as a UTC time.Time,
// discarding the real zone/offset. Every cron.Prev call in this package (and
// every history timestamp compared against its result) goes through this
// first, because a crontab expression is local-time-of-day arithmetic, and
// so is the PowerShell app's [datetime] math: .NET's DateTime does calendar
// arithmetic on its field values regardless of Kind, never re-normalizing
// through a zone's DST rule. Go's time.Time in a real IANA Location does
// the opposite — constructing or comparing times can silently skip or
// double an hour across a spring-forward/fall-back transition. Relabeling
// local fields as UTC opts every subsequent comparison out of that
// normalization, reproducing the PS app's naive (and DST-fragile, by
// design parity) arithmetic exactly. Only the webhook wire (wireTime)
// converts a naive value back to a real instant.
func NaiveNow(t time.Time) time.Time {
	l := t.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

// wireTime is NaiveNow's inverse for the webhook payload: it reinterprets a
// naive value's fields as local wall-clock time and converts to the real
// UTC instant, so a reader outside this process (n8n) sees a normal,
// correctly-zoned timestamp.
func wireTime(naive time.Time) time.Time {
	return time.Date(naive.Year(), naive.Month(), naive.Day(), naive.Hour(), naive.Minute(), naive.Second(), 0, time.Local).UTC()
}

// wireLayout matches Invoke-StoMissedRunCheck's wire format exactly.
const wireLayout = "2006-01-02T15:04:05Z"

// Detect is the pure port of Get-StoMissedRuns: schedules against history
// and live locks. now must already be naive-local (NaiveNow); so must every
// value in firstSeen. A schedule absent from firstSeen is skipped — an
// "expected" fire that predates the schedule itself (or one this process
// has never stamped) is judged on a later sweep, not this one.
func Detect(schedules map[string]string, now time.Time, graceMinutes float64, firstSeen map[string]time.Time, locks *lockfile.Dir, hist *history.Store) []Miss {
	if len(schedules) == 0 {
		return nil
	}

	// newest cron-triggered run per script — rows come back oldest first, so
	// the last write per script wins, matching the PS loop's "rows are
	// chronological" comment.
	lastCron := map[string]time.Time{}
	if hist != nil {
		rows, _ := hist.Last(2000)
		for _, r := range rows {
			if r.Trigger != "cron" {
				continue
			}
			at := r.StartedAt.Time()
			if at.IsZero() {
				continue
			}
			lastCron[r.Script] = NaiveNow(at)
		}
	}

	names := make([]string, 0, len(schedules))
	for name := range schedules {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order; PS hashtable enumeration order is unspecified anyway

	var out []Miss
	for _, name := range names {
		expr := schedules[name]
		seen, ok := firstSeen[name]
		if !ok || seen.IsZero() {
			continue
		}
		expected, ok := cron.Prev(expr, now)
		if !ok || expected.IsZero() {
			continue
		}
		if expected.Before(seen) {
			continue // fire predates the schedule
		}
		if now.Sub(expected).Minutes() < graceMinutes {
			continue
		}
		if locks != nil && locks.Probe(name) {
			continue // fired and is still running (no row yet)
		}
		if last, ok := lastCron[name]; ok && !last.Before(expected.Add(-time.Minute)) {
			continue // it ran (cron starts seconds after the minute)
		}
		out = append(out, Miss{Name: name, Expression: expr, ExpectedAt: expected})
	}
	return out
}

// stateEntry is one schedule's row in missed-state.json.
type stateEntry struct {
	Expr        string
	FirstSeen   time.Time
	LastAlerted time.Time // zero == never alerted
}

// jsonEntry is stateEntry's on-disk shape: timestamps are 'o'-format text
// (what PS's .ToString('o') writes), tolerantly parsed (a corrupt or
// unrecognized value just leaves the field zero, which Detect treats the
// same as "unseen").
type jsonEntry struct {
	Expr        string  `json:"expr"`
	FirstSeen   string  `json:"firstSeen"`
	LastAlerted *string `json:"lastAlerted"`
}

// oLayout is .NET's round-trip ('o') format: seven fractional digits, then
// Z or a real offset. The values this package writes are naive (UTC-labeled
// local fields), so they render with Z.
const oLayout = "2006-01-02T15:04:05.0000000Z07:00"

// parseStateTime reads one state timestamp into naive-local labeling. PS
// writes 'o' from a Kind=Local value — a real offset suffix — so a zoned
// value converts to its local wall-clock fields (NaiveNow). A Z/zero-offset
// value (what this package writes) is already naive: its fields are kept
// as-is. A suffix-free value (PS Kind=Unspecified) is naive too. Anything
// unparseable is zero.
func parseStateTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if _, off := t.Zone(); off != 0 {
			return NaiveNow(t)
		}
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	}
	// no zone suffix; Parse accepts fractional seconds the layout omits
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	}
	return time.Time{}
}

func loadState(data []byte) map[string]*stateEntry {
	out := map[string]*stateEntry{}
	if len(bytes.TrimSpace(data)) == 0 {
		return out
	}
	var raw map[string]jsonEntry
	if json.Unmarshal(data, &raw) != nil {
		return out // corrupt JSON — fresh start, same as PS's try/catch
	}
	for name, je := range raw {
		e := &stateEntry{Expr: je.Expr, FirstSeen: parseStateTime(je.FirstSeen)}
		if je.LastAlerted != nil {
			e.LastAlerted = parseStateTime(*je.LastAlerted)
		}
		out[name] = e
	}
	return out
}

func marshalState(state map[string]*stateEntry) ([]byte, error) {
	raw := make(map[string]jsonEntry, len(state))
	for name, e := range state {
		je := jsonEntry{Expr: e.Expr, FirstSeen: e.FirstSeen.Format(oLayout)}
		if !e.LastAlerted.IsZero() {
			s := e.LastAlerted.Format(oLayout)
			je.LastAlerted = &s
		}
		raw[name] = je
	}
	return json.MarshalIndent(raw, "", "  ")
}

// Options is everything Check needs, injected — no global config.
// GraceMinutes is caller-resolved (the CLI passes cfg.MissedGraceMinutes);
// a negative value falls back to Get-StoMissedRuns' own PS default (5) for
// a caller that hasn't resolved one.
type Options struct {
	DataDir      string
	Schedules    map[string]string
	GraceMinutes float64
	Locks        *lockfile.Dir
	Hist         *history.Store
	Hook         *webhook.Client
	Now          func() time.Time
}

// missedPayload is the webhook wire body for one missed fire.
type missedPayload struct {
	Event      string `json:"event"`
	Script     string `json:"script"`
	Schedule   string `json:"schedule"`
	ExpectedAt string `json:"expectedAt"`
	DetectedAt string `json:"detectedAt"`
	Host       string `json:"host"`
}

// Check is the stateful port of Invoke-StoMissedRunCheck: stamps first-seen
// per schedule (reset when the expression changes), webhooks each missed
// fire ONCE (deduped via lastAlerted in <dataDir>/missed-state.json), and
// returns everything currently missed for the UI.
//
// nil Schedules returns (nil, nil) immediately — it means "no crontab
// reader yet" (the P7 concern), not "zero schedules configured"; an empty,
// non-nil map is a real sweep that drops every existing schedule's state.
//
// The state file is flock'd (LOCK_EX|LOCK_NB) for the whole read-detect-write
// section: a held lock returns (nil, nil) silently rather than racing another
// sweep's read-modify-write, which is what let the PS app double-send an
// alert when two cron boots landed at the same instant (see the "ponytail"
// note this replaces, src/Runner.psm1).
func Check(o Options) ([]Miss, error) {
	if o.Schedules == nil {
		return nil, nil
	}
	nowFn := o.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	realNow := nowFn()

	f, err := os.OpenFile(filepath.Join(o.DataDir, "missed-state.json"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, nil // another sweep holds it — back off rather than race its rewrite
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	state := loadState(data)

	now := NaiveNow(realNow)
	dirty := false

	firstSeen := make(map[string]time.Time, len(o.Schedules))
	for name, expr := range o.Schedules {
		e, ok := state[name]
		if !ok || e.Expr != expr {
			e = &stateEntry{Expr: expr, FirstSeen: now}
			state[name] = e
			dirty = true
		}
		firstSeen[name] = e.FirstSeen
	}
	for name := range state {
		if _, ok := o.Schedules[name]; !ok {
			delete(state, name)
			dirty = true
		}
	}

	grace := o.GraceMinutes
	if grace < 0 {
		grace = 5
	}
	misses := Detect(o.Schedules, now, grace, firstSeen, o.Locks, o.Hist)

	for _, m := range misses {
		e := state[m.Name] // always present: every schedule was stamped above
		if !e.LastAlerted.IsZero() && !m.ExpectedAt.After(e.LastAlerted) {
			continue // this fire is already alerted
		}
		e.LastAlerted = m.ExpectedAt
		dirty = true
		if o.Hook != nil {
			o.Hook.Send(missedPayload{
				Event:      "missed",
				Script:     m.Name,
				Schedule:   m.Expression,
				ExpectedAt: wireTime(m.ExpectedAt).Format(wireLayout),
				DetectedAt: realNow.UTC().Format(wireLayout),
				Host:       webhook.Host(),
			}, "missed")
		}
	}

	if dirty {
		b, merr := marshalState(state)
		if merr == nil {
			if _, err := f.Seek(0, io.SeekStart); err == nil {
				if err := f.Truncate(0); err == nil {
					_, _ = f.Write(b) // best-effort, like PS's try/catch around Set-Content
				}
			}
		}
	}

	return misses, nil
}
