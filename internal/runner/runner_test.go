package runner_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

const secretValue = "e2e-secret-value"

// env is a whole app instance in a temp dir: real config, real locks, real
// history, and a webhook endpoint that records what it received.
type env struct {
	t     *testing.T
	r     *runner.Runner
	cfg   *config.Config
	paths config.Paths
	locks *lockfile.Dir
	hist  *history.Store
	sec   *secret.Registry

	mu     sync.Mutex
	bodies []string
}

func newEnv(t *testing.T, settings map[string]any) *env {
	t.Helper()
	appDir := t.TempDir()
	cfgJSON := map[string]any{
		"dataDir":           filepath.Join(appDir, "data"),
		"monitorIntervalMs": 1000,
		"logTailKb":         64,
	}
	for k, v := range settings {
		cfgJSON[k] = v
	}
	b, err := json.Marshal(cfgJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, paths, warns, err := config.Load(appDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected config warnings: %v", warns)
	}
	if err := os.MkdirAll(paths.ScriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	e := &env{t: t, cfg: cfg, paths: paths}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		e.mu.Lock()
		e.bodies = append(e.bodies, string(body))
		e.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	e.locks = lockfile.NewDir(paths.LocksDir)
	e.hist = history.NewStore(paths.HistoryFile)
	e.sec = secret.NewRegistry()
	e.r = &runner.Runner{
		Cfg:   cfg,
		Paths: paths,
		Locks: e.locks,
		Hist:  e.hist,
		Hook:  webhook.NewClient(srv.URL, 5*time.Second, paths.WebhookQueueFile),
		Sec:   e.sec,
	}
	return e
}

func (e *env) posted() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.bodies...)
}

// script writes an entry file and returns the Script the runner would have
// been handed by discovery.
func (e *env) script(name, rt, entryName, body string) scripts.Script {
	e.t.Helper()
	dir := filepath.Join(e.paths.ScriptsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	entry := filepath.Join(dir, entryName)
	if err := os.WriteFile(entry, []byte(body), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return scripts.Script{
		Name:       name,
		Dir:        dir,
		Entry:      entry,
		Runtime:    rt,
		Repo:       "fixrepo",
		EnvFile:    filepath.Join(dir, ".env"),
		EnvExample: filepath.Join(dir, ".env.example"),
		ModuleDir:  filepath.Join(dir, "modules"),
		VenvDir:    filepath.Join(e.paths.VenvsDir, name),
	}
}

type collected struct {
	row    *history.Row
	events []runner.Event
	lines  []string
}

func (e *env) run(ctx context.Context, spec runner.Spec) collected {
	e.t.Helper()
	var c collected
	row, err := e.r.RunToCompletion(ctx, spec, func(ev runner.Event) {
		c.events = append(c.events, ev)
		if ev.Kind == runner.EvLine {
			c.lines = append(c.lines, ev.Line)
		}
	})
	if err != nil {
		e.t.Fatalf("RunToCompletion: %v", err)
	}
	c.row = row
	return c
}

func (e *env) lockHeld(name string) bool { return e.locks.Probe(name) }

func topKeys(t *testing.T, jsonText string) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonText), &m); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, jsonText)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fixtureKeys is the frozen PS webhook payload's top-level key set.
func fixtureKeys(t *testing.T) []string {
	t.Helper()
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "webhook-payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	return topKeys(t, string(b))
}

// assertGoldenPayloadShape is binding test 8: the wire body's key set is
// exactly the PS fixture's, log included.
func assertGoldenPayloadShape(t *testing.T, body string) {
	t.Helper()
	want := fixtureKeys(t)
	got := topKeys(t, body)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("payload keys = %v\nwant                 %v", got, want)
	}
	var p struct {
		Resources map[string]json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cpuAvgPercent", "cpuMaxPercent", "memAvgMb", "memMaxMb", "samples"} {
		if _, ok := p.Resources[k]; !ok {
			t.Errorf("resources missing %q: %v", k, p.Resources)
		}
	}
}

