package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/envfile"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/procstat"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// Run is the working view (design §4.2): pick a script on the left, watch it
// on the right. The details card under the list answers "what am I about to
// run" without leaving the screen; the status bar answers "how much longer".
const (
	// detailsRows is the card's height including its rule. The PS app hides it
	// below 14 body rows and so does this: at that size the output pane needs
	// every row more than the card does (inventory §1.12).
	detailsRows    = 8
	detailsMinBody = 14

	listMinWidth = 24
	listMaxWidth = 44

	// etaBarWidth is the ETA bar in the status line — enough cells to read a
	// proportion at a glance, cheap enough to sit beside the text.
	etaBarWidth = 12
)

type runModel struct {
	w, h int

	list list.Model
	out  outputPane
	prog progress.Model

	// live run
	handle     *runner.Handle
	startedAt  time.Time
	etaSec     float64
	doneRow    *history.Row
	lastSample procstat.Sample

	queue []queued

	// the live background task (sync / lint / install / update), if any
	task *task

	// marquee state for the selected row's name (marquee.go)
	marqueeSel int
	marqueeAt  time.Time
	marqueeOn  bool

	// filter is the live script-list substring filter (`/`, floor item):
	// applied on every keystroke, esc restores it. scriptsLen is the last
	// m.scripts length reload() saw — resize()'s dirty check compares
	// against THAT rather than len(r.list.Items()), which a filter makes
	// smaller than m.scripts on purpose.
	filter     string
	scriptsLen int

	// searchTerm is the output pane's search term (ctrl+f, n/N, floor item):
	// recomputed against the CURRENT buffer on every jump, exactly like
	// PS's Move-TuiSearch, so freshly arrived output is always searchable.
	searchTerm string
}

func (r *runModel) init(m *Model) {
	r.list = newScriptList(m)
	r.prog = progress.New(progress.WithWidth(etaBarWidth), progress.WithoutPercentage())
	r.out.reset("output", m.app.Cfg.MaxOutputLines)

	// An empty pane on first open says nothing, and config.json's complaints
	// have to land somewhere a user will see — the PS app prints them into this
	// same panel. Plain lines, so the wrap cache re-derives them correctly once
	// the real width arrives (the version lives in the header chip).
	r.out.append("▸ scriptorium")
	for _, w := range m.app.Warnings {
		r.out.append("⚠ " + w)
	}
	r.out.append("", "  r run · s sync · tab focus · 1-4 views", "")
}

// applyTheme re-derives everything this view keeps a COPY of from the theme.
// It is called from Model.useTheme, never from init, so the ETA bar follows the
// injected profile rather than whatever the process environment happened to say
// when the model was built.
//
// The ETA bar is a solid Primary fill over a Muted empty half (v1.0.1 task 2).
// The released gradient did two wrong things at once: it painted background
// colors across the status line (bubbles/progress's half-block mode pairs a
// bg with every fg step — the "teal gradient bg" defect), and below truecolor
// it emitted interpolated 24-bit SGRs the profile never saw. Solid full-block
// fill has neither problem, on any profile.
//
// The nil check is not defensive padding: under a no-colour profile (NO_COLOR,
// TERM=dumb, an explicit colorMode) every token resolves to nil — correctly,
// that is what "no colour" means — and the bar should render unstyled blocks.
func (r *runModel) applyTheme(th theme.Theme) {
	r.prog.SetWidth(etaBarWidth)
	progress.WithFillCharacters('█', '░')(&r.prog)

	if th.C.Primary == nil {
		// WithColors() alone would fall back to the component's OWN default
		// palette — a 24-bit fill, on a terminal that just said it wants
		// none. Clearing the fields as well leaves the bar unstyled, which is
		// what a no-colour profile is asking for.
		progress.WithColors()(&r.prog)
		r.prog.FullColor, r.prog.EmptyColor = nil, nil
		return
	}
	progress.WithColors(th.C.Primary)(&r.prog)
	r.prog.EmptyColor = th.C.Muted
}

// initCmd is the root's startup hook for this view. Nothing to schedule yet —
// the first data load already comes from the root.
func (r *runModel) initCmd() tea.Cmd { return nil }

func (r *runModel) reload(m *Model) {
	name := ""
	if it, ok := r.list.SelectedItem().(scriptItem); ok {
		name = it.s.Name
	}
	r.scriptsLen = len(m.scripts)
	r.list.SetItems(scriptItems(r.visibleScripts(m)))
	if name != "" {
		r.selectByName(m, name)
	}
}

