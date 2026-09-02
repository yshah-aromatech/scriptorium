package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// The message taxonomy (architecture doc, "TUI decomposition"). Every type the
// finished TUI speaks is declared here, including the ones phase 11 fills in,
// so the vocabulary is one list rather than a scatter of ad-hoc structs.
//
// ---------------------------------------------------------------------------
// The two bridge idioms — and the ban
// ---------------------------------------------------------------------------
//
// Idiom 1, batched drain: a producer goroutine owns a channel; the UI consumes
// it with [DrainCmd], which blocks for the first item, sweeps up whatever else
// is already queued (bounded), and hands the whole batch to Update as ONE
// message. Update appends the batch and re-issues the command until the batch
// arrives Closed. A 50k-line script therefore costs tens of frames, not 50k
// Update calls — and back-pressure still reaches the producer, so nothing is
// silently dropped.
//
// Idiom 2, self-rescheduling tick: periodic work is a tea.Tick whose handler
// does the work and returns the next tea.Tick. Three run here — 1 Hz for ages
// and fades, 2 s for the live-lock scan, 60 s for the missed-fire sweep.
//
// BANNED: Program.Send from a background goroutine, and any bare `go func()`
// that reaches into the model. Both re-enter the update loop from outside The
// Elm Architecture's single thread, which is how a TUI acquires races that only
// show up on someone else's terminal. Anything asynchronous is a tea.Cmd — that
// includes runner.Handle.Kill, which blocks for up to the 3 s kill grace and
// would otherwise stall rendering (see runctl.go).

// drainMax bounds one batch. Large enough that a chatty run costs a handful of
// batches per frame, small enough that one Update never walks a huge slice.
const drainMax = 512

// DrainCmd is idiom 1. It blocks for the first value on ch, then takes up to
// drainMax more already-queued values without blocking, and wraps the batch in
// one message. A closed channel yields the final batch with closed=true (a
// channel that closes before any value yields a nil batch, still closed).
//
// The caller re-issues the returned command from Update for as long as
// closed is false. Never call it from anywhere but a tea.Cmd.
func DrainCmd[T any](ch <-chan T, wrap func(batch []T, closed bool) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return wrap(nil, true)
		}
		batch := []T{first}
		for len(batch) < drainMax {
			select {
			case v, more := <-ch:
				if !more {
					return wrap(batch, true)
				}
				batch = append(batch, v)
			default:
				return wrap(batch, false)
			}
		}
		return wrap(batch, false)
	}
}

// ---------------------------------------------------------------------------
// Clock / polling
// ---------------------------------------------------------------------------

// TickMsg is the 1 Hz beat: relative ages, fade expiry, elapsed time.
type TickMsg time.Time

// LockPollMsg is the 2 s beat: rescan the lock dir for runs started by cron,
// the MCP server or another shell.
type LockPollMsg time.Time

// MissedTickMsg is the 60 s beat: run app.MissedSweep.
type MissedTickMsg time.Time

// StatusFadeMsg is the status line's dissolve frame (10 Hz, and only for the
// last seconds of a message's life).
type StatusFadeMsg time.Time

// ---------------------------------------------------------------------------
// Data loads
// ---------------------------------------------------------------------------

// ScriptsLoadedMsg carries a full refresh of the fleet: discovery, last
// statuses and the managed crontab, gathered together off the update loop.
type ScriptsLoadedMsg struct {
	Scripts   []scripts.Script
	Statuses  map[string]history.Last
	Schedules map[string]string
	Recent    []history.Row
	SyncedAt  time.Time
}

// LiveRunsMsg is the result of one lock scan.
type LiveRunsMsg []lockfile.Live

// MissedMsg is the result of one missed sweep. Err never crashes the TUI; it
// becomes a status message (controller ruling 6).
type MissedMsg struct {
	Misses []missed.Miss
	Err    error
}

// HistoryLoadedMsg carries rows for the History view (phase 11).
type HistoryLoadedMsg struct{ Rows []history.Row }

// LogLoadedMsg carries one run log read back from disk (phase 11).
type LogLoadedMsg struct {
	Path  string
	Lines []string
	Err   error
}

// CronParsedMsg carries a natural-language schedule translated to a cron
// expression, for confirmation (phase 11).
type CronParsedMsg struct {
	Script string
	Expr   string
	Err    error
}

// DepsScannedMsg carries one dependency scan's result, plus what the scan was
// FOR: a run that has to wait for the answer (Args, InstallOnly false) or a
// plain `i` check. Degraded/Warning come from the PowerShell scanner's
// no-pwsh fallback, which can report deps but never install them.
type DepsScannedMsg struct {
	Script      scripts.Script
	Missing     []deps.Dep
	Args        []string
	InstallOnly bool
	Degraded    bool
	Warning     string
	Err         error
}

// ---------------------------------------------------------------------------
// Run lifecycle
// ---------------------------------------------------------------------------

// RunStartedMsg announces a launched run: the handle to drain and kill it
// with, and the ETA derived from history at launch (never mid-render).
type RunStartedMsg struct {
	Script    scripts.Script
	Handle    *runner.Handle
	StartedAt time.Time
	EtaSec    float64
}

// RunEventsMsg is one batched drain of the runner's event channel.
type RunEventsMsg struct {
	Batch  []runner.Event
	Closed bool
}

// RunDoneMsg carries the finished run's history row (nil only if the run was
// abandoned, which needs a cancelled context and a full buffer at once).
type RunDoneMsg struct{ Row *history.Row }

// RunQueuedMsg announces that a run was queued behind the live one.
type RunQueuedMsg struct {
	Name     string
	Position int
}

// ---------------------------------------------------------------------------
// Background tasks (sync, lint, dependency install, system update)
// ---------------------------------------------------------------------------

// TaskEventsMsg is one batched drain of a background task's output — the repo
// sync, a lint, a dependency install, the system update. Finished marks the
// batch that carried the task's terminal event, and OK is only meaningful once
// it has; Closed is the channel's end, which is when the task is really over
// (a cancelled task still drains to it).
type TaskEventsMsg struct {
	Name     string
	Batch    []string
	Closed   bool
	Finished bool
	OK       bool
}

// ---------------------------------------------------------------------------
// UI feedback
// ---------------------------------------------------------------------------

// StatusKind colors the status line.
type StatusKind int

// The four status kinds, matching Set-TuiStatus's -Kind values.
const (
	StatusInfo StatusKind = iota
	StatusOK
	StatusWarn
	StatusErr
)

// StatusMsg posts a transient line to the status bar.
type StatusMsg struct {
	Text string
	Kind StatusKind
}

// ClipboardMsg reports the outcome of a copy (phase 11).
type ClipboardMsg struct {
	How   string
	Chars int
}

// ErrMsg is a failure that should be shown, not fatal. Anything genuinely
// fatal is returned from Run instead.
type ErrMsg struct {
	Context string
	Err     error
}

// status is a shorthand for the common "post a line" command.
func status(kind StatusKind, text string) tea.Cmd {
	return func() tea.Msg { return StatusMsg{Text: text, Kind: kind} }
}
