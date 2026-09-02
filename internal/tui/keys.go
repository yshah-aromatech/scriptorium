package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// keyMap is the single source of truth for keys AND for everything that
// advertises them: the footer hints, the help overlay and the command palette
// all read these bindings' own help text, so the three can never drift the way
// the PS app's hand-maintained hint lists did.
//
// A key is bound here only once it does something. An advertised key that
// does nothing is worse than a key that is not advertised yet — which is why
// the phase-10 set was deliberately small and why each phase 11 wave adds its
// bindings WITH the behaviour behind them.
type keyMap struct {
	// global
	Fleet     key.Binding
	Run       key.Binding
	History   key.Binding
	Schedules key.Binding
	Quit      key.Binding

	// navigation (meaning depends on the focused pane)
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding

	// fleet — also History's Enter (preview log) and its f (scope to one
	// script): the same physical keys, generic enough descriptions that both
	// meanings read true (a binding's key-string must be unique across the
	// whole map, so two views sharing a key share the field, not a lookalike
	// copy of it — TestEveryBindingIsInAGroup enforces this).
	Open       key.Binding
	FailFilter key.Binding

	// run
	Focus        key.Binding
	Start        key.Binding
	Args         key.Binding
	Kill         key.Binding
	ClearQueue   key.Binding
	Sync         key.Binding
	Follow       key.Binding
	Env          key.Binding
	Deps         key.Binding
	Lint         key.Binding
	Upgrade      key.Binding
	ViewLog      key.Binding
	Copy         key.Binding
	ClearOut     key.Binding
	Scoped       key.Binding
	Filter       key.Binding
	SearchOutput key.Binding
	SelfUpdate   key.Binding
	WebhookTest  key.Binding
	// n/N (search next/prev) are NOT fields here: lowercase "n" is already
	// Deny's key-string below, and a second field on the same string fails
	// TestEveryBindingIsInAGroup the same way a duplicate Enter/f would (see
	// the fleet comment above) — but Deny's own description ("no") cannot be
	// stretched to also mean "next match" the way Open/FailFilter's could.
	// runview.go matches them by raw keypress instead, the same divergence
	// wave A already made for the deps modal's y (see its comment).

	// schedules
	ScheduleEdit key.Binding

	// overlays
	Palette key.Binding
	Help    key.Binding
	Close   key.Binding
	Save    key.Binding
	Accept  key.Binding
	Deny    key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Fleet:     key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "fleet")),
		Run:       key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "run")),
		History:   key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "history")),
		Schedules: key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "schedules")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:   key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),

		Open:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "open")),
		FailFilter: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "failures / scope")),

		Focus:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Start:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run script")),
		Args:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "args")),
		Kill:       key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "kill")),
		ClearQueue: key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "clear queue")),
		Sync:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
		Follow:     key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "follow")),
		Env:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", ".env")),
		Deps:       key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "deps")),
		Lint:       key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "lint")),
		Upgrade:    key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upgrade")),
		ViewLog:    key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "last log")),
		// PS's own pair (Tui.psm1:1133-1134, advertised together in its help as
		// "y / c — copy output to clipboard / clear the output panel"): y copies
		// the WHOLE retained buffer, c empties the panel. y does not collide
		// with the confirm overlay's Accept ("y","enter") — an overlay takes
		// every key before a view sees it, exactly as PS's modal modes do.
		Copy:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy all output")),
		ClearOut: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear output")),
		Scoped:   key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "script history")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter list")),
		// n/N are documented INSIDE this description, exactly as PS does it
		// (Tui.psm1:2203) — they are real keys with no binding of their own
		// (see the note on the field), and the help overlay is where a user
		// looks for them.
		SearchOutput: key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("ctrl+f", "search output · n/N next/prev")),
		SelfUpdate:   key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "self-update")),
		WebhookTest:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "webhook test")),

		ScheduleEdit: key.NewBinding(key.WithKeys("e", "enter"), key.WithHelp("e/↵", "edit")),

		Palette: key.NewBinding(key.WithKeys(":", "ctrl+p"), key.WithHelp(":", "commands")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Close:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		Save:    key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
		Accept:  key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y/↵", "confirm")),
		Deny:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "no")),
	}
}

