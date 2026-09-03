package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// The braille spinner steps at exact 80 ms boundaries of the clock — never
// mid-bucket, never off a frame counter. frozen's UnixMilli is divisible by
// 80, so the boundaries fall exactly on multiples of spinnerStep.
func TestSpinnerStepsAt80msBoundaries(t *testing.T) {
	if frozen.UnixMilli()%spinnerStep.Milliseconds() != 0 {
		t.Fatal("fixture drift: frozen no longer sits on a spinner boundary")
	}
	at := spinnerGlyph(frozen)
	if spinnerGlyph(frozen.Add(79*time.Millisecond)) != at {
		t.Error("the glyph changed inside an 80 ms bucket")
	}
	if spinnerGlyph(frozen.Add(80*time.Millisecond)) == at {
		t.Error("the glyph did not change at the 80 ms boundary")
	}
	// a full lap comes back around
	if spinnerGlyph(frozen.Add(8*80*time.Millisecond)) != at {
		t.Error("eight steps did not complete a lap")
	}
	for _, f := range spinnerFrames {
		if textkit.Width(f) != 1 {
			t.Errorf("spinner frame %q is not one cell", f)
		}
	}
}

// The ETA bar fills in eighth-cell steps: between two whole cells it renders
// one of ▏▎▍▌▋▊▉ — sub-cell motion, not a whole-cell jump.
func TestEtaBarSubCellResolution(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.TrueColor)
	plain := func(frac float64) string { return textkit.StripANSI(etaBar(th, frac, 12)) }

	if got := plain(0); got != strings.Repeat("░", 12) {
		t.Errorf("empty bar = %q", got)
	}
	if got := plain(1); got != strings.Repeat("█", 12) {
		t.Errorf("full bar = %q", got)
	}
	// 43/96 eighths: 5 full cells + a 3/8 partial + 6 empty
	got := plain(43.0 / 96.0)
	want := strings.Repeat("█", 5) + "▍" + strings.Repeat("░", 6)
	if got != want {
		t.Errorf("sub-cell bar = %q, want %q", got, want)
	}
	if w := textkit.Width(got); w != 12 {
		t.Errorf("bar width = %d, want 12", w)
	}
}

// The bar EASES toward its target after a jump instead of snapping: at the
// anchor it shows where it started, past etaEase it shows the true fraction.
func TestEtaBarEasesTowardTheTarget(t *testing.T) {
	r := &runModel{etaSec: 100, startedAt: frozen.Add(-50 * time.Second)}
	r.etaFrom, r.etaAnchor = 0, frozen // target jumped to 0.5 at frozen

	if got := r.etaFrac(frozen); got != 0 {
		t.Errorf("at the anchor the bar shows %v, want its starting 0", got)
	}
	mid := r.etaFrac(frozen.Add(etaEase / 2))
	if mid <= 0 || mid >= 0.5 {
		t.Errorf("mid-ease fraction = %v, want strictly between 0 and 0.5", mid)
	}
	// ease-out: the first half of the window covers MORE than half the gap
	if mid <= 0.25 {
		t.Errorf("mid-ease fraction = %v — not easing out", mid)
	}
	after := r.etaFrac(frozen.Add(etaEase))
	if want := 0.503; after < 0.5 || after > want {
		t.Errorf("post-ease fraction = %v, want the true target (~0.5)", after)
	}
}

// The activity pulse breathes on a ~2 s period at low amplitude — never fully
// Muted, never a blink — and the title style only exists while something runs.
func TestActivityPulse(t *testing.T) {
	lo, hi := 1.0, 0.0
	for ms := int64(0); ms < 2000; ms += 16 {
		a := pulseAmount(frozen.Add(time.Duration(ms) * time.Millisecond))
		lo, hi = min(lo, a), max(hi, a)
	}
	if lo < 0.1 || hi > 0.9 {
		t.Errorf("pulse swings %.2f–%.2f — amplitude should stay low", lo, hi)
	}
	if hi-lo < 0.5 {
		t.Errorf("pulse swings only %.2f–%.2f — the breath is invisible", lo, hi)
	}
	if a, b := pulseAmount(frozen), pulseAmount(frozen.Add(pulsePeriod)); a != b {
		t.Errorf("pulse is not %.0fs-periodic: %v vs %v", pulsePeriod.Seconds(), a, b)
	}

	m := newFixtureModel(t, truecolorEnv)
	if m.pulseTitleStyle() == nil {
		t.Error("no pulse style while a run is live")
	}
	m.Update(LiveRunsMsg{})
	if m.pulseTitleStyle() != nil {
		t.Error("a pulse style while nothing runs")
	}
}

