package tui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// History is the forensics view (design §4.3): every run, newest first, full
// width. It is a custom rowsview rather than bubbles/table (architecture doc:
// "bubbles/table can't render per-cell heat-colored sparklines") — a viewport,
// a selection index and one row-render func, same shape as fleetModel's table.
//
// Enter does NOT swap modes (design §4): it opens the run's log as a preview
// pane split under the table, so the table stays on screen the whole time.
const (
	histWhenW   = 11 // "MM-DD HH:MM"
	histAgeW    = 4
	histStatusW = 9 // glyph + gap + 7-wide word
	histDurW    = 8
	histCPUW    = 5
	histMemW    = 7
	histTrigW   = 7

	// previewMinRows is the floor on BOTH halves of the table/preview split
	// (view() below): neither one is ever squeezed to nothing.
	previewMinRows = 4
)

type historyModel struct {
	w, h int
	sel  int
	top  int

	// loaded is every row app.Hist returned for the view's window
	// (config.historyDays, falling back to the last 200 — Open-TuiHistory's
	// own rule), oldest first exactly as the store returns it.
	loaded []history.Row

	preview *historyPreview
}

// historyPreview is the log tail opened by Enter (§4.3: a split pane, not a
// mode swap).
type historyPreview struct {
	script string
	path   string
	lines  []string
}

func (h *historyModel) init(*Model)                {}
func (h *historyModel) resize(_ *Model, w, hh int) { h.w, h.h = w, hh }

func (h *historyModel) pageSize() int { return max(h.h-2, 1) }

func (h *historyModel) clampSel(n int) {
	h.sel = min(max(h.sel, 0), max(n-1, 0))
}

// filteredRows is every loaded row honoring the current scope, newest first —
// h.loaded arrives oldest-first (the store's own order), so this walks it
// backwards rather than sorting.
func (h *historyModel) filteredRows(m *Model) []history.Row {
	var out []history.Row
	for i := len(h.loaded) - 1; i >= 0; i-- {
		r := h.loaded[i]
		if m.historyScope == "" || r.Script == m.historyScope {
			out = append(out, r)
		}
	}
	return out
}

// onLoaded stores a fresh read (root.go's HistoryLoadedMsg case) and clamps
// the selection, which a shorter reload can otherwise leave out of range.
func (h *historyModel) onLoaded(m *Model, msg HistoryLoadedMsg) {
	h.loaded = msg.Rows
	h.clampSel(len(h.filteredRows(m)))
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (h *historyModel) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return h.onKey(m, msg)
	case tea.MouseClickMsg:
		return h.onClick(m, msg.Mouse())
	case tea.MouseWheelMsg:
		return h.onWheel(m, msg.Mouse())
	}
	return nil
}

func (h *historyModel) onKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	rows := h.filteredRows(m)
	n := len(rows)
	switch {
	case key.Matches(msg, k.Close):
		if h.preview != nil {
			h.preview = nil
			return status(StatusInfo, "preview closed")
		}
	case key.Matches(msg, k.Up):
		h.sel = max(h.sel-1, 0)
	case key.Matches(msg, k.Down):
		h.sel = min(h.sel+1, max(n-1, 0))
	case key.Matches(msg, k.PageUp):
		h.sel = max(h.sel-h.pageSize(), 0)
	case key.Matches(msg, k.PageDown):
		h.sel = min(h.sel+h.pageSize(), max(n-1, 0))
	case key.Matches(msg, k.Top):
		h.sel = 0
	case key.Matches(msg, k.Bottom):
		h.sel = max(n-1, 0)
	case key.Matches(msg, k.Open):
		return h.openPreview(m, rows)
	case key.Matches(msg, k.FailFilter):
		return h.toggleScope(m, rows)
	case key.Matches(msg, k.Start):
		return h.rerun(m, rows)
	}
	return nil
}

func (h *historyModel) onClick(m *Model, mouse tea.Mouse) tea.Cmd {
	row := mouse.Y - headerRows - 1 // row 0 of the body is the table's title rule
	if row < 0 {
		return nil
	}
	rows := h.filteredRows(m)
	if idx := h.top + row; idx < len(rows) {
		h.sel = idx
	}
	return nil
}

func (h *historyModel) onWheel(m *Model, mouse tea.Mouse) tea.Cmd {
	n := len(h.filteredRows(m))
	if mouse.Button == tea.MouseWheelUp {
		h.sel = max(h.sel-3, 0)
	} else {
		h.sel = min(h.sel+3, max(n-1, 0))
	}
	return nil
}