func derefInt(t *testing.T, p *int) int {
	t.Helper()
	if p == nil {
		t.Fatal("expected an exitCode, got null")
	}
	return *p
}

// ---------------------------------------------------------------------------

const okPS1 = `Write-Output "hello from ok"
Write-Output $env:SECRET_TOKEN
`

func TestPowerShellRunEndToEnd(t *testing.T) {
	pwshtest.RequirePwsh(t)
	e := newEnv(t, nil)
	sc := e.script("e2e-ok", "powershell", "ok.ps1", okPS1)

	c := e.run(context.Background(), runner.Spec{
		Script:   sc,
		Trigger:  "manual",
		ExtraEnv: map[string]string{"SECRET_TOKEN": secretValue},
	})

	if c.row == nil {
		t.Fatal("no result row")
	}
	if c.row.Status != "success" {
		t.Fatalf("status = %q, lines = %q", c.row.Status, c.lines)
	}
	if got := derefInt(t, c.row.ExitCode); got != 0 {
		t.Errorf("exitCode = %d", got)
	}
	if c.row.Success == nil || !*c.row.Success {
		t.Error("success flag not set")
	}
	if c.row.Runtime != "powershell" || c.row.Repo != "fixrepo" || c.row.Trigger != "manual" {
		t.Errorf("row identity = %+v", c.row)
	}
	if c.row.LogFile == nil {
		t.Fatal("logFile is null on a real run")
	}
	logBytes, err := os.ReadFile(*c.row.LogFile)
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, "hello from ok") {
		t.Errorf("log missing the script output: %q", logText)
	}
	if !strings.Contains(logText, "***") {
		t.Errorf("log has no redaction marker: %q", logText)
	}
	if strings.Contains(logText, secretValue) {
		t.Errorf("SECRET LEAKED into the log: %q", logText)
	}
	joined := strings.Join(c.lines, "\n")
	if !strings.Contains(joined, "***") || strings.Contains(joined, secretValue) {
		t.Errorf("SECRET LEAKED into the event stream: %q", joined)
	}

	rows, err := e.hist.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RunID != c.row.RunID {
		t.Fatalf("history rows = %+v, want the one just-finished run", rows)
	}

	bodies := e.posted()
	if len(bodies) != 1 {
		t.Fatalf("webhook got %d payloads, want 1", len(bodies))
	}
	assertGoldenPayloadShape(t, bodies[0])
	var payload struct {
		Log    string `json:"log"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "success" {
		t.Errorf("payload status = %q", payload.Status)
	}
	if !strings.Contains(payload.Log, "***") || strings.Contains(payload.Log, secretValue) {
		t.Errorf("SECRET LEAKED into the webhook log tail: %q", payload.Log)
	}
	if e.lockHeld("e2e-ok") {
		t.Error("lock still held after completion")
	}
}

const readyPY = `import time
print("ready", flush=False)
time.sleep(1)
print("done")
`

func TestPythonRunEndToEnd(t *testing.T) {
	pwshtest.RequirePython(t)
	e := newEnv(t, nil)
	sc := e.script("e2e-py", "python", "main.py", readyPY)

	h, err := e.r.Start(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var firstLine, done time.Time
	var lines []string
	var row *history.Row
	for ev := range h.Events {
		switch ev.Kind {
		case runner.EvLine:
			if firstLine.IsZero() {
				firstLine = time.Now()
			}
			lines = append(lines, ev.Line)
		case runner.EvDone:
			done = time.Now()
			row = ev.Result
		}
	}
	if row == nil || row.Status != "success" {
		t.Fatalf("row = %+v, lines = %q", row, lines)
	}
	if len(lines) < 2 || lines[0] != "ready" {
		t.Fatalf("lines = %q, want the unbuffered 'ready' first", lines)
	}
	if firstLine.IsZero() || !firstLine.Before(done) {
		t.Fatal("no line arrived before the run finished")
	}
	// the script sleeps a second after printing: a line that only shows up
	// at exit means python buffered it, i.e. PYTHONUNBUFFERED did not apply
	if gap := done.Sub(firstLine); gap < 500*time.Millisecond {
		t.Errorf("first line arrived %v before exit — output looks buffered", gap)
	}
	venvPy := filepath.Join(e.paths.VenvsDir, "e2e-py", "bin", "python")
	if _, err := os.Stat(venvPy); err != nil {
		t.Errorf("venv interpreter not created: %v", err)
	}
	if row.Runtime != "python" {
		t.Errorf("runtime = %q", row.Runtime)
	}
	if e.lockHeld("e2e-py") {
		t.Error("lock still held after completion")
	}
}

func TestSkippedWhenAlreadyRunning(t *testing.T) {
	e := newEnv(t, nil)
	sc := e.script("busy", "powershell", "ok.ps1", okPS1)
	release, _, ok := e.locks.Acquire("busy")
	if !ok {
		t.Fatal("could not pre-hold the lock")
	}
	defer release()

	c := e.run(context.Background(), runner.Spec{Script: sc, Trigger: "cron"})
	if c.row == nil || c.row.Status != "skipped" {
		t.Fatalf("row = %+v", c.row)
	}
	want := "skipped: busy is already running (pid " + strconv.Itoa(os.Getpid()) + ")"
	if len(c.lines) != 1 || c.lines[0] != want {
		t.Fatalf("lines = %q, want exactly [%q]", c.lines, want)
	}
	if got := derefInt(t, c.row.ExitCode); got != -1 {
		t.Errorf("exitCode = %d, want -1", got)
	}
	if c.row.Success == nil || *c.row.Success {
		t.Error("skipped rows are not successes")
	}
	if c.row.LogFile != nil {
		t.Errorf("logFile = %v, want null", *c.row.LogFile)
	}
	if c.row.DurationSec == nil || *c.row.DurationSec != 0 {
		t.Errorf("durationSec = %v, want 0", c.row.DurationSec)
	}
	if !c.row.StartedAt.Time().Equal(c.row.FinishedAt.Time()) {
		t.Errorf("startedAt %v != finishedAt %v", c.row.StartedAt.Time(), c.row.FinishedAt.Time())
	}
	if c.row.Resources == nil || c.row.Resources.Samples != 0 || len(c.row.Resources.CPUSeries) != 0 {
		t.Errorf("resources = %+v, want zeroed with no series", c.row.Resources)
	}

	raw, err := e.hist.RawLines()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("history lines = %v", raw)
	}
	if !strings.Contains(raw[0], `"logFile":null`) {
		t.Errorf("history line must carry an explicit null logFile: %s", raw[0])
	}

	bodies := e.posted()
	if len(bodies) != 1 {
		t.Fatalf("webhook got %d payloads", len(bodies))
	}
	assertGoldenPayloadShape(t, bodies[0])
	var payload struct {
		Status string `json:"status"`
		Log    string `json:"log"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "skipped" || payload.Log != "" {
		t.Errorf("payload = %+v, want a skipped status and an empty log", payload)
	}
	// no log file was created for a skipped run
	entries, err := os.ReadDir(e.paths.LogsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("logs dir = %v, want empty", entries)
	}
}

