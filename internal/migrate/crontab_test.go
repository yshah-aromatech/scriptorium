package migrate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/migrate"
)

// statefulRunner is a fake crontab whose spool actually changes when written,
// so idempotency is testable. It never touches a real crontab.
type statefulRunner struct {
	spool    string
	readOK   bool
	writes   int
	readFail string // non-empty: `-l` fails WITH output (the wipe-guard arm)
}

func (r *statefulRunner) run(stdin string, args ...string) (string, bool) {
	if len(args) == 1 && args[0] == "-" {
		r.writes++
		r.spool = stdin
		return "", true
	}
	if r.readFail != "" {
		return r.readFail, false
	}
	return r.spool, r.readOK
}

func newCT(r *statefulRunner) *cron.Crontab {
	return &cron.Crontab{AppDir: "/app", LogsDir: "/logs", BinPath: "/bin/scriptorium", Run: r.run}
}

func psBlock(marker bool) string {
	start, end := cron.BlockStart, cron.BlockEnd
	if !marker {
		start, end = cron.LegacyBlockStart, cron.LegacyBlockEnd
	}
	return strings.Join([]string{
		"MAILTO=someone@example.com",
		"15 3 * * * /usr/local/bin/certbot renew",
		start,
		"*/10 * * * * cd '/old/app' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'fast-job' --cron >> '/old/logs/cron-fast-job.log' 2>&1",
		"0 2 * * * cd '/old/app' && 'pwsh' -NoProfile -File scriptorium.ps1 --run 'nightly' --cron >> '/old/logs/cron-nightly.log' 2>&1",
		end,
		"# trailing user comment",
	}, "\n") + "\n"
}

// wantGoSpelling is what the block looks like after adoption.
const wantGoSpelling = "MAILTO=someone@example.com\n" +
	"15 3 * * * /usr/local/bin/certbot renew\n" +
	"# trailing user comment\n" +
	cron.BlockStart + "\n" +
	"*/10 * * * * cd '/app' && '/bin/scriptorium' --run 'fast-job' --cron >> '/logs/cron-fast-job.log' 2>&1\n" +
	"0 2 * * * cd '/app' && '/bin/scriptorium' --run 'nightly' --cron >> '/logs/cron-nightly.log' 2>&1\n" +
	cron.BlockEnd + "\n"

