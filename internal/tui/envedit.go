package tui

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/envfile"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// envOverlay is the .env editor (inventory §1.8): ctrl+s writes and
// re-registers the secrets, Esc warns once about unsaved changes and discards
// on the second press.
//
// It is a plain TEXT editor, exactly as the PS one is: Save-TuiEnv writes the
// buffer's lines back verbatim with a trailing newline and never re-serialises
// key/value pairs. That is why the write below is three lines here rather than
// an envfile.Write — a Write in internal/envfile would have to promise a
// round-trip through the parser, and this feature deliberately does not go
// through the parser at all (comments, ordering, blank lines and duplicate
// keys all survive because nothing ever re-emits them).
type envOverlay struct {
	ta       textarea.Model
	script   scripts.Script
	path     string
	original string
	escArmed bool
}

// newEnvEditor seeds the editor from the script's .env, falling back to its
// .env.example — the PS behaviour, and the useful one: a fresh script opens
// with its documented keys already in front of you. It always SAVES to .env.
func newEnvEditor(m *Model, s scripts.Script) *envOverlay {
	text, path := "", s.EnvFile
	if b, err := os.ReadFile(s.EnvFile); err == nil {
		text = string(b)
	} else if b, err := os.ReadFile(s.EnvExample); err == nil {
		text = string(b)
	}
	// one trailing newline is the file's terminator, not an empty last line;
	// the save puts it back, so the round trip is exact
	text = strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetValue(text)
	ta.MoveToBegin()
	ta.Focus()

	st := ta.Styles()
	st.Cursor.Blink = false // deterministic frames
	st.Cursor.Color = m.th.C.Primary
	st.Focused.Text = m.th.S.Base
	st.Focused.EndOfBuffer = m.th.S.Muted
	ta.SetStyles(st)

	return &envOverlay{ta: ta, script: s, path: path, original: text}
}

func (e *envOverlay) kind() overlayKind { return overlayEnv }

func (e *envOverlay) dirty() bool { return e.ta.Value() != e.original }

func (e *envOverlay) title() string {
	t := "editing " + filepath.Base(e.path) + " — " + e.script.Name
	if e.dirty() {
		t += " *"
	}
	return t
}

func (e *envOverlay) height(_ *Model, _, bodyH int) int {
	return min(max(e.ta.LineCount(), 3)+1, max(bodyH-4, 3))
}

func (e *envOverlay) rows(m *Model, w, h int) []string {
	e.ta.SetWidth(w)
	e.ta.SetHeight(h)
	return strings.Split(e.ta.View(), "\n")
}

func (e *envOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{m.keys.Save, m.keys.Close}
}

func (e *envOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		if e.dirty() && !e.escArmed {
			e.escArmed = true
			return status(StatusWarn, "unsaved changes — esc again to discard, ctrl+s to save"), false
		}
		return status(StatusInfo, ".env edit cancelled"), true

	case key.Matches(msg, m.keys.Save):
		return e.save(m), true
	}
	e.escArmed = false
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return cmd, false
}

// save writes the buffer and re-registers every value in it as a secret,
// FORCED — a value a user just typed into a .env is a secret by definition,
// whatever its key is called, and it has to be in the registry before any
// output that might echo it is rendered.
func (e *envOverlay) save(m *Model) tea.Cmd {
	path, name, body := e.path, e.script.Name, e.ta.Value()+"\n"
	sec := m.app.Sec
	return func() tea.Msg {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return ErrMsg{Context: "saving " + path, Err: err}
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return ErrMsg{Context: "saving " + path, Err: err}
		}
		values, _ := envfile.Read(path)
		for k, v := range values {
			sec.Add(k, v, true)
		}
		return StatusMsg{Kind: StatusOK, Text: "saved " + filepath.Base(path) + " for " + name}
	}
}
