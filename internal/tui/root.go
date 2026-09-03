package tui

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/buildinfo"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
	"github.com/yshah-aromatech/scriptorium/internal/update"
)

// The four workflow views (design §4). One spatial grammar, switched by 1-4.
type mode int

const (
	modeFleet mode = iota
	modeRun
	modeHistory
	modeSchedules

	// modeAny is not a view: it marks a key group whose keys work in every one
	// of them (keys.go, and the palette's view switch).
	modeAny mode = -1
)

// Which pane of a two-pane view has the keyboard.
type focus int

const (
	focusList focus = iota
	focusOutput
)

// The frame's fixed chrome: one header row, one status row, one hint row.
// Everything between belongs to the view.
const (
	headerRows = 1
	statusRows = 1
	hintRows   = 1
	chromeRows = headerRows + statusRows + hintRows

	// Below this the UI cannot say anything true, so it says that instead
	// (inventory §1.12).
	minWidth  = 40
	minHeight = 10

	// A transient status line holds for statusFadeAt, then dissolves into the
	// background over the remaining statusTTL-statusFadeAt (inventory §1.12 and
	// Get-TuiStatusLine: visible while age < 6s, blending from 5.2s over 0.8s).
	// The PS main loop keeps redrawing across a slightly wider 5.2-6.3s window
	// so the last frame of the fade is never the one left on screen; the
	// equivalent here is statusFadeStep, which keeps ticking to the end.
	statusFadeAt = 5200 * time.Millisecond
	statusTTL    = 6 * time.Second

	// statusFadeStep is the fade's frame rate — fast enough to read as a
	// dissolve, slow enough that a status message costs eight frames, not
	// eighty. Nothing schedules it until a message is nearly due to fade.
	statusFadeStep = 100 * time.Millisecond
)

// Model is the root. It owns the terminal size, the current view, pane focus
// and the shared fleet data; the views own their own layout and render
// themselves into the body rows this hands them.
type Model struct {
	app  *app.App
	th   theme.Theme
	keys keyMap
	help help.Model

	// now is injected so ages, ETAs and status expiry are deterministic under
	// test. Nothing in this package calls time.Now directly.
	now func() time.Time

	// host and version are captured once: a golden frame must not depend on
	// which machine rendered it.
	host    string
	version string

	w, h  int
	mode  mode
	focus focus

	statusText string
	statusKind StatusKind
	statusAt   time.Time

	// shared fleet data, refreshed by the tickers and by run completion
	scripts   []scripts.Script
	statuses  map[string]history.Last
	schedules map[string]string
	recent    []history.Row
	live      []lockfile.Live
	missed    map[string]missed.Miss
	syncedAt  time.Time

	// spin is the shared run spinner. It only ticks while something is
	// actually running (§12.10's budget rule: an idle TUI should cost nothing),
	// so an idle session is genuinely idle.
	spin   spinner.Model
	spinOn bool

	// historyScope is the script the History view is scoped to, "" for all of
	// them. It lives here because two things set it: the Run view's `h`
	// deep-link and the History view's own scope toggle.
	historyScope string

	// ov is the modal layer: exactly one overlay at a time, over whichever
	// view is behind it (overlay.go).
	ov overlay

	fleet   fleetModel
	run     runModel
	history historyModel
	sched   schedulesModel

	// aiConvert is a test hook for cronAI (schedules.go): nil in production,
	// where cronAI builds a real openrouter.Client keyed off
	// OPENROUTER_API_KEY. Same injection idiom as `now`.
	aiConvert func(text string) (string, error)
}

// spinnerFrame is the glyph any view showing "this is running" should use.
func (m *Model) spinnerFrame() string { return m.spin.View() }

// pulseTitleStyle is the live-now card's title voice: nil (the default panel
// voice) while idle; the activity pulse animates it while anything runs
// (task 2 wires the breathing — until then it stays the default).
func (m *Model) pulseTitleStyle() *lipgloss.Style { return nil }

// animating is true while there is something worth animating for.
func (m *Model) animating() bool { return len(m.live) > 0 || m.run.active() }

