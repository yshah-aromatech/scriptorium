package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

func fleetAt(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func plainFrame(m *Model) string { return textkit.StripANSI(m.frame()) }

// tableLines is just the script table, rendered through the view's own layout
// budget — so an assertion about a row cannot accidentally match the agenda
// card or the status bar, which mention script names too.
func tableLines(m *Model) []string {
	lay := fleetLayoutFor(m.w)
	rows := m.fleet.table(m, lay.tableW, m.bodyHeight()-1, lay.wide)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = textkit.StripANSI(r)
	}
	return out
}

func tableText(m *Model) string { return strings.Join(tableLines(m), "\n") }

func scriptRow(m *Model, name string) string {
	for _, l := range tableLines(m) {
		if strings.Contains(l, name) {
			return l
		}
	}
	return ""
}

// The summary strip is the headline: four counts derived from the same data
// the rows and the agenda are drawn from.
func TestSummaryStripCounts(t *testing.T) {
	m := fleetAt(t, 120, 40)
	s := m.fleet.summary(m)
	// fixture: backup-db + nightly-report + heartbeat succeeded, sync-orders
	// failed, cleanup-tmp never ran, nightly-report is missed.
	if s.OK != 3 || s.Failing != 1 || s.Missed != 1 || s.Total != 5 {
		t.Errorf("summary = %+v, want 3 ok / 1 failing / 1 missed / 5 total", s)
	}
	// heartbeat runs every 10 minutes, so at least one fire is due within the hour
	if s.DueHour == 0 {
		t.Errorf("nothing counted as due <1h despite %v", m.schedules)
	}
	strip := textkit.StripANSI(summaryStrip(m.th, s, 120))
	for _, want := range []string{"● 3 ok", "✗ 1 failing", "⚠ 1 missed", "5 scripts"} {
		if !strings.Contains(strip, want) {
			t.Errorf("strip %q is missing %q", strip, want)
		}
	}
}

// Every script gets a row, with the badge its last status earns and the
// running badge when a lock says otherwise — including a lock this session did
// not create.
func TestRowsAndBadges(t *testing.T) {
	m := fleetAt(t, 200, 60)
	frame := tableText(m)
	for _, want := range []string{
		"✓ backup-db",
		"· cleanup-tmp", // never run
		"✓ heartbeat",   //
		"✓ nightly-report",
		"▶ sync-orders", // an external lock outranks the failed history row
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame is missing row %q:\n%s", want, frame)
		}
	}
	// the missed script carries the warning glyph, the merely scheduled ones ↻
	if l := scriptRow(m, "nightly-report"); !strings.Contains(l, "⚠") {
		t.Errorf("nightly-report row has no missed flag: %q", l)
	}
	if l := scriptRow(m, "backup-db"); !strings.Contains(l, "↻") {
		t.Errorf("backup-db row has no schedule glyph: %q", l)
	}
	if l := scriptRow(m, "cleanup-tmp"); strings.Contains(l, "↻") {
		t.Errorf("cleanup-tmp is unscheduled but shows a schedule glyph: %q", l)
	}
	// the surplus width goes to what each script is for, not to padding
	if l := scriptRow(m, "backup-db"); !strings.Contains(l, "nightly postgres dump") {
		t.Errorf("a 200-column row has no description: %q", l)
	}
}

// Design §4's floor rule: at 80 columns the sparkline and the schedule
// expression come out of the table, and the missed flag does not.
func TestFloorCollapse(t *testing.T) {
	wideM, floorM := fleetAt(t, 120, 40), fleetAt(t, 80, 24)
	wide, floor := tableText(wideM), tableText(floorM)

	if !strings.Contains(wide, "0 2 * * *") {
		t.Errorf("the wide table lost the schedule column:\n%s", wide)
	}
	if !strings.ContainsAny(wide, blocks) {
		t.Errorf("the wide table lost the sparkline column:\n%s", wide)
	}
	if strings.Contains(floor, "0 2 * * *") {
		t.Errorf("the 80-column table kept the schedule column:\n%s", floor)
	}
	if strings.ContainsAny(floor, blocks) {
		t.Errorf("the 80-column table kept the sparkline column:\n%s", floor)
	}
	if !strings.Contains(scriptRow(floorM, "nightly-report"), "⚠") {
		t.Errorf("the 80-column table dropped the missed flag:\n%s", floor)
	}
	floorFrame := plainFrame(floorM)
	// and the rail's cards survive the collapse by stacking under the table
	for _, want := range []string{"upcoming", "live now"} {
		if !strings.Contains(floorFrame, want) {
			t.Errorf("the 80-column frame dropped the %q card:\n%s", want, floorFrame)
		}
	}
}

