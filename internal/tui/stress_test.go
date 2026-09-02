package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// stressLines is the flood the output pane has to survive. The PS TUI's
// scrollback cap is config.maxOutputLines (default 5000), so this is an order
// of magnitude past it.
const stressLines = 50000

// THE OTHER GATE: a script that dumps 50k lines as fast as it can.
//
// Three things have to be true at once. Nothing is lost — the log file on disk
// holds every line the script wrote, and the retained tail of the UI buffer
// matches it exactly. Memory is bounded — the buffer keeps the configured
// scrollback and no more, with the PS hysteresis. And frames keep coming — the
// batched drain has to turn the flood into many modest Updates, not one giant
// one, and each of those frames has to render in human time.
func TestChattyRunStaysBoundedAndKeepsRendering(t *testing.T) {
	a := seedRunnableApp(t, map[string]string{
		"firehose": fmt.Sprintf("awk 'BEGIN{for(i=0;i<%d;i++) print \"line \" i}'\n", stressLines),
	})
	m := New(a, time.Now)
	m.useTheme(theme.New(theme.Default, colorprofile.Ascii))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(m.loadFleet()())
	m.mode = modeRun

	cap := a.Cfg.MaxOutputLines
	if cap != 5000 {
		t.Fatalf("the fixture should carry the default scrollback, got %d", cap)
	}

	h, err := a.Runner.Start(context.Background(), runner.Spec{Script: m.scripts[0], Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(RunStartedMsg{Script: m.scripts[0], Handle: h, StartedAt: time.Now()})

	var (
		batches      int
		frames       int
		slowestFrame time.Duration
		peakLines    int
		tail         []string
	)
	deadline := time.Now().Add(120 * time.Second)
	cmd := drainRun(h)
	for cmd != nil && time.Now().Before(deadline) {
		msg, ok := cmd().(RunEventsMsg)
		if !ok {
			t.Fatalf("the drain produced %T", msg)
		}
		batches++
		_, next := m.Update(msg)
		peakLines = max(peakLines, len(m.run.out.buf.Lines))

		start := time.Now()
		_ = m.frame()
		frames++
		slowestFrame = max(slowestFrame, time.Since(start))

		if msg.Closed {
			// snapshot before the completion banners land on top of the tail
			tail = append([]string(nil), m.run.out.buf.Lines...)
			m.Update(next())
			break
		}
		cmd = next
	}
	if m.run.handle != nil {
		t.Fatal("the flood never finished")
	}

	// --- nothing lost -------------------------------------------------------
	rows, err := a.Hist.Last(5)
	if err != nil || len(rows) == 0 {
		t.Fatalf("no history row: %v %+v", err, rows)
	}
	row := rows[len(rows)-1]
	if row.Status != "success" {
		t.Errorf("the flood did not succeed: %+v", row)
	}
	if row.LogFile == nil {
		t.Fatal("no log file recorded")
	}
	logLines := readLog(t, a.Paths.LogsDir, "firehose")
	if len(logLines) != stressLines {
		t.Errorf("the log has %d lines, want %d — the runner lost output", len(logLines), stressLines)
	}
	if got := logLines[len(logLines)-1]; got != fmt.Sprintf("line %d", stressLines-1) {
		t.Errorf("the log's last line is %q", got)
	}

	// The UI kept the TAIL of exactly that stream, in order, byte for byte.
	// The retained count sits between the cap and the hysteresis mark, never
	// below: PS only trims once the buffer passes Max*1.1, because trimming at
	// the cap would re-wrap the whole buffer on every appended line.
	if len(tail) < cap || len(tail) > cap*11/10 {
		t.Fatalf("the buffer retained %d lines, want between the %d cap and the %d mark",
			len(tail), cap, cap*11/10)
	}
	want := logLines[len(logLines)-len(tail):]
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("retained line %d = %q, want %q — the UI dropped or reordered output",
				i, tail[i], want[i])
		}
	}

	// --- bounded ------------------------------------------------------------
	// PS trims with hysteresis at Max*1.1, so the peak is that, never 50k.
	if peakLines > cap*11/10 {
		t.Errorf("the buffer grew to %d lines, past the %d hysteresis mark", peakLines, cap*11/10)
	}

	// --- still rendering ----------------------------------------------------
	if batches < 20 {
		t.Errorf("50k lines arrived in %d batches — the drain is not bounding its batch size", batches)
	}
	if frames != batches {
		t.Errorf("%d batches produced %d frames", batches, frames)
	}
	if slowestFrame > 250*time.Millisecond {
		t.Errorf("the slowest frame during the flood took %v", slowestFrame)
	}
	t.Logf("50k lines: %d batches, %d frames, slowest frame %v, peak buffer %d lines",
		batches, frames, slowestFrame, peakLines)
}

// The drain itself: block for the first value, take what is already queued up
// to the cap, and report the close exactly once.
func TestDrainCmd(t *testing.T) {
	ch := make(chan int, 4)
	ch <- 1
	ch <- 2
	ch <- 3
	msg := DrainCmd(ch, func(b []int, closed bool) tea.Msg {
		return struct {
			B      []int
			Closed bool
		}{b, closed}
	})().(struct {
		B      []int
		Closed bool
	})
	if len(msg.B) != 3 || msg.Closed {
		t.Errorf("drained %v closed=%v, want three values and an open channel", msg.B, msg.Closed)
	}

	// a batch is capped even when far more is queued
	big := make(chan int, drainMax*3)
	for i := range drainMax * 3 {
		big <- i
	}
	got := DrainCmd(big, func(b []int, closed bool) tea.Msg { return len(b) })()
	if got != drainMax {
		t.Errorf("batch = %v, want the %d cap", got, drainMax)
	}

	// close before any value: nil batch, closed
	empty := make(chan int)
	close(empty)
	type res struct {
		B      []int
		Closed bool
	}
	r := DrainCmd(empty, func(b []int, closed bool) tea.Msg { return res{b, closed} })().(res)
	if r.B != nil || !r.Closed {
		t.Errorf("a closed empty channel gave %+v", r)
	}

	// the final batch carries the close
	last := make(chan int, 2)
	last <- 7
	close(last)
	r2 := DrainCmd(last, func(b []int, closed bool) tea.Msg { return res{b, closed} })().(res)
	if len(r2.B) != 1 || !r2.Closed {
		t.Errorf("the last batch gave %+v, want one value and closed", r2)
	}
}

// The buffer's own bound, without a process: appending far past the cap keeps
// exactly the tail and rewraps once, not per line.
func TestOutputPaneRetention(t *testing.T) {
	var o outputPane
	o.reset("test", 200)
	o.resize(80, 20)
	for i := range 5000 {
		o.append(fmt.Sprintf("line %d", i))
	}
	if n := len(o.buf.Lines); n < 200 || n > 220 {
		t.Errorf("retained %d lines, want between the 200 cap and the 220 hysteresis mark", n)
	}
	if got, want := o.buf.Lines[len(o.buf.Lines)-1], "line 4999"; got != want {
		t.Errorf("last retained line = %q, want %q", got, want)
	}
	if !o.follow || o.scroll != o.maxScroll() {
		t.Error("the pane stopped following its own tail")
	}
	// and the wrap cache is consistent with what survived
	if got := o.buf.Rejoin(0, 0, len(o.buf.Wrapped)-1, 1<<30); !strings.HasSuffix(got, "line 4999") {
		t.Errorf("the wrap cache does not end where the buffer does: %q", got[max(len(got)-40, 0):])
	}
}