func TestTimeoutKillsTheTree(t *testing.T) {
	pwshtest.RequirePwsh(t)
	e := newEnv(t, nil)
	sc := e.script("slow", "powershell", "slow.ps1", "Write-Output 'starting'\nStart-Sleep 30\n")

	start := time.Now()
	c := e.run(context.Background(), runner.Spec{Script: sc, Trigger: "manual", Timeout: 6 * time.Second})
	elapsed := time.Since(start)

	if c.row == nil || c.row.Status != "timeout" {
		t.Fatalf("status = %v, lines = %q", c.row, c.lines)
	}
	joined := strings.Join(c.lines, "\n")
	if !strings.Contains(joined, "timeout — killing") {
		t.Errorf("lines = %q, want the PS timeout notice", c.lines)
	}
	if !strings.Contains(joined, "run exceeded ") {
		t.Errorf("lines = %q, want the PS timeout prefix", c.lines)
	}
	if elapsed > 20*time.Second {
		t.Errorf("run took %v — the kill did not land promptly", elapsed)
	}
	if e.lockHeld("slow") {
		t.Error("lock still held after a timeout")
	}
}

// deafPY closes fds 1 and 2 and then keeps running: the run's pipes hit EOF
// while the process is very much alive. It is the shape that proves the
// completion test is PS's CONJUNCTION (Test-StoRunFinished: HasExited AND
// both readers done) and not pipes-EOF alone — a supervisor that finalizes on
// EOF would block in cmd.Wait() for the full 30 seconds with the timeout and
// the context no longer enforceable.
const deafPY = `import os, sys, time
sys.stdout.write("alive\n")
sys.stdout.flush()
os.close(1)
os.close(2)
time.sleep(30)
`

