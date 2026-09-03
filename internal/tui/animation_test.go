package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// clockedModel is a Run-view model whose clock the test moves by hand. Every
// animation here is a function of the clock, never of a frame counter, which is
// what makes them reproducible.
func clockedModel(t *testing.T) (*Model, func(time.Duration)) {
	t.Helper()
	m := runAt(t, 120, 40)
	at := frozen
	m.now = func() time.Time { return at }
	return m, func(d time.Duration) { at = frozen.Add(d) }
}

const longName = "very-long-nightly-reconciliation-and-reporting-job"

// withLongName adds a script whose name cannot fit the list column.
func withLongName(t testing.TB, m *Model) {
	t.Helper()
	m.scripts = append(m.scripts, scripts.Script{
		Name: longName, Runtime: "powershell", Entry: "/tmp/x.ps1",
	})
	m.run.reload(m)
	m.run.selectByName(m, longName)
	if textkit.Width(longName) <= nameColWidth(m.run.list.Width()) {
		t.Fatalf("the fixture name fits the %d-cell column, so nothing would scroll",
			nameColWidth(m.run.list.Width()))
	}
}

// ---------------------------------------------------------------------------
// Marquee
// ---------------------------------------------------------------------------

// The marquee holds for a second, then steps exactly one character per 165 ms —
// frame-accurate off the injected clock, not off the redraw rate.
func TestMarqueeStepsOnTheClock(t *testing.T) {
	m, advance := clockedModel(t)
	withLongName(t, m)
	w := nameColWidth(m.run.list.Width())
	m.frame() // anchors the marquee at the current instant

	loop := []rune(longName + marqueeLoop)
	rotated := func(off int) string {
		return string(append(append([]rune{}, loop[off%len(loop):]...), loop...))
	}

	for _, c := range []struct {
		after time.Duration
		want  string
	}{
		{0, longName},                      // the pause
		{999 * time.Millisecond, longName}, // still the pause
		{marqueePause + 1*time.Millisecond, rotated(0)},
		{marqueePause + marqueeStep, rotated(1)},
		{marqueePause + 3*marqueeStep, rotated(3)},
		{marqueePause + 40*marqueeStep, rotated(40)},
	} {
		advance(c.after)
		if got := m.run.marqueeName(m, longName, w); got != c.want {
			t.Errorf("at +%v the marquee showed\n  %q\nwant\n  %q", c.after, got, c.want)
		}
	}

	// it loops rather than running off the end
	advance(marqueePause + time.Duration(len(loop))*marqueeStep)
	if got := m.run.marqueeName(m, longName, w); got != rotated(0) {
		t.Errorf("a full lap did not return to the start: %q", got)
	}
}

// The step is visible in the FRAME, on the selected row only, and the anchor
// restarts when the selection moves.
func TestMarqueeIsWiredToTheSelectedRowOnly(t *testing.T) {
	m, advance := clockedModel(t)
	withLongName(t, m)

	// the row the long name is on, as plain text ("long-nightly" survives both
	// the truncated and the scrolled renderings at the paneled list width)
	row := func() string { return textkit.StripANSI(frameRow(m, "long-nightly")) }

	if !strings.Contains(row(), "very-long-nightly") {
		t.Fatalf("the long name is not on screen unscrolled:\n%s", plainFrame(m))
	}
	advance(marqueePause + 5*marqueeStep)
	if got := row(); !strings.Contains(got, "nightly-reconcil") || strings.Contains(got, "very-long") {
		t.Errorf("the selected row did not scroll five characters: %q", got)
	}

	// move the selection away: the long row stops scrolling and truncates,
	// and the marquee's clock restarts for whatever is selected now
	m.run.selectByName(m, "backup-db")
	m.frame()
	if m.run.marqueeAt != m.now() {
		t.Error("moving the selection did not restart the marquee clock")
	}
	if got := row(); !strings.Contains(got, "very-long-nightly") {
		t.Errorf("an unselected row is still scrolling: %q", got)
	}
}