// kickSpinner starts the spinner ticking if something just became live, and
// lets it stop otherwise.
func (m *Model) kickSpinner() tea.Cmd {
	if !m.animating() || m.spinOn {
		return nil
	}
	m.spinOn = true
	return m.spin.Tick
}

// New builds the root model. now is injectable for determinism; pass time.Now.
func New(a *app.App, now func() time.Time) *Model {
	if now == nil {
		now = time.Now
	}
	host, _ := os.Hostname()
	name := themeName(a)
	m := &Model{
		app:     a,
		keys:    defaultKeys(),
		help:    help.New(),
		now:     now,
		host:    host,
		version: buildinfo.Version,
		spin: spinner.New(spinner.WithSpinner(spinner.Spinner{
			// the PS app's braille spinner, verbatim (inventory §1.12)
			Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
			FPS:    time.Second / 10,
		})),
		statuses:  map[string]history.Last{},
		schedules: map[string]string{},
		missed:    map[string]missed.Miss{},
	}
	// Sub-models first, then the theme: useTheme re-derives everything they
	// keep a copy of, so it has to have something to re-derive it into.
	m.fleet.init(m)
	m.run.init(m)
	m.history.init(m)
	m.sched.init(m)
	m.useTheme(theme.New(name, theme.Profile(a.Cfg.ColorMode, os.Environ())))
	return m
}

// themeName resolves config.json's `theme` key: the curated palettes first,
// then any bubbletint ID (both `-` and `_` spellings — theme.Resolve). An
// unknown name falls back to the default WITH a warning naming the three
// nearest known names rather than silently — a typo that leaves the UI
// looking identical is a bug report waiting to happen. The warning joins the
// app's own list, which the Run view prints into the output pane on first
// open.
//
// This is the Go-only config key (parity divergence 23, amended for v1.0.1).
// Its warning string is Go's own; nothing here touches config.json's
// PS-parity warnings.
func themeName(a *app.App) string {
	name := strings.TrimSpace(a.Cfg.Theme)
	if name == "" {
		return theme.Default
	}
	if _, canonical, ok := theme.Resolve(name); ok {
		return canonical
	}
	warn := "config.json: unknown theme '" + name + "' — using " + theme.Default
	if near := theme.NearMatches(name, 3); len(near) > 0 {
		warn += " (closest: " + strings.Join(near, ", ") + ")"
	}
	a.Warnings = append(a.Warnings, warn)
	return theme.Default
}

// cycleTheme is `]` / `[` — the live palette cycler over the full set, curated
// palettes first, then every bubbletint ID. Session-only by design: the status
// line says which theme this is and how to make it stick.
func (m *Model) cycleTheme(dir int) tea.Cmd {
	names := theme.CycleNames()
	if len(names) == 0 {
		return nil
	}
	at := slices.Index(names, m.th.Name)
	next := names[((at+dir)%len(names)+len(names))%len(names)] // at==-1 walks in from the ends
	m.useTheme(theme.New(next, m.th.Profile))
	return status(StatusInfo, "theme: "+next+" — session only · set \"theme\": \""+next+"\" in config.json to keep it")
}

// useTheme swaps the palette and re-derives every style that was copied out of
// it. Tests pin the profile through this so a golden frame does not depend on
// the COLORTERM of whoever ran the suite.
func (m *Model) useTheme(th theme.Theme) {
	m.th = th
	m.help.Styles.ShortKey = th.S.Key
	m.help.Styles.ShortDesc = th.S.Desc
	m.help.Styles.ShortSeparator = th.S.Border
	m.help.Styles.Ellipsis = th.S.Muted
	m.help.ShortSeparator = " · "
	m.run.applyTheme(th)
}

// Init starts the three tickers and the first data load. Every command here
// reads the app facade and returns a message — none of them touch the model,
// which is what keeps the update loop single-threaded (see messages.go).
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.loadFleet(),
		m.scanLocks(),
		tickCmd(),
		lockPollCmd(),
		missedTickCmd(),
		m.run.initCmd(),
	}
	// A dev build (every build that isn't a goreleaser release) has no
	// release version to compare against and self-update runs git pull
	// instead (actions.go's selfUpdate) — checking here would show a
	// permanently-false "update available" notice on every boot AND be the
	// only network call the app makes with zero configuration. Gated at the
	// call site so the network call itself is skipped, not just the notice.
	if buildinfo.Version != "dev" {
		cmds = append(cmds, versionCheckCmd())
	}
	return tea.Batch(cmds...)
}

