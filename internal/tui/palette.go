package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// paletteOverlay is `:` / ctrl+p — every action in the app, fuzzy-searchable,
// Enter runs it (design §4: "palette lists everything").
//
// It executes a command by REPLAYING its key, not by calling a handler of its
// own: the entry list comes from keyMap.groups() and Enter synthesises that
// binding's first key as a KeyPressMsg, after switching to the view that owns
// it. So the palette has no table of actions to keep in step with the keys —
// it cannot list a command that does not exist, it cannot miss one that does,
// and it cannot run a different one than the entry you picked.
type paletteOverlay struct {
	ti    textinput.Model
	items []paletteItem
	shown []int
	sel   int
	top   int
}

type paletteItem struct {
	group string
	owner mode
	b     key.Binding
}

// paletteItems is every non-modal binding, in group order. Modal groups are
// left out because their keys only mean something while the overlay that owns
// them is open — and the palette is not that overlay.
func paletteItems(k keyMap) []paletteItem {
	var out []paletteItem
	for _, g := range k.groups() {
		if g.Modal {
			continue
		}
		for _, b := range g.Keys {
			out = append(out, paletteItem{group: g.Title, owner: g.Owner, b: b})
		}
	}
	return out
}

func newPalette(m *Model) *paletteOverlay {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	st := ti.Styles()
	st.Cursor.Blink = false // a blinking cursor is a frame no golden can pin
	st.Cursor.Color = m.th.C.Accent
	st.Focused.Text = m.th.S.Base
	ti.SetStyles(st)

	p := &paletteOverlay{ti: ti, items: paletteItems(m.keys)}
	p.filter()
	return p
}

func (p *paletteOverlay) kind() overlayKind { return overlayPalette }
func (p *paletteOverlay) title() string     { return "commands" }

func (p *paletteOverlay) height(_ *Model, _, h int) int {
	return min(len(p.shown)+1, max(h-6, 3))
}

func (p *paletteOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{
		m.keys.Up, m.keys.Down,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "run")),
		m.keys.Close,
	}
}

// filter recomputes the visible set. Matching is a case-insensitive
// subsequence over "group key description", which is what makes ":fl" find
// "fleet" and ":clq" find "clear queue" — enough fuzz to type ahead, with no
// scoring to argue about.
func (p *paletteOverlay) filter() {
	q := strings.ToLower(strings.TrimSpace(p.ti.Value()))
	p.shown = p.shown[:0]
	for i, it := range p.items {
		if q == "" || subsequence(strings.ToLower(it.group+" "+it.b.Help().Key+" "+it.b.Help().Desc), q) {
			p.shown = append(p.shown, i)
		}
	}
	p.sel = min(p.sel, max(len(p.shown)-1, 0))
}

// subsequence reports whether every rune of q appears in s, in order.
func subsequence(s, q string) bool {
	i := 0
	qr := []rune(q)
	for _, r := range s {
		if i < len(qr) && r == qr[i] {
			i++
		}
	}
	return i == len(qr)
}

func (p *paletteOverlay) rows(m *Model, w, h int) []string {
	th := m.th
	p.ti.SetWidth(max(w-4, 4)) // "❯ " plus the cursor cell must fit inside the panel
	rows := []string{th.S.Info.Render("❯ ") + p.ti.View()}

	body := max(h-1, 1)
	p.top = scrollWindow(p.top, p.sel, len(p.shown), body)
	if len(p.shown) == 0 {
		return append(rows, th.S.Muted.Render("no command matches"))
	}
	for i := p.top; i < len(p.shown) && len(rows) < h; i++ {
		it := p.items[p.shown[i]]
		mark, keyStyle := "  ", th.S.Key
		if i == p.sel {
			mark, keyStyle = th.S.Accent.Render("▎")+" ", th.S.Sel
		}
		row := mark + keyStyle.Render(textkit.Fit(it.b.Help().Key, 6)) + " " +
			th.S.Base.Render(it.b.Help().Desc) + th.S.Muted.Render("  ·  "+it.group)
		rows = append(rows, textkit.Truncate(row, w))
	}
	return rows
}