// keyGroup is one titled section of the key set. This is the ONE enumeration
// of every binding: the help overlay renders it, the command palette lists it,
// and a test walks keyMap by reflection to prove nothing is missing from it —
// so a binding added without a home here fails the build's tests rather than
// quietly existing without ever being advertised.
type keyGroup struct {
	Title string
	Keys  []key.Binding

	// Owner is the view these keys belong to, or modeAny for the ones that
	// work everywhere. The command palette switches to Owner before replaying
	// a key: without it a Run-only command picked from the Fleet view is a
	// silent no-op, and the two `e` bindings (.env / edit schedule) execute
	// each other's action depending on where you happened to be standing.
	Owner mode

	// Modal marks a group whose keys exist only while an overlay is open.
	// Help lists them (they are real keys a user needs to know); the palette
	// does not, because "run y confirm" from the palette is a no-op by
	// construction — there is nothing open to confirm.
	Modal bool
}

func (k keyMap) groups() []keyGroup {
	return []keyGroup{
		{Title: "views", Owner: modeAny, Keys: []key.Binding{k.Fleet, k.Run, k.History, k.Schedules}},
		{Title: "move", Owner: modeAny, Keys: []key.Binding{
			k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom, k.Focus, k.Follow}},
		{Title: "fleet", Owner: modeFleet, Keys: []key.Binding{k.Open, k.FailFilter}},
		{Title: "run", Owner: modeRun, Keys: []key.Binding{
			k.Start, k.Args, k.Kill, k.ClearQueue, k.Sync,
			k.Env, k.Deps, k.Lint, k.Upgrade, k.ViewLog, k.Copy, k.ClearOut, k.Scoped}},
		// its own group rather than folded into "run" above: the help
		// overlay's two-column split balances on a group boundary, and
		// tacking four more entries onto an already-long group pushed the
		// balance point INSIDE it, clipping the tail off the visible window
		// at 120x40 (TestHelpOverlayShowsTheWholeKeySet caught it).
		{Title: "tools", Owner: modeRun, Keys: []key.Binding{k.Filter, k.SearchOutput, k.SelfUpdate, k.WebhookTest}},
		{Title: "schedules", Owner: modeSchedules, Keys: []key.Binding{k.ScheduleEdit}},
		{Title: "session", Owner: modeAny, Keys: []key.Binding{k.Palette, k.Help, k.Quit}},
		{Title: "overlays", Owner: modeAny, Modal: true, Keys: []key.Binding{k.Accept, k.Deny, k.Save, k.Close}},
	}
}

// hints returns the bindings the footer shows for the current view and focused
// pane — the live keys, and only those. An open overlay answers for itself:
// showing list keys under a modal would advertise bindings that do nothing
// there (the PS footer follows its mode for the same reason).
//
// The order is a truncation strategy, and it is STRUCTURAL rather than
// arithmetic: `q quit` comes first, so no description anywhere can ever grow
// long enough to push it off the row (which is exactly what happened twice —
// once to "run script", once to "failures / scope"). After it come this pane's
// primary keys, the two that lead to everything else, the rest of the pane's
// keys, and the view digits — which the header still shows after the cut.
// Model.footer drops whole hints off the END until the row fits, so what falls
// away is always the least important thing on it.
func (m *Model) hints() []key.Binding {
	if m.ov != nil {
		return m.ov.hints(m)
	}
	return m.viewHints(m.mode, m.focus)
}