// DELIBERATE FLIP (v1.1.0 task 2): this test used to prove the marquee's own
// standalone 165 ms tick was scheduled and stopped. That tick is dead — the
// marquee now arms the SHARED 16 ms animation clock (anim.go), and this
// proves the arming: an idle list arms nothing, a scrolling name arms the
// clock exactly once, and the clock disarms itself when the scroll stops.
func TestMarqueeArmsTheSharedFrameClock(t *testing.T) {
	m, _ := clockedModel(t)
	m.Update(LiveRunsMsg{})    // drop the fixture's live run: the marquee alone decides
	m.Update(FrameMsg(frozen)) // one beat lets the clock notice and disarm
	m.run.selectByName(m, "backup-db")
	if m.run.marqueeRunning(m) {
		t.Error("a name that fits its column is reported as scrolling")
	}
	if cmd := m.kickAnim(); cmd != nil {
		t.Error("the frame clock was armed with nothing to animate")
	}

	withLongName(t, m)
	if !m.run.marqueeRunning(m) {
		t.Fatal("an overflowing selected name is not reported as scrolling")
	}
	if cmd := m.kickAnim(); cmd == nil {
		t.Fatal("the frame clock was not armed for a scrolling name")
	}
	if cmd := m.kickAnim(); cmd != nil {
		t.Error("the clock was armed a second time while already running")
	}
	// and it disarms itself once the selection moves back to a short name
	m.run.selectByName(m, "backup-db")
	if cmd := m.onFrame(); cmd != nil || m.animOn {
		t.Error("the clock kept running with nothing left to animate")
	}
}

// ---------------------------------------------------------------------------
// Status fade
// ---------------------------------------------------------------------------

// A status message holds, dissolves over the last 0.8 s, and is gone at 6 s
// (inventory §1.12 / Get-TuiStatusLine).
func TestStatusFadeWindow(t *testing.T) {
	for _, c := range []struct {
		age  time.Duration
		want float64
	}{
		{0, 0},
		{statusFadeAt, 0},
		{statusFadeAt + 400*time.Millisecond, 0.5},
		{statusTTL, 1},
		{statusTTL + time.Second, 1},
	} {
		if got := fadeAmount(c.age); got != c.want {
			t.Errorf("fadeAmount(%v) = %v, want %v", c.age, got, c.want)
		}
	}

	m, advance := clockedModel(t)
	m.Update(StatusMsg{Text: "scripts synced", Kind: StatusOK})

	fresh := m.statusBar()
	if !strings.Contains(textkit.StripANSI(fresh), "✓ scripts synced") {
		t.Fatalf("the status bar does not show the message: %q", fresh)
	}
	advance(statusFadeAt + 400*time.Millisecond)
	half := m.statusBar()
	if half == fresh {
		t.Error("the message rendered identically halfway through its fade")
	}
	if !strings.Contains(textkit.StripANSI(half), "scripts synced") {
		t.Error("the message vanished before its window was up")
	}
	advance(statusTTL + time.Millisecond)
	if strings.Contains(textkit.StripANSI(m.statusBar()), "scripts synced") {
		t.Error("the message outlived its window")
	}

	// DELIBERATE FLIP (v1.1.0 task 2): the 100 ms standalone fade tick
	// (StatusFadeMsg/fadeCmd) is dead. The 1 Hz beat arms the SHARED 16 ms
	// animation clock only when a dissolve is nearly due, and the clock both
	// clears the message at the end and then disarms itself.
	m.Update(LiveRunsMsg{})    // drop the fixture's live run: the fade alone decides
	advance(0)                 // rewind to the message's fresh instant
	m.Update(FrameMsg(frozen)) // one beat lets the clock notice and disarm
	if cmd := m.kickAnim(); cmd != nil {
		t.Error("a fresh message armed the frame clock it does not need yet")
	}
	advance(statusFadeAt)
	if cmd := m.kickAnim(); cmd == nil {
		t.Error("the frame clock was not armed inside the fade window")
	}
	advance(statusTTL)
	m.Update(FrameMsg(m.now()))
	if m.statusText != "" {
		t.Errorf("the frame beat left an expired message up: %q", m.statusText)
	}
	if m.animOn {
		t.Error("the clock stayed armed after the fade finished")
	}
}

// The fade must survive a profile with no colours at all — blending nil stops
// is the phase-10 crash class.
func TestStatusFadeUnderNoColour(t *testing.T) {
	for _, prof := range []colorprofile.Profile{colorprofile.Ascii, colorprofile.ANSI, colorprofile.TrueColor} {
		m, advance := clockedModel(t)
		m.useTheme(theme.New(theme.Default, prof))
		m.Update(StatusMsg{Text: "scripts synced", Kind: StatusErr})
		advance(statusFadeAt + 400*time.Millisecond)
		if got := textkit.StripANSI(m.statusBar()); !strings.Contains(got, "scripts synced") {
			t.Errorf("profile %v lost the message mid-fade: %q", prof, got)
		}
		_ = m.frame()
	}
}

