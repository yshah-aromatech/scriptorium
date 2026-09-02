package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// The marquee (inventory §1.12): a selected name too long for its column
// scrolls, after a one-second pause, at ~6 characters a second, looping through
// a separator. Only the selected row moves — a column of sliding text is a
// column nobody can read.
//
// The step is taken off the CLOCK, not off the frame counter, so the speed does
// not change with the redraw cadence (the PS app's own rule) and an injected
// clock makes it exactly reproducible in a test.
const (
	marqueePause = time.Second
	marqueeStep  = 165 * time.Millisecond
	marqueeLoop  = "   ·   " // what separates the end of the name from its head
)

// MarqueeTickMsg is the marquee's own beat. It is scheduled only while a
// marquee is actually running, so an idle list costs nothing (§12.10's budget
// rule).
type MarqueeTickMsg time.Time

func marqueeTickCmd() tea.Cmd {
	return tea.Tick(marqueeStep, func(t time.Time) tea.Msg { return MarqueeTickMsg(t) })
}

// marqueeName is the visible slice of a name at the current instant: the name
// itself until the pause is over, then the loop rotated one step per
// marqueeStep. Rotation is by RUNE — rotating by byte would slice a multi-byte
// character in half and shear the row.
func (r *runModel) marqueeName(m *Model, name string, w int) string {
	if textkit.Width(name) <= w {
		return name
	}
	elapsed := m.now().Sub(r.marqueeAt) - marqueePause
	if elapsed <= 0 {
		return name
	}
	loop := []rune(name + marqueeLoop)
	off := int(elapsed/marqueeStep) % len(loop)
	return string(append(append([]rune{}, loop[off:]...), loop...))
}

// noteSelection restarts the marquee whenever the selection moves, however it
// moved — a key, the mouse, or a sync reshuffling the list underneath it. Called
// from the Run view's render, which is the one place that sees every cause.
func (r *runModel) noteSelection(m *Model) {
	if idx := r.list.Index(); idx != r.marqueeSel {
		r.marqueeSel, r.marqueeAt = idx, m.now()
	}
}

// marqueeRunning reports whether the selected row's name actually overflows its
// column — the only condition under which the tick is worth scheduling.
func (r *runModel) marqueeRunning(m *Model) bool {
	if m.mode != modeRun {
		return false
	}
	it, ok := r.list.SelectedItem().(scriptItem)
	if !ok {
		return false
	}
	return textkit.Width(it.s.Name) > nameColWidth(r.list.Width())
}

// kickMarquee starts the beat if something is scrolling and it is not already
// running.
func (r *runModel) kickMarquee(m *Model) tea.Cmd {
	if r.marqueeOn || !r.marqueeRunning(m) {
		return nil
	}
	r.marqueeOn = true
	return marqueeTickCmd()
}

// onMarqueeTick keeps the beat going only while there is something to animate.
func (r *runModel) onMarqueeTick(m *Model) tea.Cmd {
	if !r.marqueeRunning(m) {
		r.marqueeOn = false
		return nil
	}
	return marqueeTickCmd()
}