func (p *paletteOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) {
		return nil, true
	}
	// arrows only, never the k/j half of the nav bindings: those are letters
	// the filter field has to receive.
	switch msg.Code {
	case tea.KeyUp:
		p.sel = max(p.sel-1, 0)
		return nil, false
	case tea.KeyDown:
		p.sel = min(p.sel+1, max(len(p.shown)-1, 0))
		return nil, false
	case tea.KeyEnter:
		if len(p.shown) == 0 {
			return nil, true
		}
		it := p.items[p.shown[p.sel]]
		// Stand where the command lives before replaying its key. Without this
		// a Run-only command picked from Fleet is a silent no-op, and the two
		// `e` bindings (.env / edit schedule) run each other's action depending
		// on which view happened to be open. This runs inside Update, so the
		// switch has already happened when the replayed key arrives.
		//
		// Unless the current view binds that key ITSELF: `f` is Fleet's
		// failures filter and History's scope toggle, one binding with one
		// owning group, and switching a History user to Fleet to run the other
		// meaning of the key they picked is the same wrong-command bug in a
		// different direction.
		if it.owner != modeAny && !m.bindsInView(it.b, m.mode) {
			m.switchTo(it.owner)
		}
		return replay(it.b), true
	}
	return p.update(msg), false
}

func (p *paletteOverlay) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.ti, cmd = p.ti.Update(msg)
	p.filter()
	return cmd
}

// replay turns a binding into the keypress that triggers it, so the palette
// runs a command down exactly the same path the keyboard does.
func replay(b key.Binding) tea.Cmd {
	keys := b.Keys()
	if len(keys) == 0 {
		return nil
	}
	press, ok := parseKey(keys[0])
	if !ok {
		return status(StatusWarn, "cannot replay the key '"+keys[0]+"'")
	}
	return func() tea.Msg { return press }
}

// themeOverlay deliberately shares the palette's small filtering and windowing
// rules without turning two short pickers into a generic framework.
type themeOverlay struct {
	ti        textinput.Model
	items     []string
	shown     []int
	sel, top  int
	original  string
	saving    bool
	settled   bool
	completed bool
}

func newThemeOverlay(m *Model) *themeOverlay {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	st := ti.Styles()
	st.Cursor.Blink = false
	st.Cursor.Color = m.th.C.Accent
	st.Focused.Text = m.th.S.Base
	ti.SetStyles(st)
	p := &themeOverlay{ti: ti, items: theme.PickerNames(), original: m.th.Name}
	p.filter()
	for i, name := range p.items {
		if name == p.original {
			p.sel = i
			break
		}
	}
	return p
}

func (p *themeOverlay) kind() overlayKind {
	// Theme selection owns its query and preview until the user applies or
	// dismisses it; queued preparation must not replace that state after a
	// failed save.
	return overlayInput
}
func (p *themeOverlay) title() string { return "theme" }
func (p *themeOverlay) height(_ *Model, _, h int) int {
	return min(max(len(p.shown)+1, 13), max(h-6, 3))
}
func (p *themeOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{m.keys.Up, m.keys.Down,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "use")),
		m.keys.Save, m.keys.Close}
}
func (p *themeOverlay) filter() {
	q := strings.ToLower(strings.TrimSpace(p.ti.Value()))
	p.sel, p.top = 0, 0
	p.shown = p.shown[:0]
	for i, name := range p.items {
		if q == "" || subsequence(strings.ToLower(name), q) {
			p.shown = append(p.shown, i)
		}
	}
	p.sel = min(p.sel, max(len(p.shown)-1, 0))
}
func (p *themeOverlay) selected() string {
	if len(p.shown) == 0 {
		return ""
	}
	return p.items[p.shown[p.sel]]
}
func (p *themeOverlay) preview(m *Model) {
	if name := p.selected(); name != "" {
		m.useTheme(theme.New(name, m.th.Profile))
	}
}
func (p *themeOverlay) rows(m *Model, w, h int) []string {
	th := m.th
	st := p.ti.Styles()
	st.Cursor.Color = th.C.Accent
	st.Focused.Text = th.S.Base
	p.ti.SetStyles(st)
	p.ti.SetWidth(max(w-4, 4))
	rows := []string{th.S.Info.Render("❯ ") + p.ti.View()}
	body := max(h-1, 1)
	listWidth := w
	var sample []string
	if w >= 64 {
		listWidth = min(30, w/2-1)
		sample = themeSample(m, w-listWidth-2)
	} else if h >= 9 {
		sample = themeSample(m, w)
		body -= 5
	}
	p.top = scrollWindow(p.top, p.sel, len(p.shown), body)
	if len(p.shown) == 0 {
		rows = append(rows, th.S.Muted.Render("no theme matches"))
	}
	for i := p.top; i < len(p.shown) && len(rows) <= body; i++ {
		name := p.items[p.shown[i]]
		mark, style := "  ", th.S.Key
		if i == p.sel {
			mark, style = th.S.Accent.Render("▎")+" ", th.S.Sel
		}
		rows = append(rows, textkit.Truncate(mark+style.Render(name), listWidth))
	}
	if w >= 64 {
		rows = fitRows(rows, h)
		for i := 1; i < h && i <= len(sample); i++ {
			rows[i] = fillTo(rows[i], listWidth, nil) + "  " + sample[i-1]
		}
	} else if len(sample) > 0 {
		rows = append(fitRows(rows, body+1), sample[:5]...)
	}
	return rows
}