// ---------------------------------------------------------------------------
// The recent-runs card (design D-1)
// ---------------------------------------------------------------------------

// The card shows the fleet's newest runs, newest first, from the rows the view
// already loaded — and both stopped statuses read as "stopped".
func TestRecentRunsCard(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	frame := plainFrame(m)

	if !strings.Contains(frame, "─ recent") {
		t.Fatalf("the recent-runs card is missing from a 160x40 Fleet frame:\n%s", frame)
	}
	rows := recentRows(m.th, m.recent, m.now(), 36, recentCardRows)
	plain := textkit.StripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "heartbeat") || !strings.Contains(plain, "backup-db") {
		t.Errorf("the card does not name the recent runs:\n%s", plain)
	}
	if strings.Index(plain, "heartbeat") > strings.Index(plain, "nightly-r") {
		t.Errorf("the card is not newest-first:\n%s", plain)
	}

	// killed and timeout both read as "stopped" at this size
	rows2 := m.recent
	rows2[len(rows2)-1].Status = "timeout"
	if got := textkit.StripANSI(strings.Join(recentRows(m.th, rows2, m.now(), 36, 3), "\n")); !strings.Contains(got, "stopped") {
		t.Errorf("a timeout does not read as stopped:\n%s", got)
	}

	// and it stays off a rail with no room for it rather than being squeezed
	// into one line that says nothing
	short := newFixtureModel(t, truecolorEnv)
	short.Update(tea.WindowSizeMsg{Width: 160, Height: 12})
	if strings.Contains(plainFrame(short), "─ recent") {
		t.Errorf("the card appeared on a rail too short to hold it:\n%s", plainFrame(short))
	}
}

// ---------------------------------------------------------------------------
// The theme config key (Go-only, parity divergence 23)
// ---------------------------------------------------------------------------

func TestThemeConfigKey(t *testing.T) {
	for _, c := range []struct {
		name, configured, want, warn string
	}{
		{"unset", "", theme.Default, ""},
		{"registered", "gruvbox-dark", "gruvbox-dark", ""},
		{"terminal palette", "terminal", "terminal", ""},
		{"tint id", "dracula", "dracula", ""},
		{"tint id, kebab spelling", "rose-pine", "rose_pine", ""},
		{"curated beats the tint of the same family", "tokyo_night", "tokyo-night", ""},
		{"unknown", "solarised-beige", theme.Default, "unknown theme 'solarised-beige'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := themeApp(t, c.configured)
			m := New(a, fixedNow)
			if m.th.Name != c.want {
				t.Errorf("theme = %q, want %q", m.th.Name, c.want)
			}
			warned := strings.Join(a.Warnings, "\n")
			if c.warn == "" && strings.Contains(warned, "theme") {
				t.Errorf("a valid theme warned: %q", warned)
			}
			if c.warn != "" && !strings.Contains(warned, c.warn) {
				t.Errorf("warnings = %q, want one naming %q", warned, c.warn)
			}
			if c.warn != "" && !strings.Contains(warned, theme.Default) {
				t.Errorf("the warning does not say what it fell back to: %q", warned)
			}
			// the v1.0.1 warning names near matches instead of dumping the
			// whole registry — "solarised" should suggest the solarized family
			if c.warn != "" {
				if !strings.Contains(warned, "closest:") || !strings.Contains(warned, "solar") {
					t.Errorf("the warning names no near matches: %q", warned)
				}
			}
		})
	}
}

// themeApp is a minimal app whose config.json carries (or omits) a theme key.
func themeApp(t *testing.T, name string) *app.App {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := fmt.Sprintf(`{"dataDir":%q}`, dataDir)
	if name != "" {
		cfg = fmt.Sprintf(`{"dataDir":%q,"theme":%q}`, dataDir, name)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := app.OpenWith(appDir, fakeCrontab(t))
	if err != nil {
		t.Fatal(err)
	}
	// a Go-only key must not produce the PS-parity "unknown key" warning
	for _, w := range a.Warnings {
		if strings.Contains(w, "unknown key") {
			t.Errorf("config warned about a known key: %q", w)
		}
	}
	return a
}