// versionCheckCmd is the startup version notice (design row 45: it ships
// regardless of which self-update mechanism a stamped build ends up using).
// Non-blocking and off the update loop like every other Init command; a
// network hiccup is swallowed rather than shown, and "already current"
// posts nothing at all — returning a nil tea.Msg is bubbletea's own way of
// saying a command had nothing to report.
func versionCheckCmd() tea.Cmd {
	return func() tea.Msg {
		latest, available, err := update.Check(buildinfo.Version)
		if err != nil || !available {
			return nil
		}
		return StatusMsg{Kind: StatusInfo, Text: "update available: " + latest + " — press U"}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return TickMsg(t) })
}

func lockPollCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return LockPollMsg(t) })
}

func missedTickCmd() tea.Cmd {
	return tea.Tick(60*time.Second, func(t time.Time) tea.Msg { return MissedTickMsg(t) })
}

// fadeCmd schedules the next fade frame, and only while one is due: a status
// message is repainted at 10 Hz for the last ~2 s of its life and not one frame
// before that. It is spawned from the 1 Hz beat rather than from the message
// itself, so posting a status never commits the program to a timer.
func (m *Model) fadeCmd() tea.Cmd {
	if m.statusText == "" || m.now().Sub(m.statusAt) < statusFadeAt-time.Second {
		return nil
	}
	return tea.Tick(statusFadeStep, func(t time.Time) tea.Msg { return StatusFadeMsg(t) })
}

// loadFleet gathers everything the Fleet view reads, off the update loop.
func (m *Model) loadFleet() tea.Cmd {
	a := m.app
	return func() tea.Msg {
		repos := scripts.Repos(a.Cfg, a.Paths)
		rows, _ := a.Hist.Last(200)
		return ScriptsLoadedMsg{
			Scripts:   scripts.Discover(repos, a.Paths),
			Statuses:  history.LastStatuses(rows),
			Schedules: a.Cron.Schedules(),
			Recent:    rows,
			SyncedAt:  scripts.LastSyncTime(repos),
		}
	}
}

func (m *Model) scanLocks() tea.Cmd {
	a := m.app
	return func() tea.Msg { return LiveRunsMsg(a.Locks.ListLive()) }
}

// missedSweep is controller ruling 4: the 60 s sweep runs through the app
// facade, and a failure becomes a status line rather than a panic.
func (m *Model) missedSweep() tea.Cmd {
	a := m.app
	return func() tea.Msg {
		misses, err := a.MissedSweep()
		return MissedMsg{Misses: misses, Err: err}
	}
}

// Update is the single place model state changes.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.relayout()
		return m, nil

	case tea.KeyPressMsg:
		// the marquee restarts on every selection move, whatever moved it, so
		// the beat is (re)started after the key rather than inside each handler
		return m, tea.Batch(m.onKey(msg), m.run.kickMarquee(m))

	case MarqueeTickMsg:
		return m, m.run.onMarqueeTick(m)

	case TickMsg:
		if m.statusText != "" && m.now().Sub(m.statusAt) > statusTTL {
			m.statusText = ""
		}
		// the 1 Hz beat is also the queue's second chance: RunDoneMsg drains it
		// first, and this catches an entry that was queued while the gate said
		// no (inventory §4.11 drains one per loop iteration).
		return m, tea.Batch(tickCmd(), m.run.dequeue(m), m.fadeCmd())

	case StatusFadeMsg:
		if m.statusText != "" && m.now().Sub(m.statusAt) >= statusTTL {
			m.statusText = ""
			return m, nil
		}
		return m, m.fadeCmd()

	case RunStartedMsg, RunQueuedMsg, RunEventsMsg, RunDoneMsg, TaskEventsMsg,
		DepsScannedMsg, LogLoadedMsg, ClipboardMsg:
		// run and sync traffic belongs to the Run view wherever the user is
		// standing: a run started from Fleet must keep draining while they read
		// the History screen.
		return m, m.run.update(m, msg)

	case LockPollMsg:
		return m, tea.Batch(m.scanLocks(), lockPollCmd())

	case MissedTickMsg:
		return m, tea.Batch(m.missedSweep(), missedTickCmd())

	case LiveRunsMsg:
		m.live = msg
		return m, m.kickSpinner()

	case spinner.TickMsg:
		if !m.animating() {
			m.spinOn = false // nothing is running: stop burning frames
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case MissedMsg:
		return m, m.onMissed(msg)

	case ScriptsLoadedMsg:
		m.scripts, m.statuses = msg.Scripts, msg.Statuses
		m.schedules, m.recent, m.syncedAt = msg.Schedules, msg.Recent, msg.SyncedAt
		m.fleet.reload(m)
		m.run.reload(m)
		return m, m.run.kickMarquee(m)

	case HistoryLoadedMsg:
		m.history.onLoaded(m, msg)
		return m, nil

	case CronParsedMsg:
		return m, m.onCronParsed(msg)

	case ScheduleSavedMsg:
		return m, m.onScheduleSaved(msg)

	case StatusMsg:
		m.statusText, m.statusKind, m.statusAt = msg.Text, msg.Kind, m.now()
		return m, nil

	case ErrMsg:
		m.statusText = msg.Context + ": " + msg.Err.Error()
		m.statusKind, m.statusAt = StatusErr, m.now()
		return m, nil
	}

	return m, m.forward(msg)
}