func TestTimeoutStillFiresAfterThePipesClose(t *testing.T) {
	pwshtest.RequirePython(t)
	e := newEnv(t, nil)
	sc := e.script("deaf-timeout", "python", "main.py", deafPY)

	start := time.Now()
	c := e.run(context.Background(), runner.Spec{
		Script: sc, Trigger: "manual", Timeout: 2 * time.Second})
	elapsed := time.Since(start)

	if c.row == nil || c.row.Status != "timeout" {
		t.Fatalf("status = %+v, lines = %q — the deadline stopped being enforceable once the pipes closed", c.row, c.lines)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("run took %v — the supervisor waited out the 30s sleep instead of enforcing the 2s timeout", elapsed)
	}
	if len(c.lines) == 0 || c.lines[0] != "alive" {
		t.Errorf("lines = %q, want the pre-close line first", c.lines)
	}
	if !strings.Contains(strings.Join(c.lines, "\n"), "timeout — killing") {
		t.Errorf("lines = %q, want the PS timeout notice", c.lines)
	}
	// finalized: history row, webhook report, released lock, EvDone
	rows, err := e.hist.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "timeout" {
		t.Errorf("history = %+v", rows)
	}
	if len(e.posted()) != 1 {
		t.Errorf("webhook got %d payloads, want 1", len(e.posted()))
	}
	if e.lockHeld("deaf-timeout") {
		t.Error("lock still held after a timeout")
	}
	var done int
	for _, ev := range c.events {
		if ev.Kind == runner.EvDone {
			done++
		}
	}
	if done != 1 {
		t.Errorf("%d EvDone events, want exactly 1", done)
	}
}

