package app_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
)

// fakeCrontab serves one canned block and refuses to be written. NOTHING in
// this package may reach the real crontab binary: every App under test is
// opened through OpenWith with this runner.
func fakeCrontab(t *testing.T, block string) cron.CrontabRunner {
	t.Helper()
	return func(stdin string, args ...string) (string, bool) {
		if len(args) == 1 && args[0] == "-l" {
			return block, true
		}
		t.Errorf("app must not write the crontab (args %v)", args)
		return "", false
	}
}

func managedBlock(name, expr string) string {
	return strings.Join([]string{
		cron.BlockStart,
		expr + " cd '/opt/scriptorium' && '/usr/local/bin/scriptorium' --run '" + name +
			"' --cron >> '/tmp/cron-" + name + ".log' 2>&1",
		cron.BlockEnd,
	}, "\n") + "\n"
}

func setup(t *testing.T, cfgExtra string) (appDir, dataDir string) {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir = t.TempDir()
	dataDir = filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"dataDir":%q%s}`, dataDir, cfgExtra)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return appDir, dataDir
}

// The frequent-success retention rule is live: a script cron-scheduled every
// <=10 minutes keeps its success rows for one day only. This is the WIRING
// test — the policy itself is covered in internal/retention.
func TestOpenPrunesWithRealSchedules(t *testing.T) {
	appDir, dataDir := setup(t, "")

	old := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02T15:04:05.000Z")
	recent := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	lines := []string{
		fmt.Sprintf(`{"script":"job","status":"success","trigger":"cron","startedAt":%q,"durationSec":1}`, old),
		fmt.Sprintf(`{"script":"job","status":"success","trigger":"cron","startedAt":%q,"durationSec":1}`, recent),
	}
	histFile := filepath.Join(dataDir, "history.jsonl")
	if err := os.WriteFile(histFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := app.OpenWith(appDir, fakeCrontab(t, managedBlock("job", "*/5 * * * *"))); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(histFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), old) {
		t.Errorf("the 3-day-old success row survived a */5 schedule:\n%s", got)
	}
	if !strings.Contains(string(got), recent) {
		t.Errorf("the newest row must always survive:\n%s", got)
	}
}

// Same data, no schedule: the frequent rule does not apply and the old
// success row stays inside the 30-day history window.
func TestOpenWithoutSchedulesKeepsOldSuccessRows(t *testing.T) {
	appDir, dataDir := setup(t, "")

	old := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02T15:04:05.000Z")
	recent := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	lines := []string{
		fmt.Sprintf(`{"script":"job","status":"success","trigger":"cron","startedAt":%q,"durationSec":1}`, old),
		fmt.Sprintf(`{"script":"job","status":"success","trigger":"cron","startedAt":%q,"durationSec":1}`, recent),
	}
	histFile := filepath.Join(dataDir, "history.jsonl")
	if err := os.WriteFile(histFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := app.OpenWith(appDir, fakeCrontab(t, "")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(histFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), old) {
		t.Errorf("an unscheduled script's old success row was pruned:\n%s", got)
	}
}

func TestOpenExposesTheManagedSchedules(t *testing.T) {
	appDir, _ := setup(t, "")
	a, err := app.OpenWith(appDir, fakeCrontab(t, managedBlock("job", "@daily")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Cron == nil {
		t.Fatal("App.Cron is nil")
	}
	if got := a.Cron.Schedules()["job"]; got != "@daily" {
		t.Errorf("Schedules()[\"job\"] = %q, want \"@daily\"", got)
	}
	if a.Cron.AppDir != a.Paths.AppDir || a.Cron.LogsDir != a.Paths.LogsDir {
		t.Errorf("Crontab paths = %q/%q, want %q/%q", a.Cron.AppDir, a.Cron.LogsDir, a.Paths.AppDir, a.Paths.LogsDir)
	}
	if a.Cron.BinPath == "" {
		t.Error("Crontab.BinPath is empty — scheduled lines would have no command")
	}
}