// forward hands a message the root does not own to the view that does. An
// open overlay swallows the mouse entirely (inventory §1.11: the mouse is live
// only in the list and history views, never under a modal) — a click that
// selected a row behind a confirm prompt would be acting on a screen the user
// cannot see.
func (m *Model) forward(msg tea.Msg) tea.Cmd {
	if m.ov != nil {
		switch msg.(type) {
		case tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
			return nil
		}
	}
	switch m.mode {
	case modeFleet:
		return m.fleet.update(m, msg)
	case modeRun:
		return m.run.update(m, msg)
	case modeHistory:
		return m.history.update(m, msg)
	case modeSchedules:
		return m.sched.update(m, msg)
	}
	return nil
}

func (m *Model) onMissed(msg MissedMsg) tea.Cmd {
	if msg.Err != nil {
		// ruling 6: a failed sweep is a visible note, never a crash. The next
		// tick tries again.
		return status(StatusWarn, "missed-run sweep failed: "+msg.Err.Error())
	}
	fresh := map[string]missed.Miss{}
	var added []string
	for _, ms := range msg.Misses {
		fresh[ms.Name] = ms
		if _, known := m.missed[ms.Name]; !known {
			added = append(added, ms.Name)
		}
	}
	m.missed = fresh
	if len(added) == 0 {
		return nil
	}
	return status(StatusWarn, "missed run: "+strings.Join(added, ", ")+" — the scheduled fire never arrived")
}

func (m *Model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	// ctrl+c belongs to the terminal, not to whichever overlay happens to be
	// open (inventory §1.2): it quits from every mode, closing anything that
	// is up on the way out.
	if msg.Mod == tea.ModCtrl && msg.Code == 'c' {
		m.closeOverlay()
		return m.quitCmd()
	}
	if m.ov != nil {
		return m.onOverlayKey(msg)
	}
	switch {
	case key.Matches(msg, k.Quit):
		return m.quitCmd()
	case key.Matches(msg, k.Palette):
		m.open(newPalette(m))
		return nil
	case key.Matches(msg, k.Help):
		m.open(&helpOverlay{})
		return nil
	case key.Matches(msg, k.ThemeNext):
		return m.cycleTheme(1)
	case key.Matches(msg, k.ThemePrev):
		return m.cycleTheme(-1)
	case key.Matches(msg, k.Fleet):
		return m.switchTo(modeFleet)
	case key.Matches(msg, k.Run):
		return m.switchTo(modeRun)
	case key.Matches(msg, k.History):
		return m.switchTo(modeHistory)
	case key.Matches(msg, k.Schedules):
		return m.switchTo(modeSchedules)
	}
	return m.forward(msg)
}