// toggleScope is `f`: all runs <-> just the selected row's script — the same
// field the Run view's `h` deep-link sets, so the two agree about "the
// current scope".
func (h *historyModel) toggleScope(m *Model, rows []history.Row) tea.Cmd {
	if m.historyScope != "" {
		m.historyScope = ""
		h.sel = 0
		return status(StatusInfo, "showing every script — f scopes to one")
	}
	if h.sel < 0 || h.sel >= len(rows) {
		return status(StatusWarn, "no run selected")
	}
	m.historyScope = rows[h.sel].Script
	h.sel = 0
	return status(StatusInfo, "scoped to "+m.historyScope+" — f shows everything")
}

// rerun is `r`: the selected row's script, through the normal run flow
// (queue if busy, dependency check otherwise) — Start-TuiRunFlow, same as
// every other re-run entry point in the app.
func (h *historyModel) rerun(m *Model, rows []history.Row) tea.Cmd {
	if h.sel < 0 || h.sel >= len(rows) {
		return status(StatusWarn, "no run selected")
	}
	name := rows[h.sel].Script
	for i := range m.scripts {
		if m.scripts[i].Name == name {
			return m.run.start(m, m.scripts[i])
		}
	}
	return status(StatusErr, "script '"+name+"' not found — removed from the repo?")
}

