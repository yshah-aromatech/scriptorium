package tui

import (
	"context"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// Run lifecycle. Everything asynchronous here is a tea.Cmd — including
// Handle.Kill, which blocks for up to the three-second kill grace and would
// freeze rendering if it ran inside Update (see the ban in messages.go).

// queued is one entry of the run queue. The SCRIPT is kept, but the name is
// what the dequeue resolves against: a sync while the entry waits replaces
// every scripts.Script value in the model (Start-TuiRunFlow, inventory §4.11).
type queued struct {
	Name string
	Args []string
}

// ---------------------------------------------------------------------------
// Starting
// ---------------------------------------------------------------------------

// start is Start-TuiRunFlow: run it, or queue it behind the live one. The
// dependency check that opens the deps modal is phase 11 — it needs the
// overlay — so phase 10 goes straight to the run.
func (r *runModel) start(m *Model, s scripts.Script, args ...string) tea.Cmd {
	// a sync owns the output pane exactly as a run does, so a run asked for
	// mid-sync queues rather than interleaving into the same buffer
	if r.handle != nil || r.syncing {
		r.queue = append(r.queue, queued{Name: s.Name, Args: args})
		pos := len(r.queue)
		return func() tea.Msg { return RunQueuedMsg{Name: s.Name, Position: pos} }
	}
	return r.launch(m, s, args)
}

// launch starts the run off the update loop and hands the handle back in a
// message. The command closes over the facade and the spec only — never over
// the model — which is what keeps the update loop single-threaded.
func (r *runModel) launch(m *Model, s scripts.Script, args []string) tea.Cmd {
	a, now := m.app, m.now()
	eta := etaSeconds(a, s.Name)
	return func() tea.Msg {
		h, err := a.Runner.Start(context.Background(), runner.Spec{
			Script:    s,
			Trigger:   "manual",
			ExtraArgs: args,
			Timeout:   timeoutFor(a, s),
		})
		if err != nil {
			return ErrMsg{Context: "starting " + s.Name, Err: err}
		}
		return RunStartedMsg{Script: s, Handle: h, StartedAt: now, EtaSec: eta}
	}
}

// timeoutFor resolves the run timeout the way every other caller must:
// script.json when it is set and positive, else the config default. The runner
// deliberately does not guess (its P5 contract).
func timeoutFor(a *app.App, s scripts.Script) time.Duration {
	minutes := float64(a.Cfg.RunTimeoutMinutes)
	if s.TimeoutMinutes != nil && *s.TimeoutMinutes > 0 {
		minutes = *s.TimeoutMinutes
	}
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes * float64(time.Minute))
}

// etaSeconds is inventory §4.12: the upper median of up to the last 200
// successful durations for this script, computed once at start and never
// re-derived mid-render. No successful history means no ETA bar rather than a
// guess.
func etaSeconds(a *app.App, name string) float64 {
	rows, err := a.Hist.Last(200)
	if err != nil {
		return 0
	}
	var durs []float64
	for _, row := range rows {
		if row.Script == name && row.Status == "success" && row.DurationSec != nil {
			durs = append(durs, *row.DurationSec)
		}
	}
	if len(durs) == 0 {
		return 0
	}
	slices.Sort(durs)
	return durs[len(durs)/2]
}

// ---------------------------------------------------------------------------
// Draining
// ---------------------------------------------------------------------------

// drain is bridge idiom 1 over the runner's event channel.
func drainRun(h *runner.Handle) tea.Cmd {
	return DrainCmd(h.Events, func(batch []runner.Event, closed bool) tea.Msg {
		return RunEventsMsg{Batch: batch, Closed: closed}
	})
}

// onRunQueued is the queued announcement's only job: say so, with the position,
// and name the key that undoes it.
func (r *runModel) onRunQueued(msg RunQueuedMsg) tea.Cmd {
	return status(StatusInfo, "queued "+msg.Name+" (position "+
		strconv.Itoa(msg.Position)+") — X clears the queue")
}

