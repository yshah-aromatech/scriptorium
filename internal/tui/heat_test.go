package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/procstat"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// v1.0.1 task 2 — heat discipline. The released sparklines ran an 8-stop
// green→yellow→red ramp on every cell of every row (66 color switches on one
// line); color has to go back to meaning exceptional. A sparkline is now
// single-hue Info, and only the cells at ≥80% of the series' own peak carry
// the heat color (Warning).

const (
	infoSGR    = "38;2;33;199;168"  // Night Owl Info #21c7a8
	warningSGR = "38;2;255;235;149" // Night Owl Warning #ffeb95
)

func TestSparklineIsSingleHueWithHeatOnlyAtThePeak(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.TrueColor)
	// peak 100; 85 is the only other cell at >=80% of it
	got := sparkline(th, []float64{10, 20, 100, 30, 85, 40}, 6, nil)

	if n := strings.Count(got, warningSGR); n != 2 {
		t.Errorf("heat on %d cells, want exactly the two >=80%% of peak:\n%q", n, got)
	}
	if n := strings.Count(got, infoSGR); n != 4 {
		t.Errorf("%d Info cells, want 4:\n%q", n, got)
	}
	// single hue + heat means exactly two foreground colors, full stop
	fgRE := regexp.MustCompile(`38;2;\d+;\d+;\d+`)
	distinct := map[string]bool{}
	for _, m := range fgRE.FindAllString(got, -1) {
		distinct[m] = true
	}
	if len(distinct) != 2 {
		t.Errorf("sparkline uses %d distinct colors %v, want Info + heat only", len(distinct), distinct)
	}
}

// A flat series is its own peak everywhere — every cell is "exceptional", so
// every cell heats. Degenerate but correct: the discipline is relative to the
// series, and a flat line at 100%% CPU deserves to glow.
func TestSparklineFlatSeriesHeatsEverywhere(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.TrueColor)
	got := sparkline(th, []float64{50, 50, 50}, 3, nil)
	if strings.Contains(got, infoSGR) || strings.Count(got, warningSGR) != 3 {
		t.Errorf("flat series should be all heat:\n%q", got)
	}
}

// An all-zero series draws flat baseline blocks in Info — nothing is
// exceptional about zero.
func TestSparklineZeroSeriesStaysCool(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.TrueColor)
	got := sparkline(th, []float64{0, 0, 0}, 3, nil)
	if strings.Contains(got, warningSGR) {
		t.Errorf("a zero series has no peak to heat:\n%q", got)
	}
}

// The background budget, stated as a test: a live-run frame may paint exactly
// four backgrounds beyond nothing at all — the window ground, the selection
// band, the card tint and the brand chip. (The ETA bar's fill would be the
// allowed fifth, but it renders as foreground-colored blocks.) The released
// TUI painted a teal gradient background across the ETA bar; this pins its
// death.
func TestRunLiveFrameStaysInsideTheBackgroundBudget(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.mode = modeRun
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.run.onRunStarted(m, RunStartedMsg{
		Script:    m.scripts[0],
		Handle:    fakeHandle("backup-db"),
		StartedAt: frozen.Add(-18 * time.Second),
		EtaSec:    42.5,
	})
	m.run.lastSample = procstat.Sample{CPU: 61.5, MemMB: 58.2}
	m.run.queue = []queued{{Name: "heartbeat"}}

	allowed := map[string]bool{
		"48;2;1;22;39":     true, // Bg — the ground itself
		"48;2;9;59;94":     true, // SelBg
		"48;2;12;32;49":    true, // CardBg
		"48;2;199;146;234": true, // Accent — the brand chip
	}
	bgRE := regexp.MustCompile(`48;2;\d+;\d+;\d+`)
	seen := map[string]bool{}
	for _, b := range bgRE.FindAllString(m.frame(), -1) {
		seen[b] = true
	}
	for b := range seen {
		if !allowed[b] {
			t.Errorf("unbudgeted background paint %s in the run-live frame", b)
		}
	}
	if !seen["48;2;1;22;39"] {
		t.Error("the ground is missing from the run-live frame")
	}
}