// Damage is row-local: one 16 ms step of a live frame changes ONLY the rows
// that animate (here: the breathing live-now title). Bubble Tea's differ then
// repaints just those rows — a pure sub-cell change never causes a full
// repaint.
func TestAnimatedFrameDamageIsRowLocal(t *testing.T) {
	m, advance := clockedModel(t)
	m.mode = modeFleet
	m.Update(FrameMsg(frozen))

	before := strings.Split(m.frame(), "\n")
	advance(16 * time.Millisecond) // inside the spinner's 80 ms bucket
	after := strings.Split(m.frame(), "\n")

	var changed []int
	for i := range before {
		if before[i] != after[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		t.Fatal("nothing changed across a 16 ms step of a live frame")
	}
	if len(changed) > 2 {
		t.Fatalf("a 16 ms step dirtied %d rows (%v) — damage is not row-local", len(changed), changed)
	}
	for _, i := range changed {
		if !strings.Contains(textkit.StripANSI(after[i]), "live now") {
			t.Errorf("row %d changed but carries no animation: %q", i, after[i])
		}
	}
}

// The frame clock is armed by run traffic and disarms itself when the run
// ends — and an idle model schedules zero ticks from every entry point.
func TestIdleModelSchedulesZeroTicks(t *testing.T) {
	m, _ := clockedModel(t)
	m.Update(LiveRunsMsg{})
	m.Update(FrameMsg(frozen))
	if m.animOn {
		t.Fatal("the clock is armed on an idle model")
	}
	if cmd := m.kickAnim(); cmd != nil {
		t.Error("kickAnim armed the clock with nothing to animate")
	}
	if cmd := m.onFrame(); cmd != nil {
		t.Error("a stray frame beat rescheduled itself on an idle model")
	}
}

// An animated 120×40 frame builds comfortably under the 2 ms budget a 60 fps
// clock allows (task 2's perf gate; the benchmark reports the exact number).
func TestAnimatedFrameBuildUnder2ms(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation inflates frame cost; the budget is judged unraced")
	}
	m := animatedModel(t)
	for range 10 { // warm the caches the way a running session would
		_ = m.frame()
	}
	const n = 100
	start := time.Now()
	for range n {
		_ = m.frame()
	}
	per := time.Since(start) / n
	t.Logf("animated frame build: %v/frame at 120x40", per)
	if per > 2*time.Millisecond {
		t.Errorf("frame build = %v, over the 2 ms budget", per)
	}
}

// animatedModel is a 120×40 model with every animation live at once: a run
// with an ETA (spinner + bar), an external live lock (pulse), a scrolling
// selected name (marquee) and a fading status message.
func animatedModel(t testing.TB) *Model {
	t.Helper()
	m := runAt(t, 120, 40)
	withLongName(t, m)
	m.run.handle = &runner.Handle{Name: "backup-db"}
	m.run.startedAt = frozen.Add(-20 * time.Second)
	m.run.etaSec = 42.5
	m.statusText, m.statusKind = "scripts synced", StatusOK
	m.statusAt = frozen.Add(-statusFadeAt - 200*time.Millisecond)
	return m
}

func BenchmarkAnimatedFrame(b *testing.B) {
	m := animatedModel(b)
	at := frozen
	m.now = func() time.Time { return at }
	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(16 * time.Millisecond)
		_ = m.frame()
	}
}
