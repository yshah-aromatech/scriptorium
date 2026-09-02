package tui

import (
	"context"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/secret"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// Background tasks: everything that streams into the output pane but is not a
// script run — the repo sync, a lint, a dependency install, the system update
// (PS: Start-TuiTask). One shape for all of them, so they all get the same
// banner, the same batched drain, and the same kill key.
//
// The producer goroutine only ever writes to its own channel; it never touches
// the model and never calls Program.Send (messages.go's ban). Cancellation is a
// context, which is what makes `x` able to stop a slow clone.

// taskEvent is one line of a task's output, plus the terminal event carrying
// its exit status.
type taskEvent struct {
	Line string
	Done bool
	OK   bool
}

// task is one live background job.
type task struct {
	name   string
	ch     chan taskEvent
	cancel context.CancelFunc
	ok     bool
	killed bool

	// after runs when the task finishes, with whether it succeeded. It is
	// where install-then-run lives, so the run is a command issued by the
	// update loop rather than something a goroutine started.
	after func(m *Model, ok bool) tea.Cmd
}

// startTask begins one. run is executed on its own goroutine and must respect
// ctx; emit is safe to call from it (it is a channel send).
func (r *runModel) startTask(m *Model, name string,
	run func(ctx context.Context, emit func(string)) bool,
	after func(m *Model, ok bool) tea.Cmd) tea.Cmd {

	if r.handle != nil {
		return status(StatusWarn, "something is already running — x to kill it first")
	}
	if r.task != nil {
		return status(StatusWarn, r.task.name+" is already running — x to kill it first")
	}

	r.out.begin(name)
	r.out.append("", banner("▶ "+name, r.out.contentWidth()))

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan taskEvent, 256)
	go func() {
		defer close(ch)
		ok := run(ctx, func(line string) { ch <- taskEvent{Line: line} })
		ch <- taskEvent{Done: true, OK: ok}
	}()

	r.task = &task{name: name, ch: ch, cancel: cancel, after: after}
	return tea.Batch(drainTask(name, ch), m.kickSpinner())
}

func drainTask(name string, ch <-chan taskEvent) tea.Cmd {
	return DrainCmd(ch, func(batch []taskEvent, closed bool) tea.Msg {
		msg := TaskEventsMsg{Name: name, Closed: closed}
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

// onTaskEvents appends one drained batch and re-issues the drain until the
// channel closes. A cancelled task still drains to the end: the producer is
// already writing into a buffered channel, and abandoning it there is how a
// goroutine leaks.
func (r *runModel) onTaskEvents(m *Model, msg TaskEventsMsg) tea.Cmd {
	t := r.task
	if t == nil {
		return nil
	}
	if len(msg.Batch) > 0 {
		r.out.append(msg.Batch...)
	}
	if msg.Finished {
		t.ok = msg.OK
	}
	if !msg.Closed {
		return drainTask(t.name, t.ch)
	}

	r.task = nil
	t.cancel() // release the context even on the success path
	w := r.out.contentWidth()
	switch {
	case t.killed:
		r.out.append(banner("⊘ "+t.name+" · killed", w))
	case t.ok:
		r.out.append(banner("✓ "+t.name+" · done", w))
	default:
		r.out.append(banner("✗ "+t.name+" · failed", w))
	}

	cmds := []tea.Cmd{m.scanLocks()}
	if t.after != nil {
		cmds = append(cmds, t.after(m, t.ok && !t.killed))
	}
	// the queue waited for this task exactly as it waits for a run
	return tea.Batch(append(cmds, r.dequeue(m))...)
}

// killTask cancels the live task. The child dies with its context; the drain
// runs on to the channel's close, which is what posts the killed banner.
func (r *runModel) killTask() tea.Cmd {
	t := r.task
	if t == nil {
		return nil
	}
	t.killed = true
	t.cancel()
	return status(StatusWarn, "killed "+t.name)
}

// ---------------------------------------------------------------------------

// pwshTask is the common case: a pwsh -Command script whose output streams
// into the pane. Every dependency install, lint and upgrade command in the app
// is generated as one of these strings (internal/deps), exactly as the PS app
// generates them.
func (r *runModel) pwshTask(m *Model, name, script string, after func(*Model, bool) tea.Cmd) tea.Cmd {
	a := m.app
	return r.startTask(m, name, func(ctx context.Context, emit func(string)) bool {
		return streamCmd(ctx, a.Sec, emit, a.Cfg.PwshBin,
			"-NoProfile", "-NonInteractive", "-Command", script)
	}, after)
}

// streamCmd runs a child process and emits its output line by line, REDACTED —
// the pane holds only redacted text, which is what makes copying from it safe
// (the same rule the runner's own sink follows).
func streamCmd(ctx context.Context, reg *secret.Registry, emit func(string), name string, args ...string) bool {
	c := exec.CommandContext(ctx, name, args...)
	// one LineWriter per stream: they are documented as not safe for
	// concurrent use, and exec copies stdout and stderr on separate
	// goroutines. emit is a channel send, so sharing THAT is fine.
	out := reg.LineWriter(sink(emit))
	errs := reg.LineWriter(sink(emit))
	c.Stdout, c.Stderr = out, errs

	err := c.Run()
	_ = out.Close()
	_ = errs.Close()
	if err != nil && ctx.Err() == nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			// the binary is missing or unrunnable: say so in the pane rather
			// than leaving it looking like a silent success
			emit(name + ": " + err.Error())
		}
	}
	return err == nil
}

// sink turns each redacted line into an emit call.
type sink func(string)

func (s sink) Write(p []byte) (int, error) {
	s(textkit.Clean(strings.TrimSuffix(string(p), "\n")))
	return len(p), nil
}
