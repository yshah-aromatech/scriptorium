package tui

import (
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// Fleet is the home view (design §4.1): what the whole fleet is doing, in one
// screen. A summary strip, one row per script, and — when the terminal is wide
// enough — a rail carrying what is about to fire and what is running now.
//
// Layout responds in one step rather than many. Above railMinWidth the rail is
// beside the table; below it the cards stack under the table, which keeps them
// rather than dropping them (they answer "is anything happening?", the whole
// point of the view). The 80×24 floor drops the sparkline and schedule columns
// from the table, per design §4's floor rules; the missed flag survives,
// because a silently missed cron fire is the one thing this screen exists to
// surface.
const (
	railMinWidth = 104
	railWidth    = 36

	// wideTableMin is design §4's floor rule made concrete: at and below the
	// 80-column frame the sparkline and the schedule expression come out of
	// the table. The schedule GLYPH stays at every width — a silently missed
	// cron fire is the one thing this screen exists to surface.
	wideTableMin = 96

	sparkCells  = 6
	scheduleCol = 13

	// recentCardRows is the recent-runs card at full size: its rule and the
	// PS card's five rows (Get-TuiRecentHeight).
	recentCardRows = 6

	// nameColMax caps the script-name column; it otherwise sizes itself to the
	// longest name on screen. A name column that grows with the terminal just
	// pushes the numbers to the far edge, and every cell it gives back goes to
	// what the script is FOR, which is the more useful thing to read.
	nameColMax = 30
	descMin    = 16
)

// fleetLayout is the view's column budget, computed in one place so the
// renderer and its tests cannot disagree about it.
type fleetLayout struct {
	tableW, railW int
	wide          bool
}

func fleetLayoutFor(w int) fleetLayout {
	l := fleetLayout{tableW: w}
	if w >= railMinWidth {
		l.railW = railWidth
		l.tableW = w - railWidth - 1
	}
	// design §4's floor rule, plus the obvious guard: even a wide frame gives
	// the table no sparkline if the rail has left it too little room.
	l.wide = w >= wideTableMin && l.tableW >= 52
	return l
}

type fleetModel struct {
	w, h     int
	sel      int
	top      int
	failOnly bool
}

func (f *fleetModel) init(*Model)               {}
func (f *fleetModel) resize(_ *Model, w, h int) { f.w, f.h = w, h }

// reload keeps the highlight on the same script across a refresh where it can,
// and inside the list where it cannot.
func (f *fleetModel) reload(m *Model) { f.clamp(m) }

func (f *fleetModel) clamp(m *Model) {
	n := len(f.rows(m))
	f.sel = min(max(f.sel, 0), max(n-1, 0))
}

// rows is the visible script list — every script, or only the ones whose last
// run failed when the `f` filter is on.
func (f *fleetModel) rows(m *Model) []scripts.Script {
	if !f.failOnly {
		return m.scripts
	}
	out := make([]scripts.Script, 0, len(m.scripts))
	for _, s := range m.scripts {
		if failing(m.statuses[s.Name].Status) {
			out = append(out, s)
		}
	}
	return out
}

func failing(status string) bool {
	return status == "failure" || status == "timeout"
}

func (f *fleetModel) selected(m *Model) *scripts.Script {
	rows := f.rows(m)
	if f.sel < 0 || f.sel >= len(rows) {
		return nil
	}
	return &rows[f.sel]
}

func (f *fleetModel) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		// row 0 is the summary strip, row 1 the table rule
		if row := msg.Mouse().Y - headerRows - 2; row >= 0 {
			if idx := f.top + row; idx < len(f.rows(m)) {
				f.sel = idx
			}
		}
		return nil
	case tea.MouseWheelMsg:
		if msg.Mouse().Button == tea.MouseWheelUp {
			f.sel = max(f.sel-1, 0)
		} else {
			f.sel = min(f.sel+1, max(len(f.rows(m))-1, 0))
		}
		return nil
	}
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	k := m.keys
	n := len(f.rows(m))
	switch {
	case key.Matches(press, k.Up):
		f.sel = max(f.sel-1, 0)
	case key.Matches(press, k.Down):
		f.sel = min(f.sel+1, max(n-1, 0))
	case key.Matches(press, k.Top):
		f.sel = 0
	case key.Matches(press, k.Bottom):
		f.sel = max(n-1, 0)
	case key.Matches(press, k.FailFilter):
		f.failOnly = !f.failOnly
		f.sel, f.top = 0, 0
		if f.failOnly {
			return status(StatusInfo, "showing failures only — f shows everything again")
		}
		return status(StatusInfo, "showing all scripts")
	case key.Matches(press, k.Open):
		return f.openInRun(m)
	case key.Matches(press, k.Start):
		// Output belongs to the Run view, so starting from here goes there:
		// a run you cannot watch is worse than one keystroke of travel.
		s := f.selected(m)
		if s == nil {
			return nil
		}
		cmd := f.openInRun(m)
		return tea.Batch(cmd, m.run.start(m, *s))
	case key.Matches(press, k.Sync):
		f.openInRun(m)
		return m.run.sync(m)
	}
	return nil
}