func (m *Model) switchTo(to mode) tea.Cmd {
	if m.mode == to {
		return nil
	}
	m.mode = to
	m.relayout()
	if to == modeHistory {
		return m.loadHistory()
	}
	return nil
}

// loadHistory reads the History view's window off the update loop:
// config.historyDays when it is set, else the last 200 rows — Open-TuiHistory's
// own fallback (a days value of 0 or less means "we don't know how far back to
// look, so show something rather than nothing").
func (m *Model) loadHistory() tea.Cmd {
	a := m.app
	return func() tea.Msg {
		var rows []history.Row
		if a.Cfg.HistoryDays > 0 {
			rows, _ = a.Hist.SinceDays(a.Cfg.HistoryDays)
		} else {
			rows, _ = a.Hist.Last(200)
		}
		return HistoryLoadedMsg{Rows: rows}
	}
}

// relayout hands each view its current body box. Called on resize and on every
// view switch, so a view never renders against a stale size.
func (m *Model) relayout() {
	if m.w < minWidth || m.h < minHeight {
		return
	}
	m.fleet.resize(m, m.w, m.bodyHeight())
	m.run.resize(m, m.w, m.bodyHeight())
	m.history.resize(m, m.w, m.bodyHeight())
	m.sched.resize(m, m.w, m.bodyHeight())
}

func (m *Model) bodyHeight() int { return max(m.h-chromeRows, 1) }

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// View renders the whole frame. Bubble Tea v2 diffs it against the last frame
// and repaints only the changed cells, so this rebuilds freely — the damage
// tracking the PS app hand-rolled is the framework's job now (§12.10).
func (m *Model) View() tea.View {
	v := tea.NewView(m.frame())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor = m.th.C.Bg
	return v
}

func (m *Model) frame() string {
	var rows []string
	if m.w < minWidth || m.h < minHeight {
		rows = m.tooSmall()
	} else {
		rows = make([]string, 0, m.h)
		rows = append(rows, m.header())
		rows = append(rows, m.body()...)
		rows = append(rows, m.statusBar())
		rows = append(rows, m.footer())
	}
	// The ground pass (v1.0.1): every row carries Fg-on-Bg edge to edge, so no
	// cell of the frame ever falls back to the terminal's own colors. The
	// `terminal` palette and no-colour profiles have no ground and pass
	// through untouched — see theme.GroundRow.
	for i, r := range rows {
		rows[i] = m.th.GroundRow(r, m.w)
	}
	return strings.Join(rows, "\n")
}

// footer renders the key hints, dropping WHOLE hints off the end until the row
// fits. bubbles/help does its own dropping, but when there is no room left even
// for its ellipsis it adds the overflowing hint anyway (shouldAddItem's `<`);
// the frame then had to cut the row mid-hint, which is how "q quit" became
// "q qui…" at 80 columns. Its width is left at zero — unlimited — so there is
// one place that decides what fits, and it is this one.
//
// Nothing here has to protect any particular hint: hints() puts `q quit` FIRST
// precisely so that dropping from the end can never reach it (keys.go).
func (m *Model) footer() string {
	hints := m.hints()
	if line := m.help.ShortHelpView(hints); textkit.VisibleWidth(line) <= m.w {
		return line
	}
	for n := len(hints) - 1; n > 0; n-- {
		line := m.help.ShortHelpView(hints[:n]) + m.th.S.Muted.Render(" "+textkit.Ellipsis)
		if textkit.VisibleWidth(line) <= m.w {
			return line
		}
	}
	// one hint too wide for the whole terminal: the last-resort cut
	return textkit.Truncate(m.help.ShortHelpView(hints), m.w)
}

func (m *Model) body() []string {
	h := m.bodyHeight()
	var rows []string
	switch m.mode {
	case modeFleet:
		rows = m.fleet.view(m, m.w, h)
	case modeRun:
		rows = m.run.view(m, m.w, h)
	case modeHistory:
		rows = m.history.view(m, m.w, h)
	case modeSchedules:
		rows = m.sched.view(m, m.w, h)
	}
	rows = fitRows(rows, h)
	if m.ov != nil {
		rows = m.applyOverlay(rows, m.w, h)
	}
	return rows
}