// visibleScripts is m.scripts narrowed by the live filter — substring, not a
// wildcard pattern, case-insensitive (Update-TuiVisible's own rule: a filter
// is text a user typed, never glob syntax).
func (r *runModel) visibleScripts(m *Model) []scripts.Script {
	if r.filter == "" {
		return m.scripts
	}
	q := strings.ToLower(r.filter)
	out := make([]scripts.Script, 0, len(m.scripts))
	for _, s := range m.scripts {
		if strings.Contains(strings.ToLower(s.Name), q) {
			out = append(out, s)
		}
	}
	return out
}

func (r *runModel) selected(m *Model) *scripts.Script {
	it, ok := r.list.SelectedItem().(scriptItem)
	if !ok {
		return nil
	}
	for i := range m.scripts {
		if m.scripts[i].Name == it.s.Name {
			return &m.scripts[i]
		}
	}
	return &it.s
}

// selectByName is the Fleet deep-link's landing point. It indexes into the
// list's OWN (possibly filtered) items, not m.scripts directly — the two can
// differ in both length and order once `/` has narrowed the list.
func (r *runModel) selectByName(m *Model, name string) {
	for i, s := range r.visibleScripts(m) {
		if s.Name == name {
			r.list.Select(i)
			return
		}
	}
}

func (r *runModel) active() bool    { return r.handle != nil || r.task != nil }
func (r *runModel) queueDepth() int { return len(r.queue) }

func (r *runModel) isRunning(n string) bool { return r.handle != nil && r.handle.Name == n }

func (r *runModel) isQueued(name string) bool {
	for _, q := range r.queue {
		if q.Name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (r *runModel) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case RunStartedMsg:
		return r.onRunStarted(m, msg)
	case RunQueuedMsg:
		return r.onRunQueued(msg)
	case RunEventsMsg:
		return r.onRunEvents(m, msg)
	case RunDoneMsg:
		return r.onRunDone(m, msg)
	case TaskEventsMsg:
		return r.onTaskEvents(m, msg)
	case DepsScannedMsg:
		return r.onDepsScanned(m, msg)
	case LogLoadedMsg:
		return r.onLogLoaded(m, msg)
	case tea.KeyPressMsg:
		return r.onKey(m, msg)
	case tea.MouseClickMsg:
		return r.onClick(m, msg.Mouse())
	case tea.MouseMotionMsg:
		return r.onDrag(m, msg.Mouse())
	case tea.MouseReleaseMsg:
		return r.onRelease(m, msg.Mouse())
	case tea.MouseWheelMsg:
		return r.onWheel(m, msg.Mouse())
	case ClipboardMsg:
		return m.onClipboard(msg)
	}
	return nil
}

// outputCell maps a mouse position to a position in the output BUFFER, or
// false when the pointer is not over the output pane's content. Body row 0 is
// the pane's title rule; the right pane starts one column past the separator.
func (r *runModel) outputCell(m *Model, mouse tea.Mouse) (row, col int, ok bool) {
	lay := runLayoutFor(m.w, m.bodyHeight())
	body := mouse.Y - headerRows
	if body < 1 || body >= m.bodyHeight() || mouse.X <= lay.listW {
		return 0, 0, false
	}
	return r.out.scroll + body - 1, mouse.X - lay.listW - 1, true
}

// onClick focuses the pane under the pointer and, in the list, selects the row
// it landed on. In the output pane it also arms a drag: the press is the
// anchor, and whether it becomes a selection or a plain click is decided on
// release.
func (r *runModel) onClick(m *Model, mouse tea.Mouse) tea.Cmd {
	row := mouse.Y - headerRows
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	if mouse.X >= runLayoutFor(m.w, m.bodyHeight()).listW {
		m.focus = focusOutput
		r.out.clearSelection()
		if br, bc, ok := r.outputCell(m, mouse); ok && mouse.Button == tea.MouseLeft {
			r.out.beginDrag(br, bc)
		}
		return nil
	}
	m.focus = focusList
	r.out.clearSelection()
	// row 0 is the title rule; the list windows by page, so the first visible
	// item is the page offset
	if idx := r.firstVisible() + row - 1; row > 0 && idx < len(r.list.Items()) {
		r.list.Select(idx)
	}
	return nil
}