// openInRun is the Enter deep-link: switch to the Run view with this script
// selected, so the two views agree about what "the current script" means.
func (f *fleetModel) openInRun(m *Model) tea.Cmd {
	s := f.selected(m)
	if s == nil {
		return nil
	}
	m.run.selectByName(m, s.Name)
	return m.switchTo(modeRun)
}

// ---------------------------------------------------------------------------

func (f *fleetModel) view(m *Model, w, h int) []string {
	f.clamp(m)
	if paneled(w) {
		return f.viewPaneled(m, w, h)
	}
	rows := []string{summaryStrip(m.th, f.summary(m), w)}
	lay := fleetLayoutFor(w)

	body := h - len(rows)
	table := f.table(m, lay.tableW, body, lay.wide)
	if lay.railW == 0 {
		// stacked: the table takes what it needs, the cards take the rest —
		// they answer "is anything happening?", which is the point of the view
		used := min(len(table), body)
		out := append(rows, table[:used]...)
		if rest := body - used; rest > 2 {
			out = append(out, f.cards(m, w, rest)...)
		}
		return out
	}

	rail := f.cards(m, lay.railW, body)
	sep := m.th.S.Border.Render("│")
	for i := range body {
		rows = append(rows, fillTo(rowAt(table, i), lay.tableW, nil)+sep+rowAt(rail, i))
	}
	return rows
}

// viewPaneled is the Fleet frame at and above panelMinWidth: the summary
// strip, then the script table and the rail cards each in a rounded panel.
// The 1-cell gap the floor spent on a separator rule becomes the gap between
// the table panel and the rail panels.
func (f *fleetModel) viewPaneled(m *Model, w, h int) []string {
	th := m.th
	pad := panelPad(w)
	rows := []string{summaryStrip(th, f.summary(m), w)}
	lay := fleetLayoutFor(w)
	body := h - 1

	tableOpts := panelOpts{title: f.title(), focused: true, pad: pad,
		hints: m.tailHints(modeFleet, focusList)}
	if lay.railW == 0 {
		// stacked: the table panel takes what its rows need, the cards the rest
		content := f.tableRows(m, w-2-2*pad, body-2, lay.wide)
		tableH := min(len(content)+2, body)
		out := append(rows, renderPanel(th, content, w, tableH, tableOpts)...)
		if rest := body - tableH; rest > 4 {
			out = append(out, f.railPanels(m, w, rest, pad)...)
		}
		return out
	}

	table := renderPanel(th, f.tableRows(m, lay.tableW-2-2*pad, body-2, lay.wide),
		lay.tableW, body, tableOpts)
	rail := f.railPanels(m, lay.railW, body, pad)
	for i := range body {
		rows = append(rows, fillTo(rowAt(table, i), lay.tableW, nil)+" "+rowAt(rail, i))
	}
	return rows
}