// onRunStarted records the handle, opens the output pane on this run and
// starts draining it.
func (r *runModel) onRunStarted(m *Model, msg RunStartedMsg) tea.Cmd {
	r.handle = msg.Handle
	r.startedAt = msg.StartedAt
	r.etaSec = msg.EtaSec
	r.doneRow = nil

	r.out.begin("run: " + msg.Script.Name)
	r.out.append("", banner("▶ "+msg.Script.Name+" · started "+
		msg.StartedAt.Format("15:04:05"), r.out.contentWidth()))
	r.selectByName(m, msg.Script.Name)
	return tea.Batch(drainRun(msg.Handle), m.kickSpinner())
}

// onRunEvents appends one drained batch and re-issues the drain until the
// channel closes. Output lines arrive already redacted — the runner's single
// chokepoint saw them first, and nothing here re-reads the raw stream.
func (r *runModel) onRunEvents(m *Model, msg RunEventsMsg) tea.Cmd {
	lines := make([]string, 0, len(msg.Batch))
	for _, ev := range msg.Batch {
		switch ev.Kind {
		case runner.EvLine:
			lines = append(lines, ev.Line)
		case runner.EvSample:
			r.lastSample = ev.Sample
		case runner.EvDone:
			r.doneRow = ev.Result
		}
	}
	if len(lines) > 0 {
		r.out.append(lines...)
	}
	if !msg.Closed {
		return drainRun(r.handle)
	}
	row := r.doneRow
	return func() tea.Msg { return RunDoneMsg{Row: row} }
}

// onRunDone closes the run out: the summary banner, a status line, and a fleet
// refresh so the list badge and the history the next ETA reads are current.
// Then the queue gets its turn.
func (r *runModel) onRunDone(m *Model, msg RunDoneMsg) tea.Cmd {
	r.handle = nil
	r.doneRow = nil
	row := msg.Row
	if row == nil {
		return tea.Batch(m.loadFleet(), status(StatusWarn, "the run ended without reporting a result"))
	}

	dur := 0.0
	if row.DurationSec != nil {
		dur = *row.DurationSec
	}
	exit := "?"
	if row.ExitCode != nil {
		exit = strconv.Itoa(*row.ExitCode)
	}
	w := r.out.contentWidth()
	r.out.append(banner(statusGlyph(row.Status)+" "+row.Script+" · "+row.Status+
		" · exit "+exit+" · "+format.Duration(dur), w))
	if res := row.Resources; res != nil {
		r.out.append("   cpu avg " + trim1(res.CPUAvgPercent) + "% / peak " + trim1(res.CPUMaxPercent) +
			"%   mem avg " + trim1(res.MemAvgMb) + "MB / peak " + trim1(res.MemMaxMb) + "MB")
	}
	if row.LogFile != nil && *row.LogFile != "" {
		r.out.append("   log: " + *row.LogFile)
	}

	kind := StatusOK
	if row.Status != "success" {
		kind = StatusErr
	}
	return tea.Batch(
		m.loadFleet(),
		m.scanLocks(),
		status(kind, row.Script+": "+row.Status),
		r.dequeue(m),
	)
}

func statusGlyph(s string) string {
	switch s {
	case "success":
		return "✓"
	case "failure":
		return "✗"
	case "killed":
		return "⊘"
	case "timeout":
		return "◷"
	case "skipped":
		return "◇"
	}
	return "·"
}

