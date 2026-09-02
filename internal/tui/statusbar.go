package tui

import (
	"strings"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// statusBar is the row above the key hints, and it answers one question at a
// time in this order of urgency: what is running now, what just happened, and
// — when nothing is happening — what the highlighted script is.
func (m *Model) statusBar() string {
	if line, busy := m.run.statusLine(m, m.w); busy {
		return line
	}
	if m.statusText != "" && m.now().Sub(m.statusAt) <= statusTTL {
		return textkit.Truncate(statusLine(m.th, m.statusKind, m.statusText), m.w)
	}
	return textkit.Truncate(m.contextLine(), m.w)
}

// statusLine renders one transient message with the glyph and color its kind
// earns (Set-TuiStatus's four kinds).
func statusLine(th theme.Theme, kind StatusKind, text string) string {
	glyph, style := "·", th.S.Info
	switch kind {
	case StatusOK:
		glyph, style = "✓", th.S.Success
	case StatusWarn:
		glyph, style = "⚠", th.S.Warning
	case StatusErr:
		glyph, style = "✗", th.S.Danger
	case StatusInfo:
	}
	return " " + style.Render(glyph+" "+text)
}

// contextLine is the resting state: whatever is true about the highlighted
// script. Idle chrome that says nothing is a wasted row.
func (m *Model) contextLine() string {
	s := m.selected()
	if s == nil {
		if len(m.scripts) == 0 {
			return " " + m.th.S.Muted.Render("no scripts yet — press s to sync a repo")
		}
		return ""
	}
	th := m.th
	parts := []string{th.S.Desc.Render(s.Name)}
	if s.Description != "" {
		parts = append(parts, th.S.Muted.Render(s.Description))
	}
	if hint := m.scheduleHint(s.Name); hint != "" {
		parts = append(parts, hint)
	}
	return " " + strings.Join(parts, th.S.Border.Render(" · "))
}

// scheduleHint is the schedule half of the context line: the expression plus
// when it next fires, or the missed-fire warning that replaces both.
func (m *Model) scheduleHint(name string) string {
	expr, ok := m.schedules[name]
	if !ok {
		return ""
	}
	th := m.th
	if ms, missing := m.missed[name]; missing {
		return th.S.Danger.Render("⚠ missed " + ms.ExpectedAt.Format("Mon 15:04"))
	}
	label := "↻ " + expr
	if next, ok := cron.Next(expr, missed.NaiveNow(m.now())); ok {
		label += " · next in " + format.RelativeTime(next.Sub(missed.NaiveNow(m.now())).Seconds())
	}
	return th.S.Info.Render(label)
}
