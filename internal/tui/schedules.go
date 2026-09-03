package tui

import (
	"errors"
	"image/color"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/openrouter"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// Schedules is the agenda view (design §4.4): every script, scheduled ones
// sorted by next fire, unscheduled ones after — a schedule is addable from
// right here, not a separate screen. e/Enter opens the edit prompt; the
// expression can be typed literally or in plain English (cron.ToCron routes
// anything that doesn't already validate through the OpenRouter client).
//
// Nothing here writes the crontab synchronously: cron.ToCron's AI path is a
// network call and app.Cron.Set/Remove shells out to the crontab binary, so
// both are tea.Cmds, and a write always asks first except a removal (empty
// input), which the floor never confirmed either.
const schedExprW = 14

type scheduleRow struct {
	Name string
	Expr string // "" = unscheduled (addable via e/Enter)

	Next    time.Time // valid only when HasNext
	HasNext bool
	Missed  *missed.Miss
}

type schedulesModel struct {
	w, h int
	sel  int
	top  int
}

func (s *schedulesModel) init(*Model)                {}
func (s *schedulesModel) resize(_ *Model, w, hh int) { s.w, s.h = w, hh }
func (s *schedulesModel) pageSize() int              { return max(s.h-2, 1) }

// rows is every script, scheduled ones first (soonest fire first — the same
// engine and naive-local labeling the Fleet agenda and the missed detector
// use), unscheduled ones after, alphabetically.
func (s *schedulesModel) rows(m *Model) []scheduleRow {
	now := missed.NaiveNow(m.now())
	var scheduled, unscheduled []scheduleRow
	for _, sc := range m.scripts {
		expr, ok := m.schedules[sc.Name]
		if !ok {
			unscheduled = append(unscheduled, scheduleRow{Name: sc.Name})
			continue
		}
		row := scheduleRow{Name: sc.Name, Expr: expr}
		if next, ok := cron.Next(expr, now); ok {
			row.Next, row.HasNext = next, true
		}
		if ms, isMissed := m.missed[sc.Name]; isMissed {
			row.Missed = &ms
		}
		scheduled = append(scheduled, row)
	}
	sort.Slice(scheduled, func(i, j int) bool {
		a, b := scheduled[i], scheduled[j]
		if a.HasNext != b.HasNext {
			return a.HasNext // a next fire beats one cron.Next could not resolve
		}
		if a.HasNext && !a.Next.Equal(b.Next) {
			return a.Next.Before(b.Next)
		}
		return a.Name < b.Name
	})
	sort.Slice(unscheduled, func(i, j int) bool { return unscheduled[i].Name < unscheduled[j].Name })
	return append(scheduled, unscheduled...)
}

func (s *schedulesModel) clampSel(n int) {
	s.sel = min(max(s.sel, 0), max(n-1, 0))
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (s *schedulesModel) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.onKey(m, msg)
	case tea.MouseClickMsg:
		return s.onClick(m, msg.Mouse())
	case tea.MouseWheelMsg:
		return s.onWheel(m, msg.Mouse())
	}
	return nil
}

func (s *schedulesModel) onKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	rows := s.rows(m)
	n := len(rows)
	switch {
	case key.Matches(msg, k.Up):
		s.sel = max(s.sel-1, 0)
	case key.Matches(msg, k.Down):
		s.sel = min(s.sel+1, max(n-1, 0))
	case key.Matches(msg, k.PageUp):
		s.sel = max(s.sel-s.pageSize(), 0)
	case key.Matches(msg, k.PageDown):
		s.sel = min(s.sel+s.pageSize(), max(n-1, 0))
	case key.Matches(msg, k.Top):
		s.sel = 0
	case key.Matches(msg, k.Bottom):
		s.sel = max(n-1, 0)
	case key.Matches(msg, k.ScheduleEdit):
		return s.edit(m, rows)
	}
	return nil
}

func (s *schedulesModel) onClick(m *Model, mouse tea.Mouse) tea.Cmd {
	row := mouse.Y - headerRows - 1
	if row < 0 {
		return nil
	}
	if idx := s.top + row; idx < len(s.rows(m)) {
		s.sel = idx
	}
	return nil
}

func (s *schedulesModel) onWheel(m *Model, mouse tea.Mouse) tea.Cmd {
	n := len(s.rows(m))
	if mouse.Button == tea.MouseWheelUp {
		s.sel = max(s.sel-3, 0)
	} else {
		s.sel = min(s.sel+3, max(n-1, 0))
	}
	return nil
}

// edit is e/Enter: the schedule prompt, pre-filled with the row's current
// expression (empty for an unscheduled script — the same prompt adds one).
func (s *schedulesModel) edit(m *Model, rows []scheduleRow) tea.Cmd {
	if s.sel < 0 || s.sel >= len(rows) {
		return status(StatusWarn, "no script selected")
	}
	row := rows[s.sel]
	name := row.Name
	m.open(newInput(m, inputSchedule,
		"schedule for "+name+" (cron / @daily / plain English; empty = remove)", row.Expr,
		func(m *Model, value string) tea.Cmd {
			if strings.TrimSpace(value) == "" {
				// floor semantics: empty removes WITHOUT confirmation
				return scheduleRemoveCmd(m.app.Cron, name)
			}
			return parseCronCmd(m, name, value)
		}))
	return nil
}

// ---------------------------------------------------------------------------
// The write path: parse -> validate -> confirm -> write
// ---------------------------------------------------------------------------

