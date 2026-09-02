package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// The key set is one list, and everything that advertises keys reads it
// ---------------------------------------------------------------------------

// Every binding on keyMap belongs to exactly one group. This is what makes
// keyMap.groups() a safe single source for the help overlay and the palette:
// a binding added without a home here fails HERE rather than quietly existing
// without ever being advertised.
func TestEveryBindingIsInAGroup(t *testing.T) {
	k := defaultKeys()
	grouped := map[string]int{}
	for _, g := range k.groups() {
		for _, b := range g.Keys {
			grouped[strings.Join(b.Keys(), ",")]++
		}
	}

	v := reflect.ValueOf(k)
	for i := range v.NumField() {
		b, ok := v.Field(i).Interface().(key.Binding)
		if !ok {
			t.Fatalf("keyMap.%s is not a key.Binding", v.Type().Field(i).Name)
		}
		id := strings.Join(b.Keys(), ",")
		switch grouped[id] {
		case 1:
		case 0:
			t.Errorf("keyMap.%s (%q) is in no group — help and the palette will never show it",
				v.Type().Field(i).Name, id)
		default:
			t.Errorf("keyMap.%s (%q) is in %d groups", v.Type().Field(i).Name, id, grouped[id])
		}
	}
}

// Every binding's first key must be replayable, or the palette cannot run it.
// The round trip also catches a typo in a binding: "pgdn" parses to nothing.
func TestEveryBindingKeyRoundTrips(t *testing.T) {
	for _, g := range defaultKeys().groups() {
		for _, b := range g.Keys {
			for _, name := range b.Keys() {
				press, ok := parseKey(name)
				if !ok {
					t.Errorf("key %q cannot be parsed back into a keypress", name)
					continue
				}
				if got := press.String(); got != name {
					t.Errorf("key %q round-tripped to %q", name, got)
				}
				if !key.Matches(press, b) {
					t.Errorf("the keypress parsed from %q does not match its own binding", name)
				}
			}
		}
	}
}

// The palette lists every non-modal binding — the expectation is DERIVED from
// the binding set, so adding a key adds a palette entry or fails here.
func TestPaletteListsEveryLiveBinding(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	p := newPalette(m)

	listed := map[string]bool{}
	for _, it := range p.items {
		listed[it.b.Help().Desc] = true
	}
	for _, g := range m.keys.groups() {
		for _, b := range g.Keys {
			desc := b.Help().Desc
			switch {
			case g.Modal && listed[desc]:
				t.Errorf("the palette lists %q, a key that only exists inside an overlay", desc)
			case !g.Modal && !listed[desc]:
				t.Errorf("the palette is missing %q (group %q)", desc, g.Title)
			}
		}
	}
	if len(p.shown) != len(p.items) {
		t.Errorf("an unfiltered palette shows %d of %d items", len(p.shown), len(p.items))
	}
}

// The palette runs a command by replaying its key, so what it does and what
// the keyboard does cannot drift.
func TestPaletteFiltersAndExecutes(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.mode = modeRun

	press(m, ":")
	if m.ov == nil || m.ov.kind() != overlayPalette {
		t.Fatal(": did not open the palette")
	}
	press(m, "f", "l", "e")
	p, ok := m.ov.(*paletteOverlay)
	if !ok {
		t.Fatal("the palette overlay lost its type")
	}
	if len(p.shown) == 0 {
		t.Fatal("typing 'fle' filtered the palette down to nothing")
	}
	if got := p.items[p.shown[0]].b.Help().Desc; got != "fleet" {
		t.Errorf("the first match for 'fle' is %q, want fleet", got)
	}

	// Enter closes the palette and replays the binding's key
	cmd := press(m, "enter")
	if m.ov != nil {
		t.Error("Enter left the palette open")
	}
	msg := cmdMsg(cmd)
	replayed, ok := msg.(tea.KeyPressMsg)
	if !ok {
		t.Fatalf("the palette returned %#v, want a replayed keypress", msg)
	}
	m.Update(replayed)
	if m.mode != modeFleet {
		t.Errorf("the replayed key did not switch views: mode = %v", m.mode)
	}
}

// ---------------------------------------------------------------------------
// The manager
// ---------------------------------------------------------------------------

// Esc closes every overlay — the contract every later overlay inherits.
func TestEscClosesEveryOverlay(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, o := range []overlay{
		&helpOverlay{},
		newPalette(m),
		confirmPrompt("really?", nil),
		newInput(m, inputArgs, "extra args", "", func(*Model, string) tea.Cmd { return nil }),
	} {
		m.open(o)
		press(m, "esc")
		if m.ov != nil {
			t.Errorf("esc did not close the %T overlay", o)
			m.closeOverlay()
		}
	}
}

// Only one overlay is ever open: opening a second replaces the first, so no
// state can hide behind another one.
func TestOnlyOneOverlayAtATime(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "?")
	press(m, ":") // goes to the help overlay, which closes on any key
	if m.ov != nil {
		t.Fatalf("help did not close on a keypress: %T", m.ov)
	}
	press(m, ":")
	m.open(&helpOverlay{})
	if m.ov.kind() != overlayHelp {
		t.Errorf("the second overlay did not replace the first: %T", m.ov)
	}
}