func TestContextCancelStillKillsAfterThePipesClose(t *testing.T) {
	pwshtest.RequirePython(t)
	e := newEnv(t, nil)
	sc := e.script("deaf-cancel", "python", "main.py", deafPY)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	h, err := e.r.Start(ctx, runner.Spec{Script: sc, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(time.Second)
		cancel()
	}()
	var row *history.Row
	for ev := range h.Events {
		if ev.Kind == runner.EvDone {
			row = ev.Result
		}
	}
	elapsed := time.Since(start)

	if row == nil || row.Status != "killed" {
		t.Fatalf("row = %+v — cancellation stopped being enforceable once the pipes closed", row)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("run took %v — the supervisor waited out the 30s sleep instead of honouring the cancel", elapsed)
	}
	if e.lockHeld("deaf-cancel") {
		t.Error("lock still held after cancellation")
	}
}

// A process that exits on its own just under the deadline is a finished run,
// never a timed-out one — PS guards its whole timeout branch with
// `-not $proc.HasExited`, so completion wins at the boundary. Repeated
// because the two events land within milliseconds of each other and the
// classification must not be a coin flip.
func TestExitAtTheDeadlineIsNotATimeout(t *testing.T) {
	pwshtest.RequirePython(t)
	e := newEnv(t, nil)
	sc := e.script("boundary", "python", "main.py", "import time\ntime.sleep(0.4)\nprint('done')\n")

	for i := 0; i < 20; i++ {
		c := e.run(context.Background(), runner.Spec{
			Script: sc, Trigger: "manual", Timeout: 600 * time.Millisecond})
		if c.row == nil || c.row.Status != "success" {
			t.Fatalf("iteration %d: status = %+v, lines = %q", i, c.row, c.lines)
		}
	}
}

func TestStartErrorIsAFullyFinalizedFailure(t *testing.T) {
	e := newEnv(t, nil)
	e.cfg.PwshBin = "/nonexistent-bin"
	sc := e.script("broken", "powershell", "ok.ps1", okPS1)
	sc.Entry = filepath.Join(sc.Dir, "does-not-exist.ps1")

	c := e.run(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if c.row == nil || c.row.Status != "failure" {
		t.Fatalf("row = %+v", c.row)
	}
	if got := derefInt(t, c.row.ExitCode); got != -1 {
		t.Errorf("exitCode = %d, want -1", got)
	}
	if len(c.lines) != 1 || !strings.HasPrefix(c.lines[0], "failed to start: ") {
		t.Fatalf("lines = %q, want one 'failed to start: ...' line", c.lines)
	}
	rows, err := e.hist.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "failure" {
		t.Fatalf("history = %+v", rows)
	}
	if len(e.posted()) != 1 {
		t.Errorf("webhook got %d payloads, want 1", len(e.posted()))
	}
	if e.lockHeld("broken") {
		t.Error("lock still held after a start failure")
	}
}

func TestCancelledContextKillsAndFinalizes(t *testing.T) {
	pwshtest.RequirePwsh(t)
	e := newEnv(t, nil)
	sc := e.script("cancelme", "powershell", "slow.ps1", "Write-Output 'up'\nStart-Sleep 30\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := e.r.Start(ctx, runner.Spec{Script: sc, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	var row *history.Row
	for ev := range h.Events {
		if ev.Kind == runner.EvDone {
			row = ev.Result
		}
	}
	if row == nil || row.Status != "killed" {
		t.Fatalf("row = %+v, want a killed run", row)
	}
	if e.lockHeld("cancelme") {
		t.Error("lock still held after cancellation")
	}
}

func TestHandleKillStopsARunningScript(t *testing.T) {
	pwshtest.RequirePwsh(t)
	e := newEnv(t, nil)
	sc := e.script("killme", "powershell", "slow.ps1", "Write-Output 'up'\nStart-Sleep 30\n")

	h, err := e.r.Start(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "killme" || h.StartedAt.IsZero() {
		t.Errorf("handle = %+v", h)
	}
	var row *history.Row
	var killed bool
	for ev := range h.Events {
		if ev.Kind == runner.EvLine && ev.Line == "up" && !killed {
			killed = true
			go h.Kill("killed")
		}
		if ev.Kind == runner.EvDone {
			row = ev.Result
		}
	}
	if !killed {
		t.Fatal("script never produced its first line")
	}
	if row == nil || row.Status != "killed" {
		t.Fatalf("row = %+v, want a killed run", row)
	}
	if e.lockHeld("killme") {
		t.Error("lock still held after a kill")
	}
}

// A grandchild that leaves our process group with setsid is invisible to a
// -pgid signal; the sampler's pid snapshot is what still reaches it. Its
// stdio goes to /dev/null so it does not hold the run's pipes open too.
const setsidPY = `import os, subprocess, sys, time
p = subprocess.Popen(["sleep", "60"], preexec_fn=os.setsid,
                     stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                     stderr=subprocess.DEVNULL)
sys.stdout.write("detached %d\n" % p.pid)
sys.stdout.flush()
time.sleep(60)
`

func TestSetsidEscapeeIsKilledFromTheSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("snapshot escalation needs /proc")
	}
	pwshtest.RequirePython(t)
	e := newEnv(t, map[string]any{"monitorIntervalMs": 100})
	sc := e.script("escapee", "python", "main.py", setsidPY)

	h, err := e.r.Start(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	escapee := 0
	var row *history.Row
	for ev := range h.Events {
		if ev.Kind == runner.EvLine && strings.HasPrefix(ev.Line, "detached ") && escapee == 0 {
			escapee, _ = strconv.Atoi(strings.TrimPrefix(ev.Line, "detached "))
			// let the sampler take at least one snapshot of the tree
			go func() {
				time.Sleep(400 * time.Millisecond)
				h.Kill("killed")
			}()
		}
		if ev.Kind == runner.EvDone {
			row = ev.Result
		}
	}
	if escapee == 0 {
		t.Fatal("escapee never reported its pid")
	}
	if row == nil || row.Status != "killed" {
		t.Fatalf("row = %+v", row)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(escapee, 0); err != nil {
			return // gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(escapee, syscall.SIGKILL)
	t.Fatalf("setsid escapee %d survived the kill", escapee)
}

const busyPY = `import time
end = time.time() + 2.0
x = 0
while time.time() < end:
    x += 1
print("burned", x)
`

func TestSamplingRecordsResources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resource sampling needs /proc")
	}
	pwshtest.RequirePython(t)
	e := newEnv(t, map[string]any{"monitorIntervalMs": 300})
	sc := e.script("busy-py", "python", "main.py", busyPY)

	c := e.run(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if c.row == nil || c.row.Status != "success" {
		t.Fatalf("row = %+v, lines = %q", c.row, c.lines)
	}
	res := c.row.Resources
	if res == nil {
		t.Fatal("no resources on a sampled run")
	}
	if res.Samples < 3 {
		t.Errorf("samples = %d, want at least 3", res.Samples)
	}
	if res.CPUMaxPercent <= 0 || res.CPUMaxPercent > 100 {
		t.Errorf("cpuMaxPercent = %v, want within (0,100]", res.CPUMaxPercent)
	}
	if res.MemMaxMb <= 0 {
		t.Errorf("memMaxMb = %v, want > 0", res.MemMaxMb)
	}
	if len(res.CPUSeries) == 0 || len(res.MemSeries) == 0 {
		t.Errorf("series = %v / %v, want non-empty", res.CPUSeries, res.MemSeries)
	}
	if len(res.CPUSeries) > 60 {
		t.Errorf("cpuSeries has %d points, want at most 60", len(res.CPUSeries))
	}
	var sampleEvents int
	for _, ev := range c.events {
		if ev.Kind == runner.EvSample {
			sampleEvents++
		}
	}
	if sampleEvents != res.Samples {
		t.Errorf("%d EvSample events but %d recorded samples", sampleEvents, res.Samples)
	}
}

// The per-script .env is registered as forced secrets and reaches the child.
func TestScriptEnvFileIsAppliedAndRedacted(t *testing.T) {
	pwshtest.RequirePwsh(t)
	e := newEnv(t, nil)
	sc := e.script("envy", "powershell", "ok.ps1", "Write-Output $env:FROM_ENV_FILE\n")
	if err := os.WriteFile(sc.EnvFile, []byte("FROM_ENV_FILE=dotenv-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := e.run(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if c.row == nil || c.row.Status != "success" {
		t.Fatalf("row = %+v, lines = %q", c.row, c.lines)
	}
	joined := strings.Join(c.lines, "\n")
	if strings.Contains(joined, "dotenv-secret-value") {
		t.Fatalf(".env value leaked: %q", joined)
	}
	if !strings.Contains(joined, "***") {
		t.Fatalf("lines = %q, want the .env value redacted (so it did reach the child)", c.lines)
	}
}

// Two runs of the same script cannot overlap: the second is skipped.
func TestSecondConcurrentRunIsSkipped(t *testing.T) {
	pwshtest.RequirePwsh(t)
	e := newEnv(t, nil)
	sc := e.script("solo", "powershell", "slow.ps1", "Write-Output 'up'\nStart-Sleep 3\n")

	h, err := e.r.Start(context.Background(), runner.Spec{Script: sc, Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var second collected
	var first *history.Row
	for ev := range h.Events {
		if ev.Kind == runner.EvLine && ev.Line == "up" && second.row == nil {
			second = e.run(context.Background(), runner.Spec{Script: sc, Trigger: "cron"})
		}
		if ev.Kind == runner.EvDone {
			first = ev.Result
		}
	}
	if second.row == nil || second.row.Status != "skipped" {
		t.Fatalf("second run = %+v, want skipped", second.row)
	}
	if first == nil || first.Status != "success" {
		t.Fatalf("first run = %+v, want success", first)
	}
}
