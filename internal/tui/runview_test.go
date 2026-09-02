package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/procstat"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

func runAt(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.mode = modeRun
	m.relayout()
	return m
}

// The two panes and the details card, with the card gone below 14 body rows —
// at that height the output needs the rows more (inventory §1.12).
func TestRunLayout(t *testing.T) {
	m := runAt(t, 120, 40)
	frame := plainFrame(m)
	for _, want := range []string{"─ scripts", "─ output", "─ details", "▸ entry", "⚙ env", "↻ cron", "✦ last"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the 120×40 Run frame is missing %q:\n%s", want, frame)
		}
	}
	// 16 rows: 13 body rows, under the details floor
	small := plainFrame(runAt(t, 120, 16))
	if strings.Contains(small, "─ details") {
		t.Errorf("the details card survived below 14 body rows:\n%s", small)
	}
	if !strings.Contains(small, "─ output") {
		t.Errorf("the output pane vanished at 16 rows:\n%s", small)
	}
}

// The details card describes the SELECTED script, and follows the selection.
func TestDetailsCardFollowsSelection(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.selectByName(m, "heartbeat")
	frame := plainFrame(m)
	for _, want := range []string{"heartbeat · python", "heartbeat/main.py", "*/10 * * * *", "last success"} {
		if !strings.Contains(frame, want) {
			t.Errorf("details card is missing %q:\n%s", want, frame)
		}
	}
	m.run.selectByName(m, "cleanup-tmp")
	frame = plainFrame(m)
	if !strings.Contains(frame, "never run") {
		t.Errorf("a script with no history should say so:\n%s", frame)
	}
	if !strings.Contains(frame, "not scheduled") {
		t.Errorf("an unscheduled script should say so:\n%s", frame)
	}
}

// tab moves the keyboard between panes and the rules say which has it.
func TestFocusSwitch(t *testing.T) {
	m := runAt(t, 120, 40)
	if m.focus != focusList {
		t.Fatal("the Run view should start on the list")
	}
	listRule := frameRow(m, "─ scripts")
	press(m, "tab")
	if m.focus != focusOutput {
		t.Fatal("tab did not move focus to the output")
	}
	if frameRow(m, "─ scripts") == listRule {
		t.Error("the pane rules render identically focused and unfocused")
	}
	press(m, "tab")
	if m.focus != focusList {
		t.Error("tab did not move focus back")
	}
}

// frameRow returns the styled row of the frame containing needle.
func frameRow(m *Model, needle string) string {
	for _, l := range strings.Split(m.frame(), "\n") {
		if strings.Contains(textkit.StripANSI(l), needle) {
			return l
		}
	}
	return ""
}

// Scrolling away from the tail drops follow; reaching the bottom (or `end`)
// picks it back up. The count of what arrived off-screen is shown, so a live
// run never looks stalled.
func TestOutputFollowAndScroll(t *testing.T) {
	m := runAt(t, 120, 40)
	m.focus = focusOutput
	for i := range 200 {
		m.run.out.append(fmt.Sprintf("line %d", i))
	}
	if !m.run.out.follow {
		t.Fatal("a fresh pane should follow")
	}
	press(m, "up", "up", "up")
	if m.run.out.follow {
		t.Error("scrolling back did not disengage follow")
	}
	if m.run.out.behind() != 3 {
		t.Errorf("behind = %d, want 3", m.run.out.behind())
	}
	if !strings.Contains(plainFrame(m), "▼ 3 more — end follows") {
		t.Errorf("the off-screen count is not shown:\n%s", plainFrame(m))
	}
	press(m, "end")
	if !m.run.out.follow || m.run.out.behind() != 0 {
		t.Error("end did not re-engage follow")
	}
	// scrolling all the way back down re-engages it without a keystroke
	press(m, "up")
	press(m, "down")
	if !m.run.out.follow {
		t.Error("reaching the tail again did not re-engage follow")
	}
	// g/G still work, and the top is not the tail
	press(m, "g")
	if m.run.out.follow || m.run.out.scroll != 0 {
		t.Errorf("g = scroll %d follow %v", m.run.out.scroll, m.run.out.follow)
	}
}

