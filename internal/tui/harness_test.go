package tui

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
	"github.com/yshah-aromatech/scriptorium/internal/update"
)

// noNetworkUpdateSource answers "no release found" without ever leaving the
// process — the package-wide default so Init()'s startup version check
// (root.go's versionCheckCmd, exercised by every teatest boot) never
// reaches the network. A test that wants a specific answer overrides it via
// update.SetSource + t.Cleanup, which restores exactly this default.
type noNetworkUpdateSource struct{}

func (noNetworkUpdateSource) Latest(context.Context) (string, bool, error) { return "", false, nil }
func (noNetworkUpdateSource) Replace(_ context.Context, current string) (string, error) {
	return current, nil
}

// Every test in this package is hermetic: a temp app dir, an injected crontab
// runner that refuses to write, no webhook URL, and a frozen clock. Nothing
// here may touch the real crontab, the real ~/.scriptorium or the network.
//
// time.Local is pinned to UTC before anything reads it so a frame containing a
// wall-clock label renders the same in every developer's timezone.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	update.SetSource(noNetworkUpdateSource{})
	os.Exit(m.Run())
}

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden frames under testdata/golden")

// frozen is the instant every golden is rendered at. Fixture timestamps are
// expressed relative to it, so ages ("3m", "12m") are stable.
var frozen = time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

func fixedNow() time.Time { return frozen }

// goldenSizes are the three frames the design contract is pinned at: the 80x24
// floor, a comfortable window, and a wide one (design §4).
var goldenSizes = [][2]int{{80, 24}, {120, 40}, {200, 60}}

// truecolorEnv / ansi256Env are "COLORTERM set" and "COLORTERM unset" — the two
// worlds every frame has to look right in.
var (
	truecolorEnv = []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	ansi256Env   = []string{"TERM=xterm-256color"}
)

// ---------------------------------------------------------------------------
// Fixture app
// ---------------------------------------------------------------------------