// tooSmall is the guard frame: the one thing that is still true at 30x8.
func (m *Model) tooSmall() []string {
	lines := []string{
		m.th.S.Danger.Render("terminal too small"),
		m.th.S.Muted.Render(fmtSize(minWidth, minHeight) + " minimum · now " + fmtSize(m.w, m.h)),
	}
	rows := make([]string, 0, max(m.h, 1))
	pad := max((m.h-len(lines))/2, 0)
	for range pad {
		rows = append(rows, "")
	}
	for _, l := range lines {
		rows = append(rows, center(l, m.w))
	}
	return fitRows(rows, max(m.h, 1))
}

// header is the brand chip, the view switcher and the right-hand context chips
// — the top of the shared spatial grammar (§4). It degrades in two steps as the
// terminal narrows rather than losing the switcher.
func (m *Model) header() string {
	brand, tabs := m.th.S.Chip.Render(" ▸ scriptorium "), m.tabs(false)
	if lipgloss.Width(brand)+2+lipgloss.Width(tabs) > m.w {
		tabs = m.tabs(true)
	}
	if lipgloss.Width(brand)+2+lipgloss.Width(tabs) > m.w {
		brand = m.th.S.Chip.Render(" ▸ sto ")
	}
	left := brand + "  " + tabs
	right := m.headerChips(m.w - lipgloss.Width(left) - 1)
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		return textkit.Truncate(left, m.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) tabs(compact bool) string {
	labels := []string{"Fleet", "Run", "History", "Schedules"}
	var b strings.Builder
	for i, label := range labels {
		digit := string(rune('1' + i))
		active := mode(i) == m.mode
		switch {
		case active:
			// Primary on the selection band — the "you are here" voice
			// (v1.0.1: the active tab belongs to Primary, not to Sel's Fg)
			b.WriteString(m.th.S.TabOn.Render(" " + digit + " " + label + " "))
		case compact:
			b.WriteString(" " + m.th.S.Key.Render(digit) + " ")
		default:
			b.WriteString(" " + m.th.S.Key.Render(digit) + " " + m.th.S.Desc.Render(label) + " ")
		}
	}
	return b.String()
}

// headerChips fills the right end with as much context as fits. Rather than
// dropping chips one by one until only the least useful survives, it tries a
// short list of whole layouts, longest first — so a narrow terminal keeps the
// two facts worth keeping (how stale the repo is, which build is running)
// instead of keeping whichever chip happened to be first.
func (m *Model) headerChips(avail int) string {
	long, short := m.repoLabels()
	layouts := [][]string{
		{long, m.host, m.version},
		{short, m.host, m.version},
		{short, m.version},
		{short},
		{m.version},
	}
	for _, layout := range layouts {
		var pills []string
		for _, c := range layout {
			if c != "" {
				pills = append(pills, m.th.S.Card.Render(" "+c+" "))
			}
		}
		if len(pills) == 0 {
			continue
		}
		out := strings.Join(pills, " ")
		if lipgloss.Width(out) <= avail {
			return out
		}
	}
	return ""
}

// repoLabels is the repo/sync chip in full and in shorthand: "scripts · synced
// 4m ago" vs "⟳ 4m". Staleness is the part that earns the cells.
func (m *Model) repoLabels() (long, short string) {
	repos := scripts.Repos(m.app.Cfg, m.app.Paths)
	if len(repos) == 0 {
		return "", ""
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name)
	}
	long = strings.Join(names, "+")
	short = long
	if !m.syncedAt.IsZero() {
		age := format.RelativeTime(m.now().Sub(m.syncedAt).Seconds())
		long += " · synced " + age + " ago"
		short = "⟳ " + age
	}
	return long, short
}

// ---------------------------------------------------------------------------

// fitRows makes a view's output exactly h rows: short output is padded, long
// output is cut. A view that miscounts shifts the status bar, so this is the
// backstop rather than a convenience.
func fitRows(rows []string, h int) []string {
	if len(rows) > h {
		return rows[:h]
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows
}

func center(s string, w int) string {
	if s == "" {
		return ""
	}
	pad := (w - lipgloss.Width(s)) / 2
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

func fmtSize(w, h int) string {
	return strconv.Itoa(w) + "×" + strconv.Itoa(h)
}