// f narrows the list to what is broken, and says so; f again restores it.
func TestFailureFilter(t *testing.T) {
	m := fleetAt(t, 120, 40)
	press(m, "f")
	if !m.fleet.failOnly {
		t.Fatal("f did not engage the filter")
	}
	table := tableText(m)
	if !strings.Contains(table, "sync-orders") {
		t.Errorf("the filtered list lost the failing script:\n%s", table)
	}
	if strings.Contains(table, "cleanup-tmp") || strings.Contains(table, "backup-db") {
		t.Errorf("the filtered list kept healthy scripts:\n%s", table)
	}
	if !strings.Contains(table, "failures only") {
		t.Errorf("the filter is on but the pane title does not say so:\n%s", table)
	}
	press(m, "f")
	if m.fleet.failOnly || !strings.Contains(tableText(m), "cleanup-tmp") {
		t.Error("f did not restore the full list")
	}
}

// With nothing failing, the filter says that rather than showing a blank pane.
func TestFailureFilterEmptyState(t *testing.T) {
	m := fleetAt(t, 120, 40)
	delete(m.statuses, "sync-orders")
	press(m, "f")
	if got := tableText(m); !strings.Contains(got, "nothing is failing") {
		t.Errorf("empty filtered list:\n%s", got)
	}
}

// Enter deep-links into the Run view with the highlighted script carried over
// — the two views have to agree about what "the current script" is.
func TestEnterDeepLinksToRun(t *testing.T) {
	m := fleetAt(t, 120, 40)
	press(m, "down", "down") // heartbeat, third alphabetically
	want := m.fleet.selected(m).Name
	press(m, "enter")

	if m.mode != modeRun {
		t.Fatalf("Enter left the mode at %v", m.mode)
	}
	if got := m.run.selected(m); got == nil || got.Name != want {
		t.Errorf("Run view selected %v, want %s", got, want)
	}
	if got := m.selected(); got == nil || got.Name != want {
		t.Errorf("the frame's current script is %v, want %s", got, want)
	}
}

// Navigation stays inside the list and the window follows the selection.
func TestNavigationClamps(t *testing.T) {
	m := fleetAt(t, 120, 40)
	press(m, "up", "up", "up")
	if m.fleet.sel != 0 {
		t.Errorf("up ran off the top: %d", m.fleet.sel)
	}
	for range 20 {
		press(m, "down")
	}
	if want := len(m.scripts) - 1; m.fleet.sel != want {
		t.Errorf("down ran off the bottom: %d, want %d", m.fleet.sel, want)
	}
	press(m, "g")
	if m.fleet.sel != 0 {
		t.Errorf("g = %d", m.fleet.sel)
	}
}