// backups returns every crontab.bak.* file in dataDir.
func backups(t *testing.T, dataDir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dataDir, "crontab.bak.*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAdoptsPSSpellingBlockWithBackup(t *testing.T) {
	dataDir := t.TempDir()
	pre := psBlock(true)
	r := &statefulRunner{spool: pre, readOK: true}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
	if r.spool != wantGoSpelling {
		t.Errorf("rewritten crontab =\n%q\nwant\n%q", r.spool, wantGoSpelling)
	}

	b := backups(t, dataDir)
	if len(b) != 1 {
		t.Fatalf("backup files = %v, want exactly 1", b)
	}
	got, err := os.ReadFile(b[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pre {
		t.Errorf("backup =\n%q\nwant the exact pre-image\n%q", got, pre)
	}
}

func TestIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	r := &statefulRunner{spool: psBlock(true), readOK: true}
	ct := newCT(r)

	if _, err := migrate.Crontab(ct, dataDir); err != nil {
		t.Fatal(err)
	}
	writesAfterFirst := r.writes
	spoolAfterFirst := r.spool

	changed, err := migrate.Crontab(ct, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second call reported a change; adoption must be one-shot")
	}
	if r.writes != writesAfterFirst {
		t.Errorf("second call wrote the crontab (%d -> %d writes)", writesAfterFirst, r.writes)
	}
	if r.spool != spoolAfterFirst {
		t.Error("second call changed the spool")
	}
	if b := backups(t, dataDir); len(b) != 1 {
		t.Errorf("backup files = %v, want still exactly 1", b)
	}
}

func TestLegacyMarkersAreRewrittenUnderCurrent(t *testing.T) {
	dataDir := t.TempDir()
	r := &statefulRunner{spool: psBlock(false), readOK: true}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if strings.Contains(r.spool, "psscripts managed block") {
		t.Errorf("legacy markers survived:\n%s", r.spool)
	}
	if r.spool != wantGoSpelling {
		t.Errorf("rewritten crontab =\n%q\nwant\n%q", r.spool, wantGoSpelling)
	}
}

// A legacy-marked block that is ALREADY Go spelling still needs the marker
// rewrite — the legacy pair alone is a trigger.
func TestLegacyMarkersWithGoSpellingStillMigrate(t *testing.T) {
	dataDir := t.TempDir()
	pre := strings.Join([]string{
		cron.LegacyBlockStart,
		"@daily cd '/app' && '/bin/scriptorium' --run 'solo' --cron >> '/logs/cron-solo.log' 2>&1",
		cron.LegacyBlockEnd,
	}, "\n") + "\n"
	r := &statefulRunner{spool: pre, readOK: true}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	want := cron.BlockStart + "\n" +
		"@daily cd '/app' && '/bin/scriptorium' --run 'solo' --cron >> '/logs/cron-solo.log' 2>&1\n" +
		cron.BlockEnd + "\n"
	if r.spool != want {
		t.Errorf("rewritten crontab =\n%q\nwant\n%q", r.spool, want)
	}
	if len(backups(t, dataDir)) != 1 {
		t.Error("want a backup before the marker rewrite")
	}
}

func TestFailedReadNeverBacksUpOrWrites(t *testing.T) {
	dataDir := t.TempDir()
	r := &statefulRunner{spool: psBlock(true), readOK: true, readFail: "crontab: permission denied"}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err == nil {
		t.Error("want an error on a failed read")
	}
	if changed {
		t.Error("changed = true after a failed read")
	}
	if r.writes != 0 {
		t.Fatalf("WIPE GUARD BREACHED: %d writes after a failed read", r.writes)
	}
	if b := backups(t, dataDir); len(b) != 0 {
		t.Errorf("backup files = %v, want none", b)
	}
}

func TestNoManagedBlockIsUntouched(t *testing.T) {
	dataDir := t.TempDir()
	pre := "MAILTO=x@y.z\n15 3 * * * /usr/local/bin/certbot renew\n"
	r := &statefulRunner{spool: pre, readOK: true}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true with no managed block")
	}
	if r.writes != 0 {
		t.Errorf("writes = %d, want 0", r.writes)
	}
	if r.spool != pre {
		t.Error("the crontab was modified")
	}
	if b := backups(t, dataDir); len(b) != 0 {
		t.Errorf("backup files = %v, want none", b)
	}
}

func TestEmptyBlockIsUntouched(t *testing.T) {
	dataDir := t.TempDir()
	pre := cron.BlockStart + "\n" + cron.BlockEnd + "\n0 1 * * * /bin/true\n"
	r := &statefulRunner{spool: pre, readOK: true}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if changed || r.writes != 0 || r.spool != pre {
		t.Errorf("an entry-less block must be left alone; changed=%v writes=%d", changed, r.writes)
	}
	if b := backups(t, dataDir); len(b) != 0 {
		t.Errorf("backup files = %v, want none", b)
	}
}

func TestAlreadyGoSpellingUnderCurrentMarkersIsANoOp(t *testing.T) {
	dataDir := t.TempDir()
	r := &statefulRunner{spool: wantGoSpelling, readOK: true}

	changed, err := migrate.Crontab(newCT(r), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if changed || r.writes != 0 {
		t.Errorf("changed=%v writes=%d, want no-op", changed, r.writes)
	}
	if b := backups(t, dataDir); len(b) != 0 {
		t.Errorf("backup files = %v, want none", b)
	}
}