// THE MODAL GATE (inventory §4.11), and the phase-10 assertion it replaces:
// P10 asserted the queue drained in every MODE because no overlay existed.
// Overlays exist now, and the gate they were written for goes live — a modal
// one holds the queue, a read-only one does not.
func TestModalOverlayBlocksTheQueue(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.queue = []queued{{Name: "heartbeat"}}

	for _, mode := range []mode{modeFleet, modeRun, modeHistory, modeSchedules} {
		m.mode = mode
		if !m.queueUnblocked() {
			t.Errorf("mode %v blocks the queue — the gate is about overlays, not views", mode)
		}
	}

	for _, o := range []overlay{&helpOverlay{}, newPalette(m)} {
		m.open(o)
		if !m.queueUnblocked() {
			t.Errorf("the read-only %T overlay blocked the queue", o)
		}
	}
	for _, o := range []overlay{
		confirmPrompt("really?", nil),
		newInput(m, inputArgs, "extra args", "", func(*Model, string) tea.Cmd { return nil }),
	} {
		m.open(o)
		if m.queueUnblocked() {
			t.Errorf("the modal %T overlay let the queue drain underneath it", o)
		}
		if cmd := m.run.dequeue(m); cmd != nil {
			t.Errorf("the queue drained under a modal %T overlay", o)
		}
	}

	m.closeOverlay()
	if cmd := m.run.dequeue(m); cmd == nil {
		t.Error("closing the overlay did not release the queue")
	}
}

// The mouse is ignored while a modal is open (inventory §1.11) — a click must
// not act on a screen the overlay is covering.
func TestMouseIsIgnoredUnderAnOverlay(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.list.Select(0)
	m.open(confirmPrompt("really?", nil))
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: 6})
	if m.run.list.Index() != 0 {
		t.Errorf("a click under a modal moved the selection to %d", m.run.list.Index())
	}
	if m.focus != focusList {
		t.Error("a click under a modal changed the focused pane")
	}
}

// ---------------------------------------------------------------------------
// Confirm
// ---------------------------------------------------------------------------

// q with something running asks first (inventory §1.2/§1.3) — and the answer
// kills the run before quitting, which is what the question promises.
func TestQuitConfirmsDuringALiveRun(t *testing.T) {
	m := runAt(t, 120, 40)
	if cmd := press(m, "q"); cmdMsg(cmd) != tea.Msg(tea.QuitMsg{}) {
		t.Error("q with nothing running did not quit immediately")
	}

	m.run.handle = fakeHandle("backup-db")
	if cmd := press(m, "q"); cmd != nil {
		t.Error("q during a live run quit instead of asking")
	}
	if m.ov == nil || m.ov.kind() != overlayConfirm {
		t.Fatalf("q during a live run opened %T, want the confirm overlay", m.ov)
	}
	if !strings.Contains(plainFrame(m), "a script is running — kill it and quit?") {
		t.Errorf("the confirm is not on screen:\n%s", plainFrame(m))
	}

	// n cancels and leaves the run alone
	press(m, "n")
	if m.ov != nil {
		t.Error("n left the confirm open")
	}
	if m.run.handle == nil {
		t.Error("n killed the run")
	}

	// y runs the action
	press(m, "q")
	cmd := press(m, "y")
	if m.ov != nil {
		t.Error("y left the confirm open")
	}
	if cmd == nil {
		t.Fatal("y produced no command")
	}
}

// ctrl+c belongs to the terminal: it quits from inside any overlay.
func TestCtrlCQuitsFromInsideAnOverlay(t *testing.T) {
	m := runAt(t, 120, 40)
	m.open(&helpOverlay{})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m.ov != nil {
		t.Error("ctrl+c left an overlay open")
	}
	if cmdMsg(cmd) != tea.Msg(tea.QuitMsg{}) {
		t.Error("ctrl+c inside an overlay did not quit")
	}
}

// ---------------------------------------------------------------------------
// Help
// ---------------------------------------------------------------------------

func TestHelpOverlayShowsTheWholeKeySet(t *testing.T) {
	m := runAt(t, 120, 40)
	press(m, "?")
	if m.ov == nil || m.ov.kind() != overlayHelp {
		t.Fatalf("? opened %T", m.ov)
	}
	frame := plainFrame(m)
	for _, g := range m.keys.groups() {
		if !strings.Contains(frame, "# "+g.Title) {
			t.Errorf("the help overlay is missing the %q group:\n%s", g.Title, frame)
		}
		for _, b := range g.Keys {
			if !strings.Contains(frame, b.Help().Desc) {
				t.Errorf("the help overlay is missing %q", b.Help().Desc)
			}
		}
	}
	// the footer follows the overlay, not the view behind it
	if !strings.Contains(frame, "any key") {
		t.Errorf("the footer still shows the view's keys under the help overlay:\n%s", frame)
	}
	press(m, "z")
	if m.ov != nil {
		t.Error("a key did not close the help overlay")
	}
}

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

func TestGoldensOverlays(t *testing.T) {
	goldenFrames(t, "overlay-help", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		press(m, "?")
		return m
	})

	goldenFrames(t, "overlay-palette", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeFleet
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		press(m, ":", "r", "u")
		return m
	})

	goldenFrames(t, "overlay-confirm", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.run.handle = fakeHandle("backup-db")
		m.run.startedAt = frozen.Add(-18 * time.Second)
		press(m, "q")
		return m
	})

	goldenFrames(t, "overlay-input", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.open(newInput(m, inputArgs, "extra args for backup-db (quotes group words)", "",
			func(*Model, string) tea.Cmd { return nil }))
		press(m, "-", "-", "f", "u", "l", "l")
		return m
	})
}
