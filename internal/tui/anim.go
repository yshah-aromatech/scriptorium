package tui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// The 60 fps animation engine (v1.1.0 task 2, ruling 2). ONE self-rescheduling
// 16 ms clock drives every animation — spinner, marquee, status fade, ETA
// motion, activity pulse — and it is armed only while at least one of them is
// live. Idle means ZERO ticks: no run, no scrolling name, no fading status —
// no timer. The two standalone beats this replaces (the 165 ms marquee tick
// and the 100 ms fade tick) are dead; their steppers now evaluate
// frame-accurately from the injected clock on this one beat, so a boundary is
// crossed on the exact frame it falls in and a scrubbed-env test replays every
// step bit-for-bit.
const (
	framePeriod = 16 * time.Millisecond // ~60 fps

	// spinnerStep is the braille spinner's cadence: a new glyph every 80 ms,
	// stepped on the frame clock at exact boundaries.
	spinnerStep = 80 * time.Millisecond

	// pulsePeriod is the activity pulse (lazygit): while anything runs, the
	// live-now title breathes Pulse↔Muted once every ~2 s, low amplitude.
	pulsePeriod = 2 * time.Second

	// etaEase is the window over which the ETA bar glides toward its target
	// after a jump (run start, a fresh ETA) instead of snapping.
	etaEase = 300 * time.Millisecond
)

// FrameMsg is the animation clock's beat.
type FrameMsg time.Time

func frameCmd() tea.Cmd {
	return tea.Tick(framePeriod, func(t time.Time) tea.Msg { return FrameMsg(t) })
}

// animLive reports whether anything on screen is actually moving — the sole
// condition under which the clock is worth a tick (§12.10's budget rule).
func (m *Model) animLive() bool {
	return len(m.live) > 0 || m.run.active() || m.run.marqueeRunning(m) || m.fadeLive()
}

// fadeLive is true while a status message is inside (or within a second of)
// its dissolve window — the same near-due arming rule the old 100 ms fade
// tick used, so a message never burns frames during its five quiet seconds.
func (m *Model) fadeLive() bool {
	if m.statusText == "" {
		return false
	}
	age := m.now().Sub(m.statusAt)
	return age >= statusFadeAt-time.Second && age <= statusTTL
}

// kickAnim arms the clock if something just started moving. Every event that
// can wake an animation routes through here; a second kick while the clock
// runs is a no-op, so over-calling costs nothing.
func (m *Model) kickAnim() tea.Cmd {
	if m.animOn || !m.animLive() {
		return nil
	}
	m.animOn = true
	return frameCmd()
}

// onFrame is one beat: retire an expired status message, then keep ticking
// only while something still animates. All stepper STATE is computed from the
// clock at render time — the beat's only job is to cause the next frame.
func (m *Model) onFrame() tea.Cmd {
	if m.statusText != "" && m.now().Sub(m.statusAt) >= statusTTL {
		m.statusText = ""
	}
	if !m.animLive() {
		m.animOn = false
		return nil
	}
	return frameCmd()
}

// ---------------------------------------------------------------------------
// Steppers — pure functions of the clock
// ---------------------------------------------------------------------------

// spinnerFrames is the braille spinner, stepped at spinnerStep boundaries.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}

func spinnerGlyph(t time.Time) string {
	return spinnerFrames[int(t.UnixMilli()/spinnerStep.Milliseconds())%len(spinnerFrames)]
}

// pulseAmount is where in the breath the pulse is at instant t: a cosine over
// pulsePeriod, swinging 0.15–0.85 of the way from Pulse toward Muted — low
// amplitude on purpose, a breath rather than a blink.
func pulseAmount(t time.Time) float64 {
	period := float64(pulsePeriod.Milliseconds())
	phase := float64(t.UnixMilli()%pulsePeriod.Milliseconds()) / period
	return 0.5 - 0.35*math.Cos(2*math.Pi*phase)
}

// pulseTitleStyle is the live-now card's breathing title: nil (the default
// panel voice) while nothing runs. theme.Mix carries the profile guard —
// continuous only in truecolor, stepped below it.
func (m *Model) pulseTitleStyle() *lipgloss.Style {
	if len(m.live) == 0 && !m.run.active() {
		return nil
	}
	st := m.th.Mix(m.th.C.Pulse, m.th.C.Muted, pulseAmount(m.now())).Bold(true)
	return &st
}

// leftEighths is the sub-cell alphabet of the ETA bar: a cell fills in eighth
// steps (btop-smooth) instead of jumping a whole cell at a time.
var leftEighths = []rune("▏▎▍▌▋▊▉█")

// etaBar renders frac (0..1) across width cells with eighth-cell resolution:
// full blocks, then one partial eighth-block, then the empty track. This is
// the ~30 lines that replace bubbles/progress (task 2's measured call): the
// component's gradient half-blocks painted backgrounds across the status line
// and emitted truecolor below the profile; this does neither, on any profile
// (under no-colour every style here is a no-op and the glyphs stand alone).
func etaBar(th theme.Theme, frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	frac = min(max(frac, 0), 1)
	eighths := int(math.Round(frac * float64(width) * 8))
	full := eighths / 8
	rem := eighths % 8

	var b strings.Builder
	if full > 0 {
		b.WriteString(th.S.Primary.Render(strings.Repeat("█", full)))
	}
	empty := width - full
	if rem > 0 {
		b.WriteString(th.S.Primary.Render(string(leftEighths[rem-1])))
		empty--
	}
	if empty > 0 {
		b.WriteString(th.S.Muted.Render(strings.Repeat("░", empty)))
	}
	return b.String()
}

func easeOutCubic(x float64) float64 {
	x = min(max(x, 0), 1)
	inv := 1 - x
	return 1 - inv*inv*inv
}

// etaFrac is the ETA bar's displayed fraction at instant now: the true
// progress, eased from wherever the bar stood when the target last jumped
// (run start). Pure clock arithmetic — no per-frame mutable state, so a
// replayed clock reproduces every sub-cell position exactly.
func (r *runModel) etaFrac(now time.Time) float64 {
	if r.etaSec <= 0 {
		return 0
	}
	target := min(now.Sub(r.startedAt).Seconds()/r.etaSec, 1)
	dt := now.Sub(r.etaAnchor)
	if dt >= etaEase {
		return target
	}
	return r.etaFrom + (target-r.etaFrom)*easeOutCubic(float64(dt)/float64(etaEase))
}