func stampAt(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// historyRow writes one history.jsonl line the way the runner would.
func historyRow(script, status string, finished time.Time, dur float64, cpuMax float64, series []float64) string {
	success := "false"
	exit := 1
	if status == "success" {
		success, exit = "true", 0
	}
	var s strings.Builder
	for i, v := range series {
		if i > 0 {
			s.WriteString(",")
		}
		fmt.Fprintf(&s, "%g", v)
	}
	return fmt.Sprintf(
		`{"event":"script_run","runId":"%s-run","script":%q,"runtime":%q,"repo":"scripts","trigger":"cron",`+
			`"status":%q,"success":%s,"exitCode":%d,"startedAt":%q,"finishedAt":%q,"durationSec":%g,"host":"sto-test",`+
			`"resources":{"cpuAvgPercent":%g,"cpuMaxPercent":%g,"memAvgMb":48.5,"memMaxMb":61.2,"samples":%d,"cpuSeries":[%s]},`+
			`"logFile":"/tmp/%s.log"}`,
		script, script, runtimeOf(script), status, success, exit,
		stampAt(finished.Add(-time.Duration(dur*float64(time.Second)))), stampAt(finished), dur,
		cpuMax/2, cpuMax, len(series), s.String(), script)
}

func runtimeOf(script string) string {
	if script == "sync-orders" || script == "heartbeat" {
		return "python"
	}
	return "powershell"
}

// fixtureScripts is the seeded fleet, chosen to cover every status the Fleet
// view has to render: one healthy, one failing, one silently missing its cron
// fire, one due within the hour, one running (externally), one never run.
var fixtureScripts = []struct {
	name, entry, desc string
}{
	{"backup-db", "backup-db.ps1", "nightly postgres dump to object storage"},
	{"cleanup-tmp", "cleanup-tmp.ps1", "prune /tmp older than 7 days"},
	{"heartbeat", "main.py", "ping the uptime endpoint"},
	{"nightly-report", "nightly-report.ps1", "assemble and mail the ops digest"},
	{"sync-orders", "main.py", "pull orders from the ERP into postgres"},
}

// fixtureSchedules is the managed crontab block's content.
var fixtureSchedules = map[string]string{
	"backup-db":      "0 2 * * *",
	"heartbeat":      "*/10 * * * *",
	"nightly-report": "*/5 * * * *",
}

func seedApp(t *testing.T) *app.App {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	scriptsDir := filepath.Join(dataDir, "scripts")

	for _, s := range fixtureScripts {
		dir := filepath.Join(scriptsDir, s.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, s.entry), "# "+s.name+"\n")
		write(t, filepath.Join(dir, "script.json"),
			fmt.Sprintf(`{"description":%q}`, s.desc))
	}

	rows := []string{
		historyRow("backup-db", "success", frozen.Add(-26*time.Hour), 41.0, 62, nil),
		historyRow("backup-db", "success", frozen.Add(-50*time.Hour), 44.0, 58, nil),
		historyRow("nightly-report", "success", frozen.Add(-5*time.Hour), 131.4, 88,
			[]float64{12, 40, 71, 88, 64, 30}),
		historyRow("sync-orders", "failure", frozen.Add(-12*time.Minute), 8.1, 34,
			[]float64{9, 22, 34, 18, 6, 2}),
		historyRow("backup-db", "success", frozen.Add(-3*time.Minute), 42.5, 66,
			[]float64{18, 44, 66, 51, 37, 21}),
		historyRow("heartbeat", "success", frozen.Add(-40*time.Second), 1.2, 7,
			[]float64{3, 7, 5, 4, 2, 1}),
	}
	write(t, filepath.Join(dataDir, "history.jsonl"), strings.Join(rows, "\n")+"\n")
	write(t, filepath.Join(appDir, "config.json"), fmt.Sprintf(`{"dataDir":%q}`, dataDir))

	a, err := app.OpenWith(appDir, fakeCrontab(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeCrontab serves the fixture's managed block and fails any write. No test
// in this package is allowed near the user's real crontab.
func fakeCrontab(t *testing.T) cron.CrontabRunner {
	t.Helper()
	lines := []string{cron.BlockStart}
	for _, name := range []string{"backup-db", "heartbeat", "nightly-report"} {
		lines = append(lines, fixtureSchedules[name]+
			" cd '/opt/scriptorium' && '/usr/local/bin/scriptorium' --run '"+name+
			"' --cron >> '/var/log/"+name+".log' 2>&1")
	}
	lines = append(lines, cron.BlockEnd)
	block := strings.Join(lines, "\n") + "\n"
	return func(_ string, args ...string) (string, bool) {
		if len(args) == 1 && args[0] == "-l" {
			return block, true
		}
		t.Errorf("the TUI must not write the crontab (args %v)", args)
		return "", false
	}
}

// newFixtureModel is a root model on the seeded app, with the machine-specific
// bits pinned and the data the tickers would have loaded already fed in.
func newFixtureModel(t *testing.T, env []string) *Model {
	t.Helper()
	m := New(seedApp(t), fixedNow)
	m.useTheme(theme.New(theme.Default, theme.Profile("auto", env)))
	m.host = "ops-01"
	m.version = "v1.4.2"

	load, ok := m.loadFleet()().(ScriptsLoadedMsg)
	if !ok {
		t.Fatal("loadFleet did not return a ScriptsLoadedMsg")
	}
	// a real FETCH_HEAD mtime would drift; pin the "synced" chip instead
	load.SyncedAt = frozen.Add(-4 * time.Minute)
	m.Update(load)

	m.Update(LiveRunsMsg{{
		Name:      "sync-orders",
		OwnerPID:  4242,
		External:  true,
		StartedAt: frozen.Add(-95 * time.Second),
	}})
	// the sweep's "missed run" warning comes back as a Cmd, which is left
	// unrun here — each golden decides what its own status line says.
	m.Update(MissedMsg{Misses: []missed.Miss{{
		Name:       "nightly-report",
		Expression: "*/5 * * * *",
		ExpectedAt: missed.NaiveNow(frozen).Add(-35 * time.Minute),
	}}})
	return m
}

// sanity: the fixture actually produced what the views are written against.
func TestFixtureShape(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	if len(m.scripts) != len(fixtureScripts) {
		t.Fatalf("discovered %d scripts, want %d: %+v", len(m.scripts), len(fixtureScripts), m.scripts)
	}
	if got := m.statuses["sync-orders"].Status; got != "failure" {
		t.Errorf("sync-orders status = %q, want failure", got)
	}
	if _, ok := m.statuses["cleanup-tmp"]; ok {
		t.Error("cleanup-tmp should have no history row")
	}
	if len(m.schedules) != 3 {
		t.Errorf("schedules = %v, want 3 entries", m.schedules)
	}
	if len(m.live) != 1 || m.live[0].Name != "sync-orders" {
		t.Errorf("live = %+v", m.live)
	}
	if _, ok := m.missed["nightly-report"]; !ok {
		t.Errorf("missed = %+v", m.missed)
	}
	if len(m.recent) == 0 {
		t.Error("no recent history rows loaded")
	}
}

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

// goldenFrames pins one model state at the three contract sizes. Each size
// gets a plain-text frame (the layout contract, the one a human reads) and the
// exact truecolor frame (the style contract); the 120x40 case additionally
// gets the COLORTERM-unset frame, which is where the 256-color downsampling
// shows up. Downsampling is width-independent, so pinning it once is the whole
// contract rather than three copies of it.
// The build func is handed the target size so a state that depends on it —
// anything the app writes into the output buffer, which is width-aware at the
// moment it is written — is built the way the running app would build it.
func goldenFrames(t *testing.T, name string, build func(t *testing.T, env []string, w, h int) *Model) {
	t.Helper()
	for _, sz := range goldenSizes {
		w, h := sz[0], sz[1]
		base := fmt.Sprintf("%s-%dx%d", name, w, h)

		m := build(t, truecolorEnv, w, h)
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		frame := m.frame()
		checkFrameShape(t, base, frame, w, h)
		checkGolden(t, base+".txt", textkit.StripANSI(frame))
		checkGolden(t, base+".ansi", frame)

		if w == 120 {
			m256 := build(t, ansi256Env, w, h)
			m256.Update(tea.WindowSizeMsg{Width: w, Height: h})
			checkGolden(t, base+".ansi256.ansi", m256.frame())
		}
	}
}

// checkFrameShape is the invariant no golden can hide: exactly h rows, none
// wider than w cells. A frame that overflows either wraps and shears every row
// below it.
func checkFrameShape(t *testing.T, name, frame string, w, h int) {
	t.Helper()
	rows := strings.Split(frame, "\n")
	if len(rows) != h {
		t.Errorf("%s: %d rows, want %d", name, len(rows), h)
	}
	for i, r := range rows {
		if got := textkit.Width(textkit.StripANSI(r)); got > w {
			t.Errorf("%s: row %d is %d cells wide, over %d: %q", name, i, got, w, textkit.StripANSI(r))
		}
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	got += "\n"
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s missing — regenerate with:\n  go test ./internal/tui -run %s -update-golden\n%v",
			path, t.Name(), err)
	}
	if string(want) != got {
		t.Errorf("golden %s does not match.\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// batchCmds unwraps a tea.Batch into its child commands (a plain command is a
// batch of one, nil is none). Tests choose which child to run: executing a
// tea.Tick child would sleep for that ticker's whole period.
func batchCmds(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		return batch
	}
	return []tea.Cmd{cmd}
}