// onDrag extends a live selection (inventory §1.11: motion with button 0 held
// and an anchor recorded).
func (r *runModel) onDrag(m *Model, mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft || r.out.anchor == nil {
		return nil
	}
	if row, col, ok := r.outputCell(m, mouse); ok {
		r.out.dragTo(row, col)
	}
	return nil
}

// onRelease ends the gesture: a drag copies its text, a plain click checks the
// word under the pointer for a device-login code and copies THAT (§1.11's
// click-to-copy). Anything else just drops the anchor.
func (r *runModel) onRelease(m *Model, mouse tea.Mouse) tea.Cmd {
	defer r.out.clearSelection()
	if r.out.selecting() {
		if text := r.out.selectedText(); strings.TrimSpace(text) != "" {
			return m.copyToClipboard(text)
		}
		return nil
	}
	if r.out.anchor == nil {
		return nil
	}
	row, col, ok := r.outputCell(m, mouse)
	if !ok {
		return nil
	}
	if word := r.out.wordAt(row, col); deviceCodeRE.MatchString(word) {
		return m.copyToClipboard(word)
	}
	return nil
}

// onWheel scrolls whichever pane the pointer is over, three rows at a time
// (inventory §1.11).
func (r *runModel) onWheel(m *Model, mouse tea.Mouse) tea.Cmd {
	delta := 3
	if mouse.Button == tea.MouseWheelUp {
		delta = -3
	}
	if mouse.X >= runLayoutFor(m.w, m.bodyHeight()).listW {
		r.out.scrollBy(delta)
		return nil
	}
	for range 3 {
		if delta < 0 {
			r.list.CursorUp()
		} else {
			r.list.CursorDown()
		}
	}
	return nil
}

// firstVisible is the index of the list's topmost rendered item. bubbles/list
// windows by page rather than by offset, so this is the page origin.
func (r *runModel) firstVisible() int {
	return r.list.Paginator.Page * r.list.Paginator.PerPage
}

func (r *runModel) onKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	switch {
	case key.Matches(msg, k.Focus):
		if m.focus == focusList {
			m.focus = focusOutput
			return status(StatusInfo, "focus: output — ↑↓ scroll, end follows")
		}
		m.focus = focusList
		return status(StatusInfo, "focus: scripts")

	case key.Matches(msg, k.Start):
		s := r.selected(m)
		if s == nil {
			return status(StatusWarn, "no script selected")
		}
		return r.start(m, *s)

	case key.Matches(msg, k.Args):
		return r.args(m)

	case key.Matches(msg, k.Env):
		return r.editEnv(m)

	case key.Matches(msg, k.Deps):
		return r.depScan(m)

	case key.Matches(msg, k.Lint):
		return r.lint(m)

	case key.Matches(msg, k.Upgrade):
		return r.systemUpdate(m)

	case key.Matches(msg, k.ViewLog):
		return r.viewLog(m)

	case key.Matches(msg, k.Copy):
		// the WHOLE retained buffer (Invoke-TuiCopy), out of the redacted
		// store — every line in there passed the runner's or the task's
		// redaction on the way in, so a copy cannot leak what the pane never
		// showed. The 72 KB OSC 52 cap in the clipboard stack exists for
		// exactly this case.
		return m.copyToClipboard(r.out.allText())

	case key.Matches(msg, k.ClearOut):
		r.out.clear()
		return status(StatusInfo, "output cleared")

	case key.Matches(msg, k.Scoped):
		return r.openHistory(m)

	case key.Matches(msg, k.Kill):
		return r.kill(m)

	case key.Matches(msg, k.ClearQueue):
		return r.clearQueue()

	case key.Matches(msg, k.Sync):
		return r.sync(m)

	case key.Matches(msg, k.Follow):
		r.out.toBottom()
		return nil

	case key.Matches(msg, k.Filter):
		return r.startFilter(m)

	case key.Matches(msg, k.SearchOutput):
		return r.startSearch(m)

	case key.Matches(msg, k.SelfUpdate):
		return r.selfUpdate(m)

	case key.Matches(msg, k.WebhookTest):
		return r.webhookTest(m)

	// n/N: search next/prev. Raw keypress match, not a keyMap field — see
	// keys.go's comment by SearchOutput for why.
	case msg.Text == "n" && msg.Mod == 0:
		return r.searchJump(1)

	case msg.Text == "N" && msg.Mod == 0:
		return r.searchJump(-1)
	}

	// Only the NAV keys belong to the focused pane; every action above stays
	// live in both, exactly as the PS list mode behaves (inventory §1.4).
	if m.focus == focusOutput {
		switch {
		case key.Matches(msg, k.Up):
			r.out.scrollBy(-1)
		case key.Matches(msg, k.Down):
			r.out.scrollBy(1)
		case key.Matches(msg, k.PageUp):
			r.out.scrollBy(-r.out.rows())
		case key.Matches(msg, k.PageDown):
			r.out.scrollBy(r.out.rows())
		case key.Matches(msg, k.Top):
			r.out.toTop()
		case key.Matches(msg, k.Bottom):
			r.out.toBottom()
		}
		return nil
	}
	var cmd tea.Cmd
	r.list, cmd = r.list.Update(msg)
	return cmd
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

