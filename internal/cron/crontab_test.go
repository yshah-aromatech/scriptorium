package cron_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

// fakeRunner is the only crontab any test in this package ever talks to.
// NOTHING here may reach the real binary: every Crontab under test is
// constructed with Run set.
type fakeRunner struct {
	out      string   // stdout for `-l`
	ok       bool     // whether `-l` "succeeded"
	writes   []string // stdin of every `crontab -` invocation, in order
	writeErr bool     // make the write fail
	calls    []string // every argv, joined, for call-count assertions
}

func (f *fakeRunner) run(stdin string, args ...string) (string, bool) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) == 1 && args[0] == "-" {
		f.writes = append(f.writes, stdin)
		return "", !f.writeErr
	}
	return f.out, f.ok
}

// writeCalls counts `crontab -` invocations — the wipe-guard assertion.
func (f *fakeRunner) writeCalls() int {
	n := 0
	for _, c := range f.calls {
		if c == "-" {
			n++
		}
	}
	return n
}

func newCT(f *fakeRunner) *cron.Crontab {
	return &cron.Crontab{
		AppDir:  "/app",
		LogsDir: "/logs",
		BinPath: "/bin/scriptorium",
		Run:     f.run,
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	d, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(d, "crontab", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------------------------------------------------------------------
// 0. Marker bytes are the fixtures' bytes (em-dash U+2014, not a hyphen).
// ---------------------------------------------------------------------

func TestMarkerBytesMatchFixtures(t *testing.T) {
	cur := fixture(t, "current.txt")
	if !strings.Contains(cur, cron.BlockStart+"\n") {
		t.Errorf("BlockStart %q not found verbatim in current.txt", cron.BlockStart)
	}
	if !strings.Contains(cur, cron.BlockEnd+"\n") {
		t.Errorf("BlockEnd %q not found verbatim in current.txt", cron.BlockEnd)
	}
	if !strings.Contains(cron.BlockStart, "—") {
		t.Errorf("BlockStart lost its em-dash: %q", cron.BlockStart)
	}
}

// ---------------------------------------------------------------------
// 1. Parse goldens: every fixture -> expected-schedules.json.
// ---------------------------------------------------------------------

func TestSchedulesParseGoldens(t *testing.T) {
	var want map[string]map[string]string
	if err := json.Unmarshal([]byte(fixture(t, "expected-schedules.json")), &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != 2 {
		t.Fatalf("expected-schedules.json has %d files, want 2", len(want))
	}
	for name, exp := range want {
		t.Run(name, func(t *testing.T) {
			f := &fakeRunner{out: fixture(t, name), ok: true}
			got := newCT(f).Schedules()
			if !reflect.DeepEqual(got, exp) {
				t.Errorf("Schedules() = %v, want %v", got, exp)
			}
			if f.writeCalls() != 0 {
				t.Errorf("a read wrote the crontab %d times", f.writeCalls())
			}
		})
	}
}

func TestLinesReturnsEveryLine(t *testing.T) {
	f := &fakeRunner{out: fixture(t, "interleaved.txt"), ok: true}
	got := newCT(f).Lines()
	want := []string{
		"0 1 * * * /bin/true",
		cron.BlockStart,
		"5 5 * * * cd '/home/sto/scriptorium' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'solo' --cron >> '/home/sto/.scriptorium/logs/cron-solo.log' 2>&1",
		cron.BlockEnd,
		"30 6 * * 1 /usr/bin/backup-home",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lines() = %#v\nwant %#v", got, want)
	}
}

// entryExprEmptyIsSkipped: a block line with a --run name but no parsable
// 5-field/@keyword prefix contributes nothing (PS: `if ($expr)`).
func TestScheduleWithUnparsableExpressionIsSkipped(t *testing.T) {
	tab := strings.Join([]string{
		cron.BlockStart,
		"garbage --run 'nope' --cron",
		cron.BlockEnd,
	}, "\n") + "\n"
	got := newCT(&fakeRunner{out: tab, ok: true}).Schedules()
	if len(got) != 0 {
		t.Errorf("Schedules() = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------
// 2. Write goldens.
// ---------------------------------------------------------------------

func TestSaveFreshCrontabWritesSortedBlock(t *testing.T) {
	f := &fakeRunner{out: "", ok: false} // "no crontab for <user>": empty, not failed
	if err := newCT(f).Save(map[string]string{"zeta": "0 2 * * *", "alpha": "*/10 * * * *"}); err != nil {
		t.Fatal(err)
	}
	want := cron.BlockStart + "\n" +
		"*/10 * * * * cd '/app' && '/bin/scriptorium' --run 'alpha' --cron >> '/logs/cron-alpha.log' 2>&1\n" +
		"0 2 * * * cd '/app' && '/bin/scriptorium' --run 'zeta' --cron >> '/logs/cron-zeta.log' 2>&1\n" +
		cron.BlockEnd + "\n"
	if len(f.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(f.writes))
	}
	if f.writes[0] != want {
		t.Errorf("written crontab =\n%q\nwant\n%q", f.writes[0], want)
	}
}

// PS's Save-StoSchedules appends the block at the END: kept lines first, in
// their original order (even a line that appeared AFTER the block on read),
// then the block.
func TestSaveKeepsUnmanagedLinesFirstThenBlock(t *testing.T) {
	f := &fakeRunner{out: fixture(t, "current.txt"), ok: true}
	ct := newCT(f)
	if err := ct.Save(ct.Schedules()); err != nil {
		t.Fatal(err)
	}
	want := "MAILTO=someone@example.com\n" +
		"15 3 * * * /usr/local/bin/certbot renew\n" +
		"# trailing user comment\n" +
		cron.BlockStart + "\n" +
		"*/10 * * * * cd '/app' && '/bin/scriptorium' --run 'fast-job' --cron >> '/logs/cron-fast-job.log' 2>&1\n" +
		"0 2 * * * cd '/app' && '/bin/scriptorium' --run 'nightly' --cron >> '/logs/cron-nightly.log' 2>&1\n" +
		cron.BlockEnd + "\n"
	if len(f.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(f.writes))
	}
	if f.writes[0] != want {
		t.Errorf("written crontab =\n%q\nwant\n%q", f.writes[0], want)
	}
}

// Ruling 1: % is escaped in the COMMAND portion only (Cron.psm1:74). A % in
// the expression survives verbatim.
func TestSaveEscapesPercentInCommandOnly(t *testing.T) {
	f := &fakeRunner{out: "", ok: true}
	ct := &cron.Crontab{AppDir: "/ap%p", LogsDir: "/lo%gs", BinPath: "/bin/sto", Run: f.run}
	if err := ct.Save(map[string]string{"job": "0 %2 * * *"}); err != nil {
		t.Fatal(err)
	}
	want := cron.BlockStart + "\n" +
		`0 %2 * * * cd '/ap\%p' && '/bin/sto' --run 'job' --cron >> '/lo\%gs/cron-job.log' 2>&1` + "\n" +
		cron.BlockEnd + "\n"
	if f.writes[0] != want {
		t.Errorf("written crontab =\n%q\nwant\n%q", f.writes[0], want)
	}
}

func TestSaveEmptyScheduleMapDropsTheBlockEntirely(t *testing.T) {
	f := &fakeRunner{out: fixture(t, "interleaved.txt"), ok: true}
	if err := newCT(f).Save(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	want := "0 1 * * * /bin/true\n30 6 * * 1 /usr/bin/backup-home\n"
	if f.writes[0] != want {
		t.Errorf("written crontab =\n%q\nwant\n%q", f.writes[0], want)
	}
}

// PS-spelling lines in the existing block become Go-spelling lines with the
// same names and expressions (architecture compat mandate, ruling 3).
func TestSaveRewritesPSSpellingAsGoSpelling(t *testing.T) {
	f := &fakeRunner{out: fixture(t, "current.txt"), ok: true}
	ct := newCT(f)
	if err := ct.Save(ct.Schedules()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.writes[0], "-NoProfile -File scriptorium.ps1") {
		t.Errorf("PS spelling survived the rewrite:\n%s", f.writes[0])
	}
	for _, want := range []string{
		"*/10 * * * * cd '/app' && '/bin/scriptorium' --run 'fast-job' --cron >> '/logs/cron-fast-job.log' 2>&1",
		"0 2 * * * cd '/app' && '/bin/scriptorium' --run 'nightly' --cron >> '/logs/cron-nightly.log' 2>&1",
	} {
		if !strings.Contains(f.writes[0], want) {
			t.Errorf("missing rewritten line %q in:\n%s", want, f.writes[0])
		}
	}
}

// ---------------------------------------------------------------------
// 3. Wipe guard: a FAILED read must never be followed by a write.
// ---------------------------------------------------------------------

func TestFailedReadNeverWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*cron.Crontab) error
	}{
		{"Save", func(c *cron.Crontab) error { return c.Save(map[string]string{"a": "* * * * *"}) }},
		{"Set", func(c *cron.Crontab) error { return c.Set("a", "* * * * *") }},
		{"Remove", func(c *cron.Crontab) error { return c.Remove("a") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// failed with stdout == the PS Ok=$false arm
			f := &fakeRunner{out: "crontab: permission denied", ok: false}
			if err := tc.call(newCT(f)); err == nil {
				t.Error("want an error on a failed read, got nil")
			}
			if f.writeCalls() != 0 {
				t.Fatalf("WIPE GUARD BREACHED: %d `crontab -` calls after a failed read", f.writeCalls())
			}
		})
	}
}

func TestWriteFailureIsReported(t *testing.T) {
	f := &fakeRunner{out: "", ok: true, writeErr: true}
	if err := newCT(f).Save(map[string]string{"a": "* * * * *"}); err == nil {
		t.Error("want an error when `crontab -` fails, got nil")
	}
}

// TestSetSingleReadGuardsAgainstWipeOnFlakyFirstRead is the regression test
// for the double-read wipe hazard (ruling 4 / M-2): Set/Remove used to call
// c.schedules() (a full read, ok silently discarded) and THEN c.save() (a
// SECOND, independent read). If the first read failed — treated as an empty
// map, since schedules() ignores Read's ok — and a LATER read succeeded
// with sibling schedules present on disk, save's own read would see those
// siblings, merge them with the (wrongly empty-derived) map, and write back
// a block containing ONLY the schedule just being set — destroying every
// sibling that was never folded in. Reproduced here directly: the fake
// crontab runner fails the FIRST `-l` and would succeed on a second one,
// with a "sibling" schedule present in that later read. Against the fixed
// single-read shape, Set must error out immediately (before ever calling
// `crontab -`) — proven by failing the test from inside the runner if a
// write is ever attempted.
func TestSetSingleReadGuardsAgainstWipeOnFlakyFirstRead(t *testing.T) {
	reads := 0
	siblingBlock := cron.BlockStart + "\n" +
		"0 2 * * * cd '/app' && '/bin/scriptorium' --run 'sibling' --cron >> '/logs/cron-sibling.log' 2>&1\n" +
		cron.BlockEnd + "\n"
	run := func(stdin string, args ...string) (string, bool) {
		if len(args) == 1 && args[0] == "-l" {
			reads++
			if reads == 1 {
				return "crontab: temporary failure", false // the flaky first read
			}
			return siblingBlock, true // a LATER read would succeed, sibling intact
		}
		t.Fatalf("WIPE GUARD BREACHED: crontab write attempted after a failed read (args=%v, stdin=%q)", args, stdin)
		return "", false
	}
	ct := &cron.Crontab{AppDir: "/app", LogsDir: "/logs", BinPath: "/bin/scriptorium", Run: run}

	if err := ct.Set("newjob", "* * * * *"); err == nil {
		t.Fatal("Set() = nil error, want an error (the crontab read failed)")
	}
	if reads != 1 {
		t.Errorf("Set() read the crontab %d times, want exactly 1", reads)
	}

	// The real crontab (as a later, successful read would show) still has
	// its sibling schedule — nothing was ever written.
	got := ct.Schedules()
	if got["sibling"] != "0 2 * * *" {
		t.Errorf("Schedules() = %v, want the untouched sibling schedule still present", got)
	}
	if _, ok := got["newjob"]; ok {
		t.Error("newjob must not have been written")
	}
}

// ---------------------------------------------------------------------
// 4. Empty crontab (exit 1, no output) is a WRITEABLE empty, not a failure.
// ---------------------------------------------------------------------

func TestEmptyCrontabSemanticsAllowWriting(t *testing.T) {
	f := &fakeRunner{out: "", ok: false}
	if err := newCT(f).Set("job", "*/5 * * * *"); err != nil {
		t.Fatal(err)
	}
	if f.writeCalls() != 1 {
		t.Fatalf("write calls = %d, want 1", f.writeCalls())
	}
	if !strings.Contains(f.writes[0], "--run 'job' --cron") {
		t.Errorf("block not written:\n%q", f.writes[0])
	}
}

// ---------------------------------------------------------------------
// 5. Set name validation; Remove of an absent name still rewrites (PS).
// ---------------------------------------------------------------------

func TestSetRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"bad name", "has'quote", "", "semi;colon", "pct%name", "sl/ash"} {
		f := &fakeRunner{out: "", ok: true}
		if err := newCT(f).Set(name, "* * * * *"); err == nil {
			t.Errorf("Set(%q) = nil, want an error", name)
		}
		if len(f.calls) != 0 {
			t.Errorf("Set(%q) touched the crontab: %v", name, f.calls)
		}
	}
}

func TestSetAcceptsSafeNames(t *testing.T) {
	for _, name := range []string{"a", "job-1", "job_1", "job.name", "ABC123"} {
		f := &fakeRunner{out: "", ok: true}
		if err := newCT(f).Set(name, "* * * * *"); err != nil {
			t.Errorf("Set(%q) = %v, want nil", name, err)
		}
	}
}

func TestRemoveOfAbsentNameStillRewrites(t *testing.T) {
	f := &fakeRunner{out: fixture(t, "interleaved.txt"), ok: true}
	if err := newCT(f).Remove("not-there"); err != nil {
		t.Fatal(err)
	}
	if f.writeCalls() != 1 {
		t.Fatalf("write calls = %d, want 1 (PS parity: Remove always saves)", f.writeCalls())
	}
	if !strings.Contains(f.writes[0], "--run 'solo' --cron") {
		t.Errorf("the surviving schedule was dropped:\n%q", f.writes[0])
	}
}

func TestRemoveDropsTheNamedSchedule(t *testing.T) {
	f := &fakeRunner{out: fixture(t, "current.txt"), ok: true}
	if err := newCT(f).Remove("nightly"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.writes[0], "--run 'nightly'") {
		t.Errorf("'nightly' survived Remove:\n%q", f.writes[0])
	}
	if !strings.Contains(f.writes[0], "--run 'fast-job'") {
		t.Errorf("'fast-job' was lost:\n%q", f.writes[0])
	}
}

// ---------------------------------------------------------------------
// 6. Both spellings parse out of one block (compat mandate).
// ---------------------------------------------------------------------

func TestReaderParsesBothSpellings(t *testing.T) {
	tab := strings.Join([]string{
		cron.BlockStart,
		"*/10 * * * * cd '/app' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'ps-job' --cron >> '/logs/cron-ps-job.log' 2>&1",
		"@daily cd '/app' && '/bin/scriptorium' --run 'go-job' --cron >> '/logs/cron-go-job.log' 2>&1",
		cron.BlockEnd,
	}, "\n") + "\n"
	got := newCT(&fakeRunner{out: tab, ok: true}).Schedules()
	want := map[string]string{"ps-job": "*/10 * * * *", "go-job": "@daily"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Schedules() = %v, want %v", got, want)
	}
}