func TestScrollWindow(t *testing.T) {
	cases := []struct{ top, sel, n, h, want int }{
		{0, 0, 10, 4, 0},
		{0, 3, 10, 4, 0},
		{0, 4, 10, 4, 1}, // selection just left the window
		{5, 2, 10, 4, 2}, // and just left it upward
		{9, 9, 10, 4, 6}, // clamped to the last full page
		{0, 0, 0, 4, 0},  // empty list
		{3, 3, 10, 0, 0}, // no room
	}
	for _, c := range cases {
		if got := scrollWindow(c.top, c.sel, c.n, c.h); got != c.want {
			t.Errorf("scrollWindow(%d,%d,%d,%d) = %d, want %d", c.top, c.sel, c.n, c.h, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Pure render funcs, on their own
// ---------------------------------------------------------------------------

func TestSparkline(t *testing.T) {
	th := theme.New(theme.Default, theme.Profile("auto", truecolorEnv))
	if got := textkit.StripANSI(sparkline(th, nil, 6, nil)); got != "······" {
		t.Errorf("empty series = %q, want a muted placeholder", got)
	}
	// scaled to the series' own peak: the max value is always a full block
	got := textkit.StripANSI(sparkline(th, []float64{0, 1, 2, 3, 4, 5}, 6, nil))
	if !strings.HasSuffix(got, "█") {
		t.Errorf("sparkline %q does not end at full height", got)
	}
	if textkit.Width(got) != 6 {
		t.Errorf("sparkline %q is not 6 cells", got)
	}
	// a flat low series still reads, because the scale is relative
	if flat := textkit.StripANSI(sparkline(th, []float64{2, 2, 2}, 6, nil)); flat != "   ███" {
		t.Errorf("flat series = %q", flat)
	}
	// longer series keep their tail
	long := textkit.StripANSI(sparkline(th, []float64{9, 9, 9, 9, 9, 9, 9, 0}, 6, nil))
	if !strings.HasSuffix(long, "▁") {
		t.Errorf("a long series lost its most recent point: %q", long)
	}
	// heat discipline (v1.0.1): the peak heats, the trough stays Info —
	// the full ≥80%-of-peak contract is pinned in heat_test.go
	hot := sparkline(th, []float64{0, 100}, 2, nil)
	if !strings.Contains(hot, th.S.Warning.Render("█")) {
		t.Errorf("the peak is not painted with the heat color: %q", hot)
	}
	if !strings.Contains(hot, th.S.Info.Render("▁")) {
		t.Errorf("the trough is not painted in the sparkline hue: %q", hot)
	}
}

func TestAgendaIsSoonestFirst(t *testing.T) {
	m := fleetAt(t, 120, 40)
	items := m.fleet.agenda(m)
	if len(items) != 3 {
		t.Fatalf("agenda = %+v, want one item per schedule", items)
	}
	for i := 1; i < len(items); i++ {
		if items[i].In < items[i-1].In {
			t.Errorf("agenda is out of order: %+v", items)
		}
	}
	rows := textkit.StripANSI(strings.Join(agendaRows(m.th, items, 36, 6), "\n"))
	if !strings.Contains(rows, "upcoming") || !strings.Contains(rows, items[0].Name) {
		t.Errorf("agenda card:\n%s", rows)
	}
}

func TestActivityCard(t *testing.T) {
	th := theme.New(theme.Default, theme.Profile("auto", truecolorEnv))
	idle := textkit.StripANSI(strings.Join(activityRows(th, nil, frozen, "⠋", 0, 36, 6), "\n"))
	if !strings.Contains(idle, "idle") {
		t.Errorf("an empty activity card should say idle:\n%s", idle)
	}
	live := []lockfile.Live{
		{Name: "sync-orders", OwnerPID: 4242, External: true, StartedAt: frozen.Add(-95 * time.Second)},
		{Name: "backup-db", OwnerPID: 99, StartedAt: frozen.Add(-5 * time.Second)},
	}
	card := textkit.StripANSI(strings.Join(activityRows(th, live, frozen, "⠋", 2, 36, 6), "\n"))
	for _, want := range []string{"sync-orders", "↗", "4242", "1m", "2 queued"} {
		if !strings.Contains(card, want) {
			t.Errorf("activity card is missing %q:\n%s", want, card)
		}
	}
	// overflow is admitted, not silently dropped
	many := make([]lockfile.Live, 8)
	for i := range many {
		many[i] = lockfile.Live{Name: "job", OwnerPID: i + 1, StartedAt: frozen}
	}
	if got := textkit.StripANSI(strings.Join(activityRows(th, many, frozen, "⠋", 0, 36, 4), "\n")); !strings.Contains(got, "more") {
		t.Errorf("overflow was dropped without a note:\n%s", got)
	}
}

// The spinner only ticks while something is live, so an idle TUI is idle.
func TestSpinnerOnlyRunsWhileSomethingIs(t *testing.T) {
	m := fleetAt(t, 120, 40)
	if !m.animating() || !m.spinOn {
		t.Fatal("a live lock did not start the spinner")
	}
	m.Update(LiveRunsMsg(nil))
	if m.animating() {
		t.Fatal("no locks, still animating")
	}
	if _, cmd := m.Update(m.spin.Tick()); cmd != nil {
		t.Error("the spinner kept ticking with nothing running")
	}
	if m.spinOn {
		t.Error("the spinner did not release its ticker")
	}
	// and it restarts when something becomes live again
	m.Update(LiveRunsMsg{{Name: "backup-db", OwnerPID: 7, StartedAt: frozen}})
	if !m.spinOn {
		t.Error("a new live run did not restart the spinner")
	}
}

func TestFleetGoldens(t *testing.T) {
	goldenFrames(t, "fleet", func(t *testing.T, env []string, w, h int) *Model {
		return newFixtureModel(t, env)
	})
	goldenFrames(t, "fleet-failures", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.fleet.failOnly = true
		return m
	})
	goldenFrames(t, "fleet-empty", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.Update(ScriptsLoadedMsg{})
		m.Update(LiveRunsMsg(nil))
		m.missed = nil
		return m
	})
}