func trim1(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

// queueUnblocked is the modal gate from inventory §4.11: the PS app drains the
// queue only while no MODAL overlay is open (deps/confirm/input/env), and
// keeps draining under the read-only ones (history/help).
//
// Phase 10 has no overlays at all, so this is true for every mode that exists —
// deliberately written as the gate rather than omitted, so phase 11 adds its
// modes to one list instead of rediscovering the rule. It must never return
// false for a mode that cannot block, or the queue dead-locks.
func (m *Model) queueUnblocked() bool {
	switch m.mode {
	case modeFleet, modeRun, modeHistory, modeSchedules:
		return true
	}
	return false
}

// dequeue takes ONE entry per turn, re-resolving it by name against the
// current script list: a sync while the entry waited replaces every
// scripts.Script value, and an entry whose script is gone is reported, not
// silently dropped.
func (r *runModel) dequeue(m *Model) tea.Cmd {
	if r.handle != nil || r.syncing || len(r.queue) == 0 || !m.queueUnblocked() {
		return nil
	}
	next := r.queue[0]
	r.queue = r.queue[1:]
	for i := range m.scripts {
		if m.scripts[i].Name == next.Name {
			return r.launch(m, m.scripts[i], next.Args)
		}
	}
	return status(StatusWarn, "queued script '"+next.Name+"' no longer exists — skipped")
}

func (r *runModel) clearQueue() tea.Cmd {
	if len(r.queue) == 0 {
		return status(StatusInfo, "the queue is already empty")
	}
	n := len(r.queue)
	r.queue = nil
	return status(StatusInfo, "cleared "+strconv.Itoa(n)+" queued run(s)")
}

// ---------------------------------------------------------------------------
// Killing
// ---------------------------------------------------------------------------

// kill wraps Handle.Kill in a command. Kill blocks for up to the 3s kill grace
// (SIGTERM, wait, SIGKILL to the group and to every sampled pid), which is
// exactly why it may not run inside Update.
func (r *runModel) kill(_ *Model) tea.Cmd {
	h := r.handle
	if h == nil {
		return status(StatusWarn, "nothing is running")
	}
	return func() tea.Msg {
		h.Kill("killed")
		return StatusMsg{Text: "killed " + h.Name, Kind: StatusWarn}
	}
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

// taskEvent is one line of a background task's output, plus the terminal event
// carrying its exit status.
type taskEvent struct {
	Line string
	Done bool
	OK   bool
}

// sync streams a repo sync into the output pane through the same batched drain
// the runner uses. The producer goroutine only ever writes to its channel —
// it never touches the model and never calls Program.Send.
func (r *runModel) sync(m *Model) tea.Cmd {
	if r.handle != nil {
		return status(StatusWarn, "something is already running — x to kill it first")
	}
	if r.syncing {
		return status(StatusWarn, "a sync is already running")
	}
	r.syncing = true
	r.out.begin("sync")
	r.out.append("", banner("▶ sync scripts repos", r.out.contentWidth()))

	a := m.app
	ch := make(chan taskEvent, 256)
	go func() {
		defer close(ch)
		ok := scripts.Sync(a.Cfg, a.Paths, a.Sec, func(line string) {
			ch <- taskEvent{Line: line}
		})
		ch <- taskEvent{Done: true, OK: ok}
	}()
	r.syncCh = ch
	return tea.Batch(drainSync(ch), m.kickSpinner())
}

func drainSync(ch <-chan taskEvent) tea.Cmd {
	return DrainCmd(ch, func(batch []taskEvent, closed bool) tea.Msg {
		msg := SyncEventsMsg{Closed: closed}
		for _, e := range batch {
			if e.Done {
				msg.Finished, msg.OK = true, e.OK
				continue
			}
			msg.Batch = append(msg.Batch, e.Line)
		}
		return msg
	})
}

func (r *runModel) onSyncEvents(m *Model, msg SyncEventsMsg) tea.Cmd {
	if len(msg.Batch) > 0 {
		r.out.append(msg.Batch...)
	}
	if msg.Finished {
		r.syncOK = msg.OK
	}
	if !msg.Closed {
		return drainSync(r.syncCh)
	}
	r.syncCh = nil
	r.syncing = false
	w := r.out.contentWidth()
	if r.syncOK {
		r.out.append(banner("✓ sync · done", w))
		return tea.Batch(m.loadFleet(), status(StatusOK, "scripts synced"), r.dequeue(m))
	}
	r.out.append(banner("✗ sync · failed", w))
	return tea.Batch(m.loadFleet(), status(StatusErr, "sync failed — see the output pane"), r.dequeue(m))
}