type runLayout struct {
	listW, outW  int
	listH, cardH int
}

func runLayoutFor(w, h int) runLayout {
	l := runLayout{listW: min(max(w/3, listMinWidth), listMaxWidth)}
	l.outW = max(w-l.listW-1, 10)
	if h >= detailsMinBody {
		l.cardH = detailsRows
	}
	l.listH = max(h-l.cardH, 1)
	return l
}

func (r *runModel) resize(m *Model, w, h int) {
	r.w, r.h = w, h
	l := runLayoutFor(w, h)
	r.list.SetWidth(l.listW)
	r.list.SetHeight(max(l.listH-1, 1)) // the title rule takes one row
	r.out.resize(l.outW, h)
	if r.scriptsLen != len(m.scripts) {
		r.reload(m)
	}
}

func (r *runModel) view(m *Model, w, h int) []string {
	r.resize(m, w, h)
	r.noteSelection(m)
	l := runLayoutFor(w, h)
	th := m.th

	left := []string{sectionRule(th, r.listTitle(), l.listW, m.focus == focusList)}
	switch {
	case len(m.scripts) == 0:
		left = append(left, " "+th.S.Muted.Render("no scripts yet — press s to sync"))
	case len(r.list.Items()) == 0:
		left = append(left, " "+th.S.Muted.Render("no matches — esc restores"))
	default:
		left = append(left, strings.Split(r.list.View(), "\n")...)
	}
	left = fitRows(left, l.listH)
	if l.cardH > 0 {
		left = append(left, r.detailsCard(m, l.listW, l.cardH)...)
	}
	left = fitRows(left, h)

	right := fitRows(r.out.view(th, r.spinFor(m), m.focus == focusOutput), h)

	sep := th.S.Border.Render("│")
	rows := make([]string, h)
	for i := range h {
		rows[i] = fillTo(left[i], l.listW, nil) + sep + right[i]
	}
	return rows
}

func (r *runModel) listTitle() string {
	title := "scripts"
	if r.filter != "" {
		title += " [/" + r.filter + "]"
	}
	if n := len(r.queue); n > 0 {
		title += " · " + strconv.Itoa(n) + " queued"
	}
	return title
}

// spinFor is the spinner glyph for the output title, shown only while this
// view actually has something in flight.
func (r *runModel) spinFor(m *Model) string {
	if !r.active() {
		return ""
	}
	return m.spinnerFrame()
}

// detailsCard is the eight-row answer to "what am I about to run": identity,
// entry point, environment, schedule, and how the last run went.
func (r *runModel) detailsCard(m *Model, w, h int) []string {
	th := m.th
	rows := []string{sectionRule(th, "details", w, false)}
	s := r.selected(m)
	if s == nil {
		return fitRows(append(rows, " "+th.S.Muted.Render("no script selected")), h)
	}

	line := func(glyph, label, value string) string {
		return " " + th.S.Info.Render(glyph) + " " + textkit.Truncate(
			th.S.Desc.Render(label)+" "+th.S.Muted.Render(value), max(w-3, 4))
	}
	repo := ""
	if s.Repo != "" {
		repo = " · " + s.Repo
	}
	rows = append(rows,
		" "+th.S.Success.Render("●")+" "+textkit.Truncate(
			th.S.Base.Render(s.Name)+th.S.Muted.Render(" · "+s.Runtime+repo), max(w-3, 4)),
		line("▸", "entry", entryLabel(m, *s)),
		line("⚙", "env", envLabel(*s)),
		line("↻", "cron", m.cronLabel(s.Name)),
	)
	rows = append(rows, r.lastRunLines(m, s.Name, w)...)
	return fitRows(rows, h)
}