// Output arriving while the pane is held back must keep arriving.
func TestOutputKeepsArrivingWhileScrolledBack(t *testing.T) {
	m := runAt(t, 120, 40)
	m.focus = focusOutput
	for i := range 100 {
		m.run.out.append(fmt.Sprintf("line %d", i))
	}
	press(m, "g")
	before := len(m.run.out.buf.Lines)
	m.run.out.append("late arrival")
	if len(m.run.out.buf.Lines) != before+1 {
		t.Error("a line arriving while scrolled back was dropped")
	}
	if m.run.out.scroll != 0 {
		t.Error("the viewport moved under the user")
	}
}

func TestScrollbarGeometry(t *testing.T) {
	cases := []struct{ scroll, h, total, wantLen, wantAt int }{
		{0, 10, 10, 0, 0},  // nothing to scroll
		{0, 10, 100, 1, 0}, // long buffer, thumb at the top
		{90, 10, 100, 1, 9},
		{0, 10, 20, 5, 0},
		{10, 10, 20, 5, 5},
	}
	for _, c := range cases {
		gotLen, gotAt := scrollbar(c.scroll, c.h, c.total)
		if gotLen != c.wantLen || gotAt != c.wantAt {
			t.Errorf("scrollbar(%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.scroll, c.h, c.total, gotLen, gotAt, c.wantLen, c.wantAt)
		}
	}
}

// Output lines are colored by what they say (inventory §1.12).
func TestOutputColouring(t *testing.T) {
	th := theme.New(theme.Default, theme.Profile("auto", truecolorEnv))
	cases := []struct {
		line string
		want string
	}{
		{"─── ✓ backup-db · success", th.S.Success.Render("─── ✓ backup-db · success")},
		{"─── ✗ backup-db · failure", th.S.Danger.Render("─── ✗ backup-db · failure")},
		{"─── ⊘ backup-db · killed", th.S.Warning.Render("─── ⊘ backup-db · killed")},
		{"WARNING: deprecated", th.S.Muted.Render("WARNING: deprecated")},
		{"connection failed after 3 tries", th.S.Danger.Render("connection failed after 3 tries")},
		{"plain progress output", "plain progress output"},
		{"errors_total 0", "errors_total 0"}, // word boundary: not an error line
	}
	for _, c := range cases {
		if got := colorLine(th, c.line); got != c.want {
			t.Errorf("colorLine(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// ETA is the upper median of the successful runs in history (inventory §4.12),
// computed once at launch.
func TestEtaMedian(t *testing.T) {
	m := runAt(t, 120, 40)
	// fixture backup-db successes: 41.0, 44.0, 42.5 -> sorted 41, 42.5, 44
	if got := etaSeconds(m.app, "backup-db"); got != 42.5 {
		t.Errorf("eta = %v, want the upper median 42.5", got)
	}
	if got := etaSeconds(m.app, "sync-orders"); got != 0 {
		t.Errorf("a script with no SUCCESSFUL history should have no eta, got %v", got)
	}
	if got := etaSeconds(m.app, "cleanup-tmp"); got != 0 {
		t.Errorf("a script with no history at all should have no eta, got %v", got)
	}
}

// The status line owns the bar while a run is live: what, how long, how much
// longer — and it counts over rather than parking at 100%.
func TestRunStatusLine(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")
	m.run.startedAt = frozen.Add(-20 * time.Second)
	m.run.etaSec = 42.5
	m.run.lastSample = procstat.Sample{CPU: 61.5}

	line, busy := m.run.statusLine(m, 120)
	if !busy {
		t.Fatal("a live run did not claim the status bar")
	}
	plain := textkit.StripANSI(line)
	for _, want := range []string{"backup-db", "20s", "left", "cpu 61.5%"} {
		if !strings.Contains(plain, want) {
			t.Errorf("status line %q is missing %q", plain, want)
		}
	}
	// past the estimate it counts over
	m.run.startedAt = frozen.Add(-60 * time.Second)
	over, _ := m.run.statusLine(m, 120)
	if !strings.Contains(textkit.StripANSI(over), "over") {
		t.Errorf("an overrunning run should count over: %q", textkit.StripANSI(over))
	}
	// a run outranks a transient message
	m.Update(StatusMsg{Text: "something else", Kind: StatusInfo})
	if strings.Contains(textkit.StripANSI(m.statusBar()), "something else") {
		t.Error("a transient message displaced the live run line")
	}
}

// ---------------------------------------------------------------------------
// Queue (inventory §4.11)
// ---------------------------------------------------------------------------

func TestQueueSemantics(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")

	send(m, cmdMsg(m.run.start(m, m.scripts[1])))
	send(m, cmdMsg(m.run.start(m, m.scripts[2])))
	if len(m.run.queue) != 2 {
		t.Fatalf("queue = %+v, want two entries", m.run.queue)
	}
	if !strings.Contains(m.statusText, "position 2") {
		t.Errorf("the second queued run did not report its position: %q", m.statusText)
	}
	if got := plainFrame(m); !strings.Contains(got, "2 queued") {
		t.Errorf("the queue depth is not on screen:\n%s", got)
	}
	// a queued script wears the queued badge
	if !m.run.isQueued(m.scripts[1].Name) {
		t.Error("a queued script is not reported as queued")
	}

	// the gate never dead-locks on a mode that cannot block
	for _, mode := range []mode{modeFleet, modeRun, modeHistory, modeSchedules} {
		m.mode = mode
		if !m.queueUnblocked() {
			t.Errorf("mode %v blocks the queue, but no overlay exists yet", mode)
		}
	}

	// X clears it
	m.Update(cmdMsg(m.run.clearQueue()))
	if len(m.run.queue) != 0 || !strings.Contains(m.statusText, "cleared 2") {
		t.Errorf("X left queue=%v status=%q", m.run.queue, m.statusText)
	}
}

// A queued entry is re-resolved BY NAME at dequeue: a sync while it waits
// replaces every script value, and a script that vanished is reported.
func TestQueueReresolvesByName(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.queue = []queued{{Name: "no-such-script"}}
	msg := cmdMsg(m.run.dequeue(m))
	got, ok := msg.(StatusMsg)
	if !ok || !strings.Contains(got.Text, "no longer exists") {
		t.Errorf("dequeuing a vanished script gave %#v", msg)
	}
	if len(m.run.queue) != 0 {
		t.Error("the vanished entry was not consumed")
	}

	// and a live handle holds the queue back entirely
	m.run.handle = fakeHandle("backup-db")
	m.run.queue = []queued{{Name: "heartbeat"}}
	if cmd := m.run.dequeue(m); cmd != nil {
		t.Error("the queue drained while something was still running")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func cmdMsg(c tea.Cmd) tea.Msg {
	if c == nil {
		return nil
	}
	return c()
}

// send applies a message and then the single message its command produced,
// which is how the program itself behaves for a one-step reaction.
func send(m *Model, msg tea.Msg) {
	_, cmd := m.Update(msg)
	if out := cmdMsg(cmd); out != nil {
		if _, batched := out.(tea.BatchMsg); !batched {
			m.Update(out)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }
func f64Ptr(f float64) *float64 {
	return &f
}

func TestGoldensRun(t *testing.T) {
	goldenFrames(t, "run", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		return m
	})

	// A finished run, driven through the real completion path — the banner,
	// the stats and the log line are what onRunDone actually writes, at the
	// width it actually writes them, so the golden pins the shipped output
	// rather than a hand-typed imitation of it.
	goldenFrames(t, "run-output-focus", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.focus = focusOutput
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.run.selectByName(m, "backup-db")
		m.run.handle = fakeHandle("backup-db")
		m.run.out.begin("run: backup-db")
		m.run.out.append("", banner("▶ backup-db · started 14:29:57", m.run.out.contentWidth()),
			"connecting to postgres://db.internal:5432",
			"dumping schema public (18 tables)",
			"WARNING: table audit_log is 4.2GB, this will take a while",
			"uploading backup-2026-09-02.dump to s3://ops-backups",
			"upload failed once, retrying")
		m.run.onRunDone(m, RunDoneMsg{Row: doneRow("backup-db", "success", 0, 42.5)})
		return m
	})

	goldenFrames(t, "run-live", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.run.selectByName(m, "backup-db")
		m.run.handle = fakeHandle("backup-db")
		m.run.startedAt = frozen.Add(-18 * time.Second)
		m.run.etaSec = 42.5
		m.run.lastSample = procstat.Sample{CPU: 61.5, MemMB: 58.2}
		m.run.queue = []queued{{Name: "heartbeat"}}
		m.run.out.begin("run: backup-db")
		m.run.out.append("", banner("▶ backup-db · started 14:29:42", m.run.out.contentWidth()),
			"connecting to postgres://db.internal:5432",
			"dumping schema public (18 tables)")
		return m
	})
}

// fakeHandle is a handle for the render tests: nothing feeds its channel and
// nothing kills it, which is all a frame needs to draw "this is running".
func fakeHandle(name string) *runner.Handle {
	return &runner.Handle{
		Name:      name,
		StartedAt: frozen,
		Events:    make(chan runner.Event),
		Kill:      func(string) {},
	}
}

// row builds a finished history row the way the runner would.
func doneRow(script, status string, exit int, dur float64) *history.Row {
	log := "/var/log/scriptorium/" + script + ".log"
	return &history.Row{
		Event: "script_run", Script: script, Status: status,
		Success: boolPtr(status == "success"), ExitCode: intPtr(exit),
		DurationSec: f64Ptr(dur), LogFile: &log,
		Resources: &history.Resources{CPUAvgPercent: 33, CPUMaxPercent: 66, MemAvgMb: 48.5, MemMaxMb: 61.2},
	}
}

// readLog returns the lines of the newest log file in the app's logs dir.
func readLog(t *testing.T, dir, script string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	newest := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), script+"-") {
			if e.Name() > newest {
				newest = e.Name()
			}
		}
	}
	if newest == "" {
		t.Fatalf("no log file for %s in %s", script, dir)
	}
	f, err := os.Open(filepath.Join(dir, newest))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// A finished run writes its own epitaph into the output pane and the status
// bar, and asks for the fleet data to be reloaded so the badge and the next
// ETA are current.
func TestRunDoneReportsEverywhere(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")
	m.run.out.begin("run: backup-db")

	cmds := batchCmds(m.run.onRunDone(m, RunDoneMsg{Row: doneRow("backup-db", "failure", 3, 12.25)}))
	if m.run.handle != nil {
		t.Error("the handle was not released")
	}
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"✗ backup-db · failure · exit 3 · 12.2s", "cpu avg 33% / peak 66%", "log:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the completion report is missing %q:\n%s", want, out)
		}
	}
	var sawStatus, sawReload bool
	for _, c := range cmds {
		switch msg := c().(type) {
		case StatusMsg:
			sawStatus = true
			if msg.Kind != StatusErr || !strings.Contains(msg.Text, "backup-db: failure") {
				t.Errorf("status = %+v", msg)
			}
		case ScriptsLoadedMsg:
			sawReload = true
		}
	}
	if !sawStatus || !sawReload {
		t.Errorf("run completion: status=%v fleet reload=%v", sawStatus, sawReload)
	}
}

// The completion stats must not be left to the wrapper: at the floor it folds
// mid-word, which on this line splits a number from its unit ("peak" /
// "61.2MB"). Each stat line is emitted to fit instead.
func TestCompletionStatsNeverSplitAValue(t *testing.T) {
	for _, w := range []int{80, 120, 200} {
		m := runAt(t, w, 24)
		m.run.handle = fakeHandle("backup-db")
		m.run.out.begin("run: backup-db")
		before := len(m.run.out.buf.Lines)
		m.run.onRunDone(m, RunDoneMsg{Row: doneRow("backup-db", "success", 0, 12.25)})

		var stats []string
		for _, l := range m.run.out.buf.Lines[before:] {
			if strings.Contains(l, "cpu avg") || strings.Contains(l, "mem avg") {
				stats = append(stats, l)
			}
		}
		if len(stats) == 0 {
			t.Fatalf("width %d: no stats line was emitted", w)
		}
		for _, l := range stats {
			if got := textkit.Width(l); got > m.run.out.contentWidth() {
				t.Errorf("width %d: stats line is %d cells, over the %d-cell pane — it will fold mid-value: %q",
					w, got, m.run.out.contentWidth(), l)
			}
		}
		// and every value stays whole on whichever line carries it
		joined := strings.Join(stats, "\n")
		for _, want := range []string{"peak 66%", "peak 61.2MB"} {
			if !strings.Contains(joined, want) {
				t.Errorf("width %d: %q was split across lines:\n%s", w, want, joined)
			}
		}
	}
}

// A finished run hands the queue its turn, re-resolved by name.
func TestRunDoneDrainsTheQueue(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")
	m.run.queue = []queued{{Name: "gone-away"}}
	for _, c := range batchCmds(m.run.onRunDone(m, RunDoneMsg{Row: doneRow("backup-db", "success", 0, 1)})) {
		if msg, ok := c().(StatusMsg); ok && strings.Contains(msg.Text, "no longer exists") {
			return
		}
	}
	t.Error("the queue was not drained when the run finished")
}

// Sync streams into the same pane through the same batched drain, and says how
// it went. Nothing here starts a real git fetch: the drain is fed directly, the
// way the producer goroutine would.
func TestSyncStreamsIntoTheOutputPane(t *testing.T) {
	m := runAt(t, 120, 40)
	ch := make(chan taskEvent, 8)
	m.run.syncCh = ch
	m.run.syncing = true
	m.run.out.begin("sync")

	ch <- taskEvent{Line: "Fetching origin"}
	ch <- taskEvent{Line: "Already up to date."}
	ch <- taskEvent{Done: true, OK: true}
	close(ch)

	cmd := drainSync(ch)
	for cmd != nil {
		msg, ok := cmd().(SyncEventsMsg)
		if !ok {
			t.Fatal("the sync drain produced the wrong message")
		}
		cmd = m.run.onSyncEvents(m, msg)
		if msg.Closed {
			break
		}
	}
	out := strings.Join(m.run.out.buf.Lines, "\n")
	for _, want := range []string{"Fetching origin", "Already up to date.", "✓ sync · done"} {
		if !strings.Contains(out, want) {
			t.Errorf("the sync output is missing %q:\n%s", want, out)
		}
	}
	if m.run.syncing {
		t.Error("the sync never released its flag")
	}
	// and it refuses to stack on a live run
	m.run.handle = fakeHandle("backup-db")
	if msg, ok := cmdMsg(m.run.sync(m)).(StatusMsg); !ok || !strings.Contains(msg.Text, "already running") {
		t.Errorf("sync during a run gave %#v", msg)
	}
}

// Click focuses the pane under the pointer and picks the row it landed on;
// the wheel scrolls whichever pane it is over (inventory §1.11).
func TestMouseFocusAndSelection(t *testing.T) {
	m := runAt(t, 120, 40)
	lay := runLayoutFor(120, m.bodyHeight())

	// a click in the output pane takes the keyboard there
	m.Update(tea.MouseClickMsg{X: lay.listW + 5, Y: 6, Button: tea.MouseLeft})
	if m.focus != focusOutput {
		t.Error("a click in the output pane did not focus it")
	}
	// and a click on a list row takes it back and selects that row
	m.Update(tea.MouseClickMsg{X: 3, Y: headerRows + 3, Button: tea.MouseLeft})
	if m.focus != focusList {
		t.Error("a click in the list pane did not focus it")
	}
	if got := m.run.list.Index(); got != 2 {
		t.Errorf("clicked row selected index %d, want 2", got)
	}

	// the wheel over the output scrolls it without moving the selection
	for i := range 100 {
		m.run.out.append(fmt.Sprintf("line %d", i))
	}
	before := m.run.list.Index()
	m.Update(tea.MouseWheelMsg{X: lay.listW + 5, Y: 6, Button: tea.MouseWheelUp})
	if m.run.out.follow {
		t.Error("the wheel over the output did not scroll it")
	}
	if m.run.list.Index() != before {
		t.Error("the wheel over the output moved the list selection")
	}
	// and over the list it moves the selection
	m.Update(tea.MouseWheelMsg{X: 3, Y: 6, Button: tea.MouseWheelUp})
	if m.run.list.Index() == before {
		t.Error("the wheel over the list did not move the selection")
	}
}

// The Fleet view answers the same gestures.
func TestFleetMouse(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(tea.MouseClickMsg{X: 4, Y: headerRows + 2 + 3, Button: tea.MouseLeft})
	if m.fleet.sel != 3 {
		t.Errorf("clicked fleet row selected %d, want 3", m.fleet.sel)
	}
	m.Update(tea.MouseWheelMsg{X: 4, Y: 6, Button: tea.MouseWheelUp})
	if m.fleet.sel != 2 {
		t.Errorf("wheel up left the selection at %d", m.fleet.sel)
	}
}

// Queueing announces itself through the taxonomy's own message.
func TestQueuedMessage(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = fakeHandle("backup-db")
	msg, ok := cmdMsg(m.run.start(m, m.scripts[1])).(RunQueuedMsg)
	if !ok {
		t.Fatalf("queueing produced %T, want RunQueuedMsg", msg)
	}
	if msg.Position != 1 || msg.Name != m.scripts[1].Name {
		t.Errorf("queued msg = %+v", msg)
	}
	send(m, msg)
	if !strings.Contains(m.statusText, "position 1") {
		t.Errorf("status = %q", m.statusText)
	}
}