// railPanels is the paneled rail: upcoming, live now and — when the rail is
// tall enough — recent runs, each in its own rounded card. The live-now
// title carries the activity pulse while anything is running (anim.go).
// The cards keep pad 0 at every width: the rail is a fixed 36 columns, and
// the recent card's columns need all 34 inner cells (the wide-pad pass is
// for the panes that grow with the terminal, not for a fixed rail).
func (f *fleetModel) railPanels(m *Model, w, h, _ int) []string {
	th := m.th
	inner := w - 2
	items := f.agenda(m)

	agendaH := min(len(items)+2, max(h/3, 4))
	out := renderPanel(th, agendaBody(th, items, inner, agendaH-2), w, agendaH,
		panelOpts{title: "upcoming"})

	queued := m.run.queueDepth()
	actH := min(max(h-len(out), 3), len(m.live)+boolInt(queued > 0)+2)
	actBody := activityBody(th, m.live, m.now(), m.spinnerFrame(), queued, inner, actH-2)
	out = append(out, renderPanel(th, actBody, w, actH,
		panelOpts{title: "live now", titleStyle: m.pulseTitleStyle()})...)

	if rest := min(h-len(out), recentCardRows+1); rest >= 5 {
		out = append(out, renderPanel(th, recentBody(th, m.recent, m.now(), inner, rest-2),
			w, rest, panelOpts{title: "recent"})...)
	}
	return out
}

func rowAt(rows []string, i int) string {
	if i < len(rows) {
		return rows[i]
	}
	return ""
}

// cards is the rail: what is about to fire, what is running now, and what just
// happened. Each takes what it needs and the last one takes what is left — the
// recent-runs card appears only when the rail is tall enough to hold a useful
// slice of it, rather than being squeezed to a single row.
func (f *fleetModel) cards(m *Model, w, h int) []string {
	items := f.agenda(m)
	agendaH := min(len(items)+1, max(h/3, 2))
	out := agendaRows(m.th, items, w, agendaH)
	for len(out) < agendaH {
		out = append(out, "")
	}

	out = append(out, "")
	actH := min(max(h-len(out)-1, 1), len(m.live)+2)
	out = append(out, activityRows(m.th, m.live, m.now(), m.spinnerFrame(), m.run.queueDepth(), w, actH)...)

	if rest := min(h-len(out)-1, recentCardRows); rest >= 3 {
		out = append(out, "")
		out = append(out, recentRows(m.th, m.recent, m.now(), w, rest)...)
	}
	return out
}

// table renders the script rows under a title rule — the floor path. Column
// budget, widest first: the sparkline and the schedule expression are the two
// that go at the 80-column floor.
func (f *fleetModel) table(m *Model, w, h int, wide bool) []string {
	return append([]string{sectionRule(m.th, f.title(), w, true)},
		f.tableRows(m, w, h-1, wide)...)
}

// tableRows is the table's content: h rows of scripts, windowed on the
// selection.
func (f *fleetModel) tableRows(m *Model, w, h int, wide bool) []string {
	rows := f.rows(m)
	f.top = scrollWindow(f.top, f.sel, len(rows), h)

	if len(rows) == 0 {
		return []string{" " + m.th.S.Muted.Render(f.emptyNote())}
	}
	nameW := 8
	for _, s := range rows {
		nameW = max(nameW, textkit.Width(s.Name))
	}
	nameW = min(nameW, nameColMax)

	var out []string
	for i := f.top; i < len(rows) && len(out) < h; i++ {
		out = append(out, f.row(m, rows[i], i == f.sel, wide, w, nameW))
	}
	return out
}

func (f *fleetModel) title() string {
	if f.failOnly {
		return "scripts — failures only"
	}
	return "scripts"
}

func (f *fleetModel) emptyNote() string {
	if f.failOnly {
		return "nothing is failing — f shows everything"
	}
	return "no scripts yet — press s to sync a repo"
}

