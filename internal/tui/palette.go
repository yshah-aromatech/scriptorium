package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
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
	p.ti.SetWidth(max(w-2, 4))
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
		if it.owner != modeAny {
			m.switchTo(it.owner)
		}
		return replay(it.b), true
	}
	var cmd tea.Cmd
	p.ti, cmd = p.ti.Update(msg)
	p.filter()
	return cmd, false
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
