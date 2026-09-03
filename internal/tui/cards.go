package tui

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// Pure render functions: (data, width, theme) -> rows. No model, no clock of
// their own, no side effects — which is what makes every one of them
// golden-testable on its own, without a running program.
//
// The visual language is rules rather than boxes. A full box costs two columns
// on each side of every pane; a top rule with an inset title costs none, says
// the same thing, and leaves the 80-column floor with room for content.

// The sparkline alphabet is braille (v1.1.0 task 3): each cell is a 2×4 dot
// grid — two samples per cell, four levels per column — so the same width
// carries twice the history at eight times the sub-cell resolution of the old
// eighth-block ramp, and the shape survives monochrome untouched.
//
// brailleLeft/brailleRight are the dot bitmasks for one column filled
// bottom-up by level 0-4 (U+2800 base; dots 7/3/2/1 left, 8/6/5/4 right).
var (
	brailleLeft  = [5]rune{0, 0x40, 0x44, 0x46, 0x47}
	brailleRight = [5]rune{0, 0x80, 0xA0, 0xB0, 0xB8}
)

// sectionRule draws "─ title ───────────" across w cells. A focused pane's
// rule and title take the accent; everything else stays in the border color,
// so which pane has the keyboard is legible without reading a label.
func sectionRule(th theme.Theme, title string, w int, focused bool) string {
	line, text := th.S.Border, th.S.Title
	if focused {
		line, text = th.S.BorderOn, th.S.TitleOn
	}
	if title == "" {
		return line.Render(strings.Repeat("─", max(w, 0)))
	}
	head := "─ " + title + " "
	fill := max(w-textkit.Width(head), 0)
	return line.Render("─ ") + text.Render(title) + line.Render(" "+strings.Repeat("─", fill))
}

// fleetSummary is the at-a-glance count strip's data (design §4.1).
type fleetSummary struct {
	OK      int
	Failing int
	Missed  int
	DueHour int
	Total   int
}