// viewHints is hints() for a named view, ignoring any open overlay — the
// palette needs it to ask what a view binds while the palette itself is what is
// on screen.
func (m *Model) viewHints(md mode, fc focus) []key.Binding {
	k := m.keys
	var primary, secondary []key.Binding
	switch md {
	case modeFleet:
		primary = []key.Binding{k.Up, k.Down, k.Open, k.FailFilter, k.Start, k.Sync}
	case modeRun:
		if fc == focusOutput {
			primary = []key.Binding{k.Up, k.Down, k.PageUp, k.Follow, k.Focus, k.Start, k.Kill}
		} else {
			primary = []key.Binding{k.Up, k.Down, k.Focus, k.Start, k.Args, k.Kill, k.Sync}
			secondary = []key.Binding{k.Env, k.Deps, k.Lint, k.ViewLog, k.Copy, k.ClearOut,
				k.Scoped, k.ClearQueue, k.Filter, k.SearchOutput, k.SelfUpdate, k.WebhookTest}
		}
	case modeHistory:
		primary = []key.Binding{k.Up, k.Down, k.Open, k.Start, k.FailFilter}
	case modeSchedules:
		primary = []key.Binding{k.Up, k.Down, k.ScheduleEdit}
	}
	out := append([]key.Binding{k.Quit}, primary...)
	out = append(out, k.Palette, k.Help)
	out = append(out, secondary...)
	return append(out, k.Fleet, k.Run, k.History, k.Schedules)
}

// bindsInView reports whether a view answers this key itself. A binding shared
// by two views — `f` is Fleet's failures filter AND History's scope toggle, `↵`
// is Fleet's deep-link AND History's log preview — has ONE owning group, so
// without this the palette would drag a user out of the view where the key
// already does something and run the other meaning of it.
//
// The per-view hint lists are the answer to "what is live here": they are
// maintained beside each view's key handler, and a key a view answers but never
// advertises would be a bug in its own right.
func (m *Model) bindsInView(b key.Binding, md mode) bool {
	id := strings.Join(b.Keys(), ",")
	for _, fc := range []focus{focusList, focusOutput} {
		for _, h := range m.viewHints(md, fc) {
			if strings.Join(h.Keys(), ",") == id {
				return true
			}
		}
	}
	return false
}

// parseKey turns a binding's key name back into the keypress that produces it
// — "enter", "ctrl+p", "G", "?". It is what lets the command palette execute a
// binding by REPLAYING it: the palette then needs no table of actions of its
// own, and cannot drift from what the keys actually do.
//
// It mirrors ultraviolet's own key matcher (which key.Matches runs against),
// restricted to the shapes this app's bindings use. Unknown names return false
// rather than a wrong key.
func parseKey(name string) (tea.KeyPressMsg, bool) {
	var k tea.KeyPressMsg
	parts := strings.Split(name, "+")
	for i, part := range parts {
		if i < len(parts)-1 {
			switch part {
			case "ctrl":
				k.Mod |= tea.ModCtrl
			case "alt":
				k.Mod |= tea.ModAlt
			case "shift":
				k.Mod |= tea.ModShift
			default:
				return k, false
			}
			continue
		}
		if code, ok := namedKeys[part]; ok {
			k.Code = code
			continue
		}
		if utf8.RuneCountInString(part) != 1 {
			return k, false
		}
		k.Code, _ = utf8.DecodeRuneInString(part)
		if k.Mod == 0 {
			// a printable key carries its own text; that is what Key.String
			// reports and what key.Matches compares against
			k.Text = part
		}
	}
	return k, k.Code != 0
}

// namedKeys is the subset of ultraviolet's key names this app binds. Kept
// small on purpose: a name that is not here is a binding nothing can replay,
// and the round-trip test says so.
var namedKeys = map[string]rune{
	"enter":     tea.KeyEnter,
	"tab":       tea.KeyTab,
	"esc":       tea.KeyEscape,
	"space":     tea.KeySpace,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"delete":    tea.KeyDelete,
	"backspace": tea.KeyBackspace,
}