// themeSample uses the same styles and panel renderer as the live views.
func themeSample(m *Model, w int) []string {
	s := m.th.S
	rows := []string{
		s.Chip.Render(" SCRIPTORIUM ") + " " + s.TabOn.Render(" Fleet ") + " " + s.ChipOff.Render("Run"),
		s.Primary.Render("▎") + s.Sel.Render(" backup-db ") + " " + s.RuntimePS.Render("ps"),
		s.Card.Render("  sync-files ") + " " + s.RuntimePy.Render("py"),
		s.Success.Render("OK") + "  " + s.Warning.Render("TIMEOUT") + "  " + s.Danger.Render("FAIL"),
		s.Pulse.Render("Running") + "  " + s.Info.Render("Queued") + "  " + s.Muted.Render("2m ago"),
	}
	rows = append(rows, renderPanel(m.th, []string{
		s.Base.Render("$ backup-db --verify"),
		s.Success.Render("Backup complete: 42 records"),
	}, w, 4, panelOpts{title: "Output · example", focused: true})...)
	rows = append(rows, renderPanel(m.th, []string{
		s.Muted.Render("Daily at 09:00") + "  " + s.Info.Render("Scheduled"),
	}, w, 3, panelOpts{title: "Schedule"})...)
	for i := range rows {
		rows[i] = textkit.Truncate(rows[i], w)
	}
	return rows
}
func (p *themeOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if p.saving {
		return nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		m.useTheme(theme.New(p.original, m.th.Profile))
		return nil, true
	}
	if key.Matches(msg, m.keys.Save) {
		if p.saving || p.selected() == "" {
			return nil, false
		}
		p.saving = true
		p.preview(m)
		picker, appDir, name := p, m.app.Paths.AppDir, m.th.Name
		return func() tea.Msg { return ThemeSavedMsg{Picker: picker, Name: name, Err: config.SaveTheme(appDir, name)} }, false
	}
	switch msg.Code {
	case tea.KeyUp:
		p.sel = max(p.sel-1, 0)
		p.preview(m)
		return nil, false
	case tea.KeyDown:
		p.sel = min(p.sel+1, max(len(p.shown)-1, 0))
		p.preview(m)
		return nil, false
	case tea.KeyEnter:
		if p.selected() == "" {
			return nil, false
		}
		p.preview(m)
		return nil, true
	}
	return p.update(m, msg), false
}

func (p *themeOverlay) update(m *Model, msg tea.Msg) tea.Cmd {
	if p.saving {
		return nil
	}
	before := p.ti.Value()
	var cmd tea.Cmd
	p.ti, cmd = p.ti.Update(msg)
	if p.ti.Value() != before {
		p.filter()
		p.preview(m)
	}
	return cmd
}
