package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// inputKind says what a prompt is FOR. One prompt component serves them all
// (architecture: "prompts -> one bubbles/textinput with a Kind field"); the
// kind is what lets a caller — and a golden frame — tell them apart, and what
// the live-filter behaviour keys off.
type inputKind int

const (
	inputArgs inputKind = iota
	inputFilter
	inputSchedule
	inputSearch
)

func (k inputKind) String() string {
	switch k {
	case inputFilter:
		return "filter"
	case inputSchedule:
		return "schedule"
	case inputSearch:
		return "search"
	}
	return "args"
}

// inputOverlay is the text prompt (inventory §1.7): Enter submits, Esc
// cancels. A prompt with an onChange applies as you type — the PS filter's
// behaviour — and Esc restores what was there before it opened.
type inputOverlay struct {
	ti       textinput.Model
	prompt   string
	kindOf   inputKind
	original string

	onSubmit func(m *Model, value string) tea.Cmd
	onChange func(m *Model, value string) tea.Cmd
}

// newInput builds a prompt. initial pre-fills the field (and is what Esc
// restores for a live-filtering kind); onSubmit runs on Enter with the final
// text and is the only place a prompt is allowed to change anything.
func newInput(m *Model, kindOf inputKind, prompt, initial string,
	onSubmit func(m *Model, value string) tea.Cmd) *inputOverlay {
	ti := textinput.New()
	ti.Prompt = ""
	ti.SetValue(initial)
	ti.CursorEnd()
	ti.Focus()

	// A blinking cursor makes every frame time-dependent, which a golden frame
	// cannot pin. Static block cursor: the same cell, every render.
	st := ti.Styles()
	st.Cursor.Blink = false
	st.Cursor.Color = m.th.C.Primary
	st.Focused.Text = m.th.S.Base
	st.Focused.Placeholder = m.th.S.Muted
	ti.SetStyles(st)

	return &inputOverlay{ti: ti, prompt: prompt, kindOf: kindOf, original: initial, onSubmit: onSubmit}
}

// live makes this a filter-style prompt: onChange runs on every keystroke and
// Esc restores the original value through it.
func (in *inputOverlay) live(onChange func(m *Model, value string) tea.Cmd) *inputOverlay {
	in.onChange = onChange
	return in
}

func (in *inputOverlay) kind() overlayKind           { return overlayInput }
func (in *inputOverlay) title() string               { return in.kindOf.String() }
func (in *inputOverlay) height(*Model, int, int) int { return 2 }

func (in *inputOverlay) rows(m *Model, w, _ int) []string {
	in.ti.SetWidth(max(w-4, 4)) // "❯ " plus the cursor cell must fit inside the panel
	return []string{
		m.th.S.Desc.Render(in.prompt),
		m.th.S.Info.Render("❯ ") + in.ti.View(),
	}
}

func (in *inputOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "submit")),
		m.keys.Close,
	}
}

func (in *inputOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) {
		// a live-filtering prompt restores what was there before it opened
		// (inventory §1.7); everything else just says it did nothing
		if in.onChange != nil {
			return in.onChange(m, in.original), true
		}
		return status(StatusInfo, "cancelled"), true
	}
	if msg.Code == tea.KeyEnter {
		return in.onSubmit(m, in.ti.Value()), true
	}
	var cmd tea.Cmd
	in.ti, cmd = in.ti.Update(msg)
	if in.onChange != nil {
		return tea.Batch(cmd, in.onChange(m, in.ti.Value())), false
	}
	return cmd, false
}