// row is one script line: what happened, what it is, how it has been behaving,
// when it next runs and how long ago it last did.
func (f *fleetModel) row(m *Model, s scripts.Script, selected, wide bool, w, nameW int) string {
	th := m.th
	var bg color.Color
	bar := " "
	if selected {
		bg = th.C.SelBg
		bar = tint(th.S.Primary, bg).Render("▎")
	}

	last := m.statuses[s.Name]
	st := last.Status
	if m.isLive(s.Name) {
		st = "running"
	}

	age := "—"
	if !last.At.IsZero() {
		age = format.RelativeTime(m.now().Sub(last.At).Seconds())
	}

	// fixed columns: bar, badge+gap, runtime, schedule glyph, age
	fixed := 1 + 2 + 3 + 2 + 6
	if wide {
		fixed += sparkCells + 1 + scheduleCol + 1
	}
	avail := max(w-fixed, 8)
	descW := 0
	if avail-nameW-1 >= descMin {
		descW = avail - nameW - 1
	} else {
		nameW = avail
	}

	// every cell carries the row background: a nested lipgloss style resets it
	// at each span end, so a selected row assembled without this is a ragged
	// band rather than a solid one.
	gap := tint(th.S.Base, bg).Render(" ")

	var b strings.Builder
	b.WriteString(bar)
	b.WriteString(badge(th, st, bg))
	b.WriteString(gap)
	b.WriteString(tint(th.S.Base, bg).Render(textkit.Fit(s.Name, nameW)))
	if descW > 0 {
		b.WriteString(gap)
		b.WriteString(tint(th.S.Muted, bg).Render(textkit.Fit(s.Description, descW)))
	}
	b.WriteString(gap)
	b.WriteString(runtimeTag(th, s.Runtime, bg))
	if wide {
		b.WriteString(gap)
		b.WriteString(sparkline(th, cpuSeries(last), sparkCells, bg))
	}
	b.WriteString(m.scheduleGlyph(s.Name, bg))
	if wide {
		b.WriteString(gap)
		b.WriteString(tint(th.S.Muted, bg).Render(textkit.Fit(m.schedules[s.Name], scheduleCol)))
	}
	b.WriteString(tint(th.S.Desc, bg).Render(right(age, 6)))
	return fillTo(b.String(), w, bg)
}

// scheduleGlyph is the two-cell column that says whether a script is on a
// schedule and whether that schedule is being honoured.
func (m *Model) scheduleGlyph(name string, bg color.Color) string {
	th := m.th
	glyph, st := " ", th.S.Muted
	switch {
	case m.missed[name].Name != "":
		glyph, st = "⚠", th.S.Danger
	case m.schedules[name] != "":
		glyph, st = "↻", th.S.Info
	}
	return tint(th.S.Base, bg).Render(" ") + tint(st, bg).Render(glyph)
}

func cpuSeries(last history.Last) []float64 {
	if last.Resources == nil {
		return nil
	}
	return last.Resources.CPUSeries
}

func (m *Model) isLive(name string) bool {
	for _, l := range m.live {
		if l.Name == name {
			return true
		}
	}
	return false
}

// summary counts the four things the strip reports. "due <1h" is computed from
// the live crontab through the same cron engine the scheduler uses, so it can
// never disagree with the agenda below it.
func (f *fleetModel) summary(m *Model) fleetSummary {
	s := fleetSummary{Total: len(m.scripts)}
	for _, sc := range m.scripts {
		switch st := m.statuses[sc.Name].Status; {
		case st == "success":
			s.OK++
		case failing(st):
			s.Failing++
		}
	}
	s.Missed = len(m.missed)
	for _, it := range f.agenda(m) {
		if it.In <= time.Hour {
			s.DueHour++
		}
	}
	return s
}

// agenda is every scheduled script's next fire, soonest first. cron.Next works
// in naive local labelling (the same arithmetic crond does), so the countdown
// here is the countdown the daemon is keeping.
func (f *fleetModel) agenda(m *Model) []agendaItem {
	now := missed.NaiveNow(m.now())
	var out []agendaItem
	for name, expr := range m.schedules {
		next, ok := cron.Next(expr, now)
		if !ok {
			continue
		}
		out = append(out, agendaItem{Name: name, Expr: expr, In: next.Sub(now)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].In != out[j].In {
			return out[i].In < out[j].In
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// scrollWindow keeps sel inside a viewport of h rows, moving the window only
// as far as it has to.
func scrollWindow(top, sel, n, h int) int {
	if h < 1 || n == 0 {
		return 0
	}
	top = min(top, max(n-h, 0))
	top = min(top, sel)
	top = max(top, sel-h+1)
	return max(top, 0)
}

// selected is the script the whole frame is talking about — the status bar's
// context line and the Run view's deep-link both read it.
func (m *Model) selected() *scripts.Script {
	if m.mode == modeRun {
		return m.run.selected(m)
	}
	return m.fleet.selected(m)
}