// parseCronCmd routes text through cron.ToCron off the update loop — the AI
// path is a network call, so this must be a tea.Cmd even for the literal
// path (a caller cannot know which one it will take without running it).
func parseCronCmd(m *Model, name, text string) tea.Cmd {
	ai := m.cronAI()
	return func() tea.Msg {
		conv := cron.ToCron(text, ai)
		var err error
		if conv.Err != "" {
			err = errors.New(conv.Err)
		}
		return CronParsedMsg{Script: name, Expr: conv.Expression, Err: err}
	}
}

// onCronParsed is controller ruling 4 (validate before write) plus the
// ALWAYS-confirm rule: a successful parse never writes on its own.
func (m *Model) onCronParsed(msg CronParsedMsg) tea.Cmd {
	if msg.Err != nil {
		return status(StatusErr, "cron: "+msg.Err.Error())
	}
	if !cron.Validate(msg.Expr) {
		return status(StatusErr, "cron: not a valid expression")
	}
	m.open(confirmPrompt("schedule '"+msg.Script+"' as: "+msg.Expr+" ?",
		scheduleSetCmd(m.app.Cron, msg.Script, msg.Expr)))
	return nil
}

// onScheduleSaved reports a Set or Remove — "failed to update crontab" is
// PS's own status text (Open-TuiCronInput), on purpose: the specific reason
// (a wipe-guard refusal or a write failure) is not a distinction a user needs.
func (m *Model) onScheduleSaved(msg ScheduleSavedMsg) tea.Cmd {
	if msg.Err != nil {
		return status(StatusErr, "failed to update crontab")
	}
	verb := "scheduled " + msg.Name + " : " + msg.Expr
	if msg.Removed {
		verb = "schedule removed for " + msg.Name
	}
	return tea.Batch(m.loadFleet(), status(StatusOK, verb))
}

func scheduleSetCmd(ct *cron.Crontab, name, expr string) tea.Cmd {
	return func() tea.Msg {
		return ScheduleSavedMsg{Name: name, Expr: expr, Err: ct.Set(name, expr)}
	}
}

func scheduleRemoveCmd(ct *cron.Crontab, name string) tea.Cmd {
	return func() tea.Msg {
		return ScheduleSavedMsg{Name: name, Removed: true, Err: ct.Remove(name)}
	}
}

// cronAI resolves the function cron.ToCron needs for its natural-language
// path: a test's injected hook when one is set, else a real client keyed off
// OPENROUTER_API_KEY — nil when that is unset, which is what makes ToCron
// return the PS app's own "not set" error rather than trying a request with
// no key.
func (m *Model) cronAI() func(string) (string, error) {
	if m.aiConvert != nil {
		return m.aiConvert
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		return nil
	}
	return openrouter.New(apiKey, m.app.Cfg.OpenRouterModel).Convert
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (s *schedulesModel) view(m *Model, w, hh int) []string {
	rows := s.rows(m)
	s.clampSel(len(rows))
	if paneled(w) {
		pad := panelPad(w)
		return renderPanel(m.th, s.tableRows(m, rows, w-2-2*pad, hh-2), w, hh,
			panelOpts{title: "schedules", focused: true, pad: pad,
				hints: m.tailHints(modeSchedules, focusList)})
	}
	return fitRows(s.table(m, rows, w, hh), hh)
}

// table is the floor path: a title rule over tableRows.
func (s *schedulesModel) table(m *Model, rows []scheduleRow, w, hh int) []string {
	return append([]string{sectionRule(m.th, "schedules", w, true)},
		s.tableRows(m, rows, w, hh-1)...)
}

// tableRows is the table's content: hh rows, windowed on the selection.
func (s *schedulesModel) tableRows(m *Model, rows []scheduleRow, w, hh int) []string {
	if len(rows) == 0 {
		return []string{" " + m.th.S.Muted.Render("no scripts yet — press s to sync a repo")}
	}
	s.top = scrollWindow(s.top, s.sel, len(rows), max(hh, 1))

	nameW := 8
	for _, r := range rows {
		nameW = max(nameW, textkit.Width(r.Name))
	}
	nameW = min(nameW, nameColMax)

	var out []string
	for i := s.top; i < len(rows) && len(out) < hh; i++ {
		out = append(out, s.row(m, rows[i], i == s.sel, w, nameW))
	}
	return out
}

// row renders script | cron expr | next-run countdown / missed status /
// "addable" note (design §4.4's column list).
func (s *schedulesModel) row(m *Model, r scheduleRow, selected bool, w, nameW int) string {
	th := m.th
	var bg color.Color
	bar := " "
	if selected {
		bg = th.C.SelBg
		bar = tint(th.S.Primary, bg).Render("▎")
	}
	gap := tint(th.S.Base, bg).Render(" ")

	expr := "—"
	if r.Expr != "" {
		expr = r.Expr
	}

	note, st := "never fires", th.S.Muted
	switch {
	case r.Expr == "":
		note, st = "not scheduled — e adds one", th.S.Muted
	case r.Missed != nil:
		note, st = "⚠ missed "+r.Missed.ExpectedAt.Format("Mon 15:04"), th.S.Danger
	case r.HasNext:
		note, st = "next in "+format.RelativeTime(r.Next.Sub(missed.NaiveNow(m.now())).Seconds()), th.S.Info
	}

	fixed := 1 + 1 + nameW + 1 + schedExprW + 1
	noteW := max(w-fixed, 8)

	var b strings.Builder
	b.WriteString(bar)
	b.WriteString(gap)
	b.WriteString(tint(th.S.Base, bg).Render(textkit.Fit(r.Name, nameW)))
	b.WriteString(gap)
	b.WriteString(tint(th.S.Desc, bg).Render(textkit.Fit(expr, schedExprW)))
	b.WriteString(gap)
	b.WriteString(tint(st, bg).Render(textkit.Truncate(note, noteW)))
	return fillTo(b.String(), w, bg)
}