// entryLabel is the entry file relative to the scripts dir — the part a human
// recognises, without the machine-specific prefix.
func entryLabel(m *Model, s scripts.Script) string {
	if rel, err := filepath.Rel(m.app.Paths.ScriptsDir, s.Entry); err == nil {
		return rel
	}
	return filepath.Base(s.Entry)
}

// envLabel says how much environment a script carries: how many .env keys are
// configured, and whether it has a module dir or a venv of its own. Discovery
// fills in those paths for every script whether or not they exist, so the card
// checks the disk — a card that claims a venv that was never created is worse
// than one that says nothing.
func envLabel(s scripts.Script) string {
	keys, _ := envfile.Keys(s.EnvFile)
	parts := []string{strconv.Itoa(len(keys)) + " vars"}
	if dirExists(s.ModuleDir) {
		parts = append(parts, "modules")
	}
	if dirExists(s.VenvDir) {
		parts = append(parts, "venv")
	}
	return strings.Join(parts, " · ")
}

func dirExists(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// cronLabel is the schedule and when it next fires — or the missed-fire note,
// which replaces the countdown because it is the more urgent fact.
func (m *Model) cronLabel(name string) string {
	expr, ok := m.schedules[name]
	if !ok {
		return "not scheduled"
	}
	if ms, missing := m.missed[name]; missing {
		return expr + " · ⚠ missed " + ms.ExpectedAt.Format("Mon 15:04")
	}
	now := missed.NaiveNow(m.now())
	if next, ok := cron.Next(expr, now); ok {
		return expr + " · next in " + format.RelativeTime(next.Sub(now).Seconds())
	}
	return expr
}

func (r *runModel) lastRunLines(m *Model, name string, w int) []string {
	th := m.th
	last, ok := m.statuses[name]
	if !ok || last.At.IsZero() {
		return []string{" " + th.S.Info.Render("✦") + " " + th.S.Muted.Render("never run")}
	}
	st := th.S.Success
	if last.Status != "success" {
		st = th.S.Danger
	}
	head := " " + th.S.Info.Render("✦") + " " + th.S.Desc.Render("last") + " " +
		st.Render(last.Status) + th.S.Muted.Render(" · "+format.Duration(last.DurationSec)+
		" · "+format.RelativeTime(m.now().Sub(last.At).Seconds())+" ago")
	out := []string{textkit.Truncate(head, w)}
	if res := last.Resources; res != nil {
		out = append(out, textkit.Truncate("   "+th.S.Muted.Render(
			"cpu "+trim1(res.CPUMaxPercent)+"% peak · mem "+trim1(res.MemMaxMb)+"MB peak"), w))
	}
	out = append(out, textkit.Truncate("   "+th.S.Muted.Render("at "+last.At.Format("2006-01-02 15:04:05")), w))
	return out
}

// ---------------------------------------------------------------------------
// Status line
// ---------------------------------------------------------------------------

// statusLine is what the status bar shows while this view has work in flight:
// what is running, for how long, and — when history knows how long it usually
// takes — a bar and an estimate. Past the estimate it counts OVER rather than
// sitting at 100%, which is the honest thing to show.
func (r *runModel) statusLine(m *Model, w int) (string, bool) {
	th := m.th
	if r.handle == nil {
		if r.task != nil {
			return " " + th.S.Pulse.Render(m.spinnerFrame()) + " " +
				th.S.Base.Render(r.task.name+"…"), true
		}
		return "", false
	}

	elapsed := m.now().Sub(r.startedAt)
	head := " " + th.S.Pulse.Render(m.spinnerFrame()) + " " +
		th.S.Base.Render(r.handle.Name) + " " +
		th.S.Desc.Render(format.RelativeTime(elapsed.Seconds()))

	tail := ""
	if r.etaSec > 0 {
		left := r.etaSec - elapsed.Seconds()
		note := "~" + format.RelativeTime(left) + " left"
		if left < 0 {
			note = "+" + format.RelativeTime(-left) + " over"
		}
		tail = "  " + r.prog.ViewAs(min(elapsed.Seconds()/r.etaSec, 1)) + " " + th.S.Muted.Render(note)
	}
	if n := len(r.queue); n > 0 {
		tail += th.S.Border.Render(" · ") + th.S.Info.Render(strconv.Itoa(n)+" queued")
	}
	if r.lastSample.CPU > 0 {
		tail += th.S.Border.Render(" · ") + th.S.Muted.Render("cpu "+trim1(r.lastSample.CPU)+"%")
	}
	return textkit.Truncate(head+tail, w), true
}