// openPreview is Enter: read the row's log tail into the split preview pane.
// LogTail is a bounded local read (config.logTailKb) — the same class of
// synchronous file read newEnvEditor already does inline — so this needs no
// round trip through a tea.Cmd.
func (h *historyModel) openPreview(m *Model, rows []history.Row) tea.Cmd {
	if h.sel < 0 || h.sel >= len(rows) {
		return status(StatusWarn, "no run to preview")
	}
	row := rows[h.sel]
	if row.LogFile == nil || *row.LogFile == "" {
		return status(StatusWarn, "no log for this run")
	}
	path := *row.LogFile
	text := history.LogTail(path, m.app.Cfg.LogTailKb)
	var lines []string
	if text != "" {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		lines = strings.Split(strings.TrimRight(text, "\n"), "\n")
	}
	h.preview = &historyPreview{script: row.Script, path: path, lines: lines}
	return nil
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (h *historyModel) view(m *Model, w, hh int) []string {
	rows := h.filteredRows(m)
	h.clampSel(len(rows))
	if paneled(w) {
		return h.viewPaneled(m, rows, w, hh)
	}

	if h.preview == nil {
		return fitRows(h.table(m, rows, w, hh), hh)
	}
	pv, tableH := h.split(hh)
	out := fitRows(h.table(m, rows, w, tableH), tableH)
	out = append(out, h.previewRows(m, w, pv)...)
	return fitRows(out, hh)
}

// split divides the body between table and preview: roughly in half, then
// favoring the table's floor over the preview's if the frame is too short to
// give both (the too-small guard at 40x10 keeps this from ever needing to go
// below "something on each side").
func (h *historyModel) split(hh int) (pv, tableH int) {
	pv = max(hh/2, previewMinRows)
	if tableFloor := hh - previewMinRows; pv > tableFloor {
		pv = max(tableFloor, 1)
	}
	return pv, hh - pv
}

// viewPaneled is the History frame at and above panelMinWidth: the table in
// a rounded panel, the log preview in its own panel under it.
func (h *historyModel) viewPaneled(m *Model, rows []history.Row, w, hh int) []string {
	th := m.th
	pad := panelPad(w)
	pv, tableH := 0, hh
	if h.preview != nil {
		pv, tableH = h.split(hh)
	}
	out := renderPanel(th, h.tableRows(m, rows, w-2-2*pad, tableH-2), w, tableH,
		panelOpts{title: h.title(m, rows), focused: true, pad: pad,
			hints: m.tailHints(modeHistory, focusList)})
	if h.preview != nil {
		out = append(out, renderPanel(th, h.previewBody(m, w-2-2*pad, pv-2),
			w, pv, panelOpts{title: "log: " + h.preview.script, pad: pad})...)
	}
	return out
}

func (h *historyModel) title(m *Model, rows []history.Row) string {
	if m.historyScope != "" {
		return "history — " + m.historyScope
	}
	return "history"
}

func (h *historyModel) emptyNote(m *Model) string {
	if m.historyScope != "" {
		return "no runs for " + m.historyScope + " — f shows everything"
	}
	return "no runs yet"
}

// table is the floor path: a title rule over tableRows.
func (h *historyModel) table(m *Model, rows []history.Row, w, hh int) []string {
	return append([]string{sectionRule(m.th, h.title(m, rows), w, true)},
		h.tableRows(m, rows, w, hh-1)...)
}

// tableRows is the table's content: hh rows, windowed on the selection.
func (h *historyModel) tableRows(m *Model, rows []history.Row, w, hh int) []string {
	if len(rows) == 0 {
		return []string{" " + m.th.S.Muted.Render(h.emptyNote(m))}
	}
	h.top = scrollWindow(h.top, h.sel, len(rows), max(hh, 1))

	wide := w >= wideTableMin
	nameW := 8
	for _, r := range rows {
		nameW = max(nameW, textkit.Width(r.Script))
	}
	nameW = min(nameW, nameColMax)

	var out []string
	for i := h.top; i < len(rows) && len(out) < hh; i++ {
		out = append(out, h.row(m, rows[i], i == h.sel, wide, w, nameW))
	}
	return out
}

// row renders when/age/status/script/duration/cpu-peak/heat-sparkline/mem/
// trigger (design §4.3's column list). At the 80-column floor age, cpu-peak,
// the sparkline, mem and trigger drop out (§4's collapse rule leaves
// when/status/script/duration).
func (h *historyModel) row(m *Model, r history.Row, selected, wide bool, w, nameW int) string {
	th := m.th
	var bg color.Color
	bar := " "
	if selected {
		bg = th.C.SelBg
		bar = tint(th.S.Primary, bg).Render("▎")
	}
	gap := tint(th.S.Base, bg).Render(" ")

	started := r.StartedAt.Time().Local()
	when, age := "—", "—"
	if !started.IsZero() {
		when = started.Format("01-02 15:04")
		age = format.RelativeTime(m.now().Sub(started).Seconds())
	}

	stStyle := th.S.Warning
	switch r.Status {
	case "success":
		stStyle = th.S.Success
	case "failure":
		stStyle = th.S.Danger
	}
	statusWord := r.Status
	if statusWord == "" {
		statusWord = "?"
	}

	dur := "—"
	if r.DurationSec != nil {
		dur = format.Duration(*r.DurationSec)
	}
	cpu, mem := "—", "—"
	var series []float64
	if r.Resources != nil {
		cpu = trim1(r.Resources.CPUMaxPercent) + "%"
		mem = trim1(r.Resources.MemMaxMb) + "MB"
		series = r.Resources.CPUSeries
	}
	trigger := r.Trigger
	if trigger == "" {
		trigger = "—"
	}

	var b strings.Builder
	b.WriteString(bar)
	b.WriteString(gap)
	b.WriteString(tint(th.S.Desc, bg).Render(textkit.Fit(when, histWhenW)))
	if wide {
		b.WriteString(gap)
		b.WriteString(tint(th.S.Muted, bg).Render(right(age, histAgeW)))
	}
	b.WriteString(gap)
	b.WriteString(tint(stStyle, bg).Render(statusGlyph(r.Status)))
	b.WriteString(gap)
	b.WriteString(tint(stStyle, bg).Render(textkit.Fit(statusWord, histStatusW-2)))
	b.WriteString(gap)
	b.WriteString(tint(th.S.Base, bg).Render(textkit.Fit(r.Script, nameW)))
	b.WriteString(gap)
	b.WriteString(tint(th.S.Desc, bg).Render(right(dur, histDurW)))
	if wide {
		b.WriteString(gap)
		b.WriteString(tint(th.S.Muted, bg).Render(right(cpu, histCPUW)))
		b.WriteString(gap)
		b.WriteString(sparkline(th, series, sparkCells, bg))
		b.WriteString(gap)
		b.WriteString(tint(th.S.Muted, bg).Render(right(mem, histMemW)))
		b.WriteString(gap)
		b.WriteString(tint(th.S.Muted, bg).Render(textkit.Fit(trigger, histTrigW)))
	}
	return fillTo(b.String(), w, bg)
}

// previewRows is the floor's split log pane: a title rule naming the run —
// the script name only, never the path: a run log's real path is a temp or
// per-host directory, which would make the frame environment-dependent (the
// same class of bug P10's golden-authenticity rule exists to catch).
func (h *historyModel) previewRows(m *Model, w, hh int) []string {
	return fitRows(append([]string{sectionRule(m.th, "log: "+h.preview.script, w, false)},
		h.previewBody(m, w, hh-1)...), hh)
}

// previewBody is the log tail itself, colored the same way the output pane
// colors run output.
func (h *historyModel) previewBody(m *Model, w, hh int) []string {
	th := m.th
	pv := h.preview
	if len(pv.lines) == 0 {
		return []string{" " + th.S.Muted.Render("no output")}
	}
	var rows []string
	start := max(len(pv.lines)-max(hh, 0), 0)
	for _, l := range pv.lines[start:] {
		rows = append(rows, fillTo(colorLine(th, textkit.Truncate(l, w)), w, nil))
	}
	return rows
}
