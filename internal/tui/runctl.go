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
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
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

// start is Start-TuiRunFlow: queue it behind whatever is running, else check
// its dependencies and either ask about the missing ones or go straight to the
// run. The dependency scan is a command (it shells out), so a slow scan costs
// no frames.
func (r *runModel) start(m *Model, s scripts.Script, args ...string) tea.Cmd {
	// a background task owns the output pane exactly as a run does, so a run
	// asked for mid-task queues rather than interleaving into the same buffer
	if r.handle != nil || r.task != nil {
		r.queue = append(r.queue, queued{Name: s.Name, Args: args})
		pos := len(r.queue)
		return func() tea.Msg { return RunQueuedMsg{Name: s.Name, Position: pos} }
	}
	return r.scanDeps(m, s, args, false)
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
		// Two stat groups on one line where it fits, one per line where it does
		// not. Wrapping decides these later at pane width, and it breaks
		// mid-word — which on this line means splitting a number from its unit
		// ("peak" / "61.2MB"). Choosing the seam here means the reader never
		// sees that.
		cpu := "   cpu avg " + trim1(res.CPUAvgPercent) + "% / peak " + trim1(res.CPUMaxPercent) + "%"
		mem := "   mem avg " + trim1(res.MemAvgMb) + "MB / peak " + trim1(res.MemMaxMb) + "MB"
		if textkit.Width(cpu+mem) <= w {
			r.out.append(cpu + mem)
		} else {
			r.out.append(cpu, mem)
		}
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

// dequeue takes ONE entry per turn, re-resolving it by name against the
// current script list: a sync while the entry waited replaces every
// scripts.Script value, and an entry whose script is gone is reported, not
// silently dropped.
func (r *runModel) dequeue(m *Model) tea.Cmd {
	if r.handle != nil || r.task != nil || len(r.queue) == 0 || !m.queueUnblocked() {
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
// Sync
// ---------------------------------------------------------------------------

// sync streams a repo sync into the output pane as a background task, so the
// spinner, the scroll keys and `x` all keep working through a slow clone.
func (r *runModel) sync(m *Model) tea.Cmd {
	a := m.app
	return r.startTask(m, "sync scripts repos",
		func(ctx context.Context, emit func(string)) bool {
			return scripts.Sync(ctx, a.Cfg, a.Paths, a.Sec, emit)
		},
		func(m *Model, ok bool) tea.Cmd {
			if ok {
				return tea.Batch(m.loadFleet(), status(StatusOK, "scripts synced"))
			}
			return tea.Batch(m.loadFleet(), status(StatusErr, "sync failed — see the output pane"))
		})
}