// summaryStrip is the first thing on the Fleet screen: four counts, each in
// the color of the thing it counts, and a zero count muted so the eye skips
// it. The right end says how big the fleet is.
func summaryStrip(th theme.Theme, s fleetSummary, w int) string {
	chip := func(glyph string, n int, label string, st lipgloss.Style) string {
		if n == 0 {
			st = th.S.Muted // a zero count should not compete for attention
		}
		return st.Render(glyph+" "+strconv.Itoa(n)) + th.S.Desc.Render(" "+label)
	}
	left := " " + strings.Join([]string{
		chip("●", s.OK, "ok", th.S.Success),
		chip("✗", s.Failing, "failing", th.S.Danger),
		chip("⚠", s.Missed, "missed", th.S.Warning),
		chip("⏲", s.DueHour, "due <1h", th.S.Info),
	}, th.S.Border.Render("  ·  "))
	right := th.S.Muted.Render(strconv.Itoa(s.Total)+" scripts") + " "
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return textkit.Truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// sparkline renders a series as `cells` braille columns — two samples per
// cell, four dot levels per column — scaled to the series' own maximum (a
// script that never exceeds 5% CPU still gets a readable shape). Fewer
// samples than slots right-aligns the shape against the age column.
//
// Heat discipline (v1.0.1 task 2, preserved): the line is single-hue Info;
// only cells holding a sample at ≥80% of the series' own peak carry the heat
// color — color means exceptional, and the SHAPE still carries the whole
// series under NO_COLOR.
func sparkline(th theme.Theme, series []float64, cells int, bg color.Color) string {
	if cells <= 0 {
		return ""
	}
	if len(series) == 0 {
		return tint(th.S.Muted, bg).Render(strings.Repeat("·", cells))
	}
	slots := cells * 2
	if len(series) > slots {
		series = series[len(series)-slots:]
	}
	peak := 0.0
	for _, v := range series {
		peak = max(peak, v)
	}
	// a present sample always draws at least its bottom dot: a flatline at
	// zero is still a visible baseline, not an empty column
	level := func(v float64) int {
		if peak <= 0 {
			return 1
		}
		return min(max(int(v/peak*4+0.5), 1), 4)
	}

	vals := make([]float64, slots)
	present := make([]bool, slots)
	for i, v := range series {
		at := slots - len(series) + i
		vals[at], present[at] = v, true
	}

	var b strings.Builder
	for c := range cells {
		l, r := 2*c, 2*c+1
		if !present[l] && !present[r] {
			if bg != nil {
				b.WriteString(lipgloss.NewStyle().Background(bg).Render(" "))
			} else {
				b.WriteString(" ")
			}
			continue
		}
		glyph, heat := rune(0x2800), false
		if present[l] {
			glyph |= brailleLeft[level(vals[l])]
			heat = heat || (peak > 0 && vals[l] >= 0.8*peak)
		}
		if present[r] {
			glyph |= brailleRight[level(vals[r])]
			heat = heat || (peak > 0 && vals[r] >= 0.8*peak)
		}
		st := th.S.Info
		if heat {
			st = th.S.Warning
		}
		b.WriteString(tint(st, bg).Render(string(glyph)))
	}
	return b.String()
}

// badge is the leading status glyph of a script row, ported from the PS list
// (inventory §1.12): the shape carries the meaning, the color reinforces it.
func badge(th theme.Theme, status string, bg color.Color) string {
	glyph, st := "·", th.S.Muted
	switch status {
	case "success":
		glyph, st = "✓", th.S.Success
	case "failure":
		glyph, st = "✗", th.S.Danger
	case "killed":
		glyph, st = "⊘", th.S.Warning
	case "timeout":
		glyph, st = "◷", th.S.Warning
	case "skipped":
		glyph, st = "◇", th.S.Warning
	case "running":
		glyph, st = "▶", th.S.Info
	case "queued":
		glyph, st = "»", th.S.Info
	}
	return tint(st, bg).Render(glyph)
}

// runtimeTag is the two-cell "ps"/"py" chip that says which interpreter a
// script needs — the one column that is never worth truncating.
func runtimeTag(th theme.Theme, runtime string, bg color.Color) string {
	if runtime == "python" {
		return tint(th.S.RuntimePy, bg).Render("py")
	}
	return tint(th.S.RuntimePS, bg).Render("ps")
}

// agendaItem is one upcoming cron fire.
type agendaItem struct {
	Name string
	Expr string
	In   time.Duration
}

// agendaRows renders the upcoming-runs card at the floor: a title rule over
// agendaBody. Paneled frames wrap the body in a rounded panel instead.
func agendaRows(th theme.Theme, items []agendaItem, w, h int) []string {
	if h <= 0 {
		return nil
	}
	return append([]string{sectionRule(th, "upcoming", w, false)}, agendaBody(th, items, w, h-1)...)
}

// agendaBody is the card's content: soonest first, the countdown in the
// gutter so the column reads as a timeline.
func agendaBody(th theme.Theme, items []agendaItem, w, h int) []string {
	if len(items) == 0 {
		return []string{" " + th.S.Muted.Render("nothing scheduled")}
	}
	var rows []string
	for _, it := range items {
		if len(rows) >= h {
			break
		}
		when := th.S.Info.Render(right(format.RelativeTime(it.In.Seconds()), 6))
		name := textkit.Truncate(th.S.Desc.Render(it.Name), max(w-9, 1))
		rows = append(rows, " "+when+"  "+name)
	}
	return rows
}

// activityRows renders the live-activity card: every held lock, whoever
// started it — this session, cron, the MCP server or another shell. A run this
// TUI did not start is exactly as real as one it did, which is why the source
// is on the row.
func activityRows(th theme.Theme, live []lockfile.Live, now time.Time, spin string, queued int, w, h int) []string {
	if h <= 0 {
		return nil
	}
	return append([]string{sectionRule(th, "live now", w, false)},
		activityBody(th, live, now, spin, queued, w, h-1)...)
}

// activityBody is the live-activity card's content (see activityRows).
func activityBody(th theme.Theme, live []lockfile.Live, now time.Time, spin string, queued int, w, h int) []string {
	var rows []string
	// the queue line always gets a row if there is a queue at all
	room := h - boolInt(queued > 0)
	for i, l := range live {
		if len(rows) >= room-1 && i < len(live)-1 {
			rows = append(rows, " "+th.S.Muted.Render("+"+strconv.Itoa(len(live)-i)+" more"))
			break
		}
		elapsed := th.S.Muted.Render(right(format.RelativeTime(now.Sub(l.StartedAt).Seconds()), 5))
		pid := th.S.Muted.Render("·" + strconv.Itoa(l.OwnerPID))
		mark := " "
		if l.External {
			mark = th.S.Info.Render("↗") // started outside this session: cron, MCP, another shell
		}
		name := textkit.Truncate(l.Name, max(w-9-lipgloss.Width(pid), 1))
		rows = append(rows, " "+th.S.Pulse.Render(spin)+" "+name+mark+" "+elapsed+" "+pid)
	}
	if queued > 0 {
		rows = append(rows, " "+th.S.Info.Render("»")+" "+
			th.S.Desc.Render(strconv.Itoa(queued)+" queued")+" "+
			th.S.Muted.Render("· X clears"))
	}
	if len(rows) == 0 {
		rows = append(rows, " "+th.S.Muted.Render("idle"))
	}
	if len(rows) > h {
		rows = rows[:max(h, 0)]
	}
	return rows
}

// recentRows is the recent-runs card (design D-1): the last few runs across the
// WHOLE fleet, newest first — what just happened, how it went, how hard it
// worked. It reads the history rows the Fleet view already loaded, so the card
// costs a slice rather than a second read of history.jsonl.
//
// killed and timeout both read as "stopped": at this size the distinction is
// noise, and the History view has it in full.
func recentRows(th theme.Theme, rows []history.Row, now time.Time, w, h int) []string {
	if h <= 1 {
		return nil
	}
	return append([]string{sectionRule(th, "recent", w, false)},
		recentBody(th, rows, now, w, h-1)...)
}

// recentBody is the recent-runs card's content (see recentRows).
func recentBody(th theme.Theme, rows []history.Row, now time.Time, w, h int) []string {
	if len(rows) == 0 {
		return []string{" " + th.S.Muted.Render("no runs yet")}
	}
	var out []string
	nameW := max(min(w-26, 22), 8)
	for i := len(rows) - 1; i >= 0 && len(out) < h; i-- {
		row := rows[i]
		word, st := row.Status, th.S.Warning
		switch row.Status {
		case "success":
			st = th.S.Success
		case "failure":
			st = th.S.Danger
		case "killed", "timeout":
			word = "stopped"
		}
		age := ""
		if !row.StartedAt.IsZero() {
			age = format.RelativeTime(now.Sub(row.StartedAt.Time().Local()).Seconds())
		}
		var series []float64
		if row.Resources != nil {
			series = row.Resources.CPUSeries
		}
		out = append(out, " "+st.Render(statusGlyph(row.Status))+" "+
			th.S.Base.Render(textkit.Fit(row.Script, nameW))+" "+
			runtimeTag(th, row.Runtime, nil)+" "+
			th.S.Muted.Render(textkit.Fit(word, 7))+" "+
			sparkline(th, series, sparkCells, nil)+" "+
			th.S.Desc.Render(right(age, 4)))
	}
	return out
}

// ---------------------------------------------------------------------------

// right right-aligns s in w cells (truncating from the left is never right for
// a duration, so an over-wide value is returned whole).
func right(s string, w int) string {
	if gap := w - textkit.VisibleWidth(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

// tint applies a row background to a cell style. Nested lipgloss styles reset
// the background at every span end, so a row with a background has to hand it
// to each cell rather than wrapping them.
func tint(s lipgloss.Style, bg color.Color) lipgloss.Style {
	if bg == nil {
		return s
	}
	return s.Background(bg)
}

// fillTo pads a row out to w cells, carrying bg to the panel edge so a
// highlighted row is a solid band rather than a ragged one.
func fillTo(s string, w int, bg color.Color) string {
	gap := w - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	spaces := strings.Repeat(" ", gap)
	if bg == nil {
		return s + spaces
	}
	return s + lipgloss.NewStyle().Background(bg).Render(spaces)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
