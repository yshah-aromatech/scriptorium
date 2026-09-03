package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// The v1.0.1 "no ground" defect, stated as a test: a truecolor frame may not
// contain a single visible cell that falls back to the terminal's own
// background. Before the fix, frames reset to the terminal default 257 times —
// Night Owl rendered as fragments on an alien background.
//
// bareCells walks a row's SGR state machine and counts the display cells
// rendered while NO background is armed.
func bareCells(row string) int {
	bare := 0
	bgOn := false
	for len(row) > 0 {
		if strings.HasPrefix(row, "\x1b[") {
			end := strings.IndexByte(row[2:], 'm')
			if end < 0 {
				break // not an SGR; frames contain nothing else
			}
			params := strings.Split(row[2:2+end], ";")
			for i := 0; i < len(params); i++ {
				switch params[i] {
				case "", "0":
					bgOn = false
				case "49":
					bgOn = false
				case "48":
					bgOn = true
					// skip the 2;r;g;b / 5;n payload
					if i+1 < len(params) && params[i+1] == "2" {
						i += 4
					} else if i+1 < len(params) && params[i+1] == "5" {
						i += 2
					}
				case "38", "58":
					if i+1 < len(params) && params[i+1] == "2" {
						i += 4
					} else if i+1 < len(params) && params[i+1] == "5" {
						i += 2
					}
				default:
					if n, ok := atoi(params[i]); ok && ((n >= 40 && n <= 47) || (n >= 100 && n <= 107)) {
						bgOn = true
					}
				}
			}
			row = row[2+end+1:]
			continue
		}
		next := strings.Index(row, "\x1b[")
		text := row
		if next >= 0 {
			text, row = row[:next], row[next:]
		} else {
			row = ""
		}
		if !bgOn {
			bare += textkit.Width(text)
		}
	}
	return bare
}

func atoi(s string) (int, bool) {
	n := 0
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// Every view, every contract size: the ground reaches every cell, edge to
// edge — each row is exactly the frame's width, none of it unpainted.
func TestGroundCoversEveryCellInEveryView(t *testing.T) {
	for _, view := range []string{"1", "2", "3", "4"} {
		for _, sz := range goldenSizes {
			m := newFixtureModel(t, truecolorEnv)
			m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			press(m, view)
			for i, row := range strings.Split(m.frame(), "\n") {
				if got := bareCells(row); got != 0 {
					t.Errorf("view %s %dx%d row %d: %d cells have no background paint:\n%q",
						view, sz[0], sz[1], i, got, row)
				}
				if w := textkit.Width(textkit.StripANSI(row)); w != sz[0] {
					t.Errorf("view %s %dx%d row %d: %d cells wide, want the ground padded to %d",
						view, sz[0], sz[1], i, w, sz[0])
				}
			}
		}
	}
}

// The too-small guard frame is still a frame — it gets the ground too.
func TestGroundCoversTheTooSmallFrame(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	for i, row := range strings.Split(m.frame(), "\n") {
		if got := bareCells(row); got != 0 {
			t.Errorf("too-small row %d: %d cells have no background paint:\n%q", i, got, row)
		}
	}
}

// The `terminal` palette is the designed exception: it inherits the user's
// scheme wholesale, so it paints NO truecolor ground at all — most of the
// frame stays on the terminal's own default background, and the only SGRs it
// emits are ANSI-indexed.
func TestTerminalPalettePaintsNoGround(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	m.useTheme(theme.New("terminal", colorprofile.TrueColor))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	frame := m.frame()

	if strings.Contains(frame, "48;2;") {
		t.Error("the terminal palette painted a truecolor background")
	}
	if strings.Contains(frame, "38;2;") {
		t.Error("the terminal palette painted a truecolor foreground — tokens must stay ANSI 0-15")
	}
	bare := 0
	for _, row := range strings.Split(frame, "\n") {
		bare += bareCells(row)
	}
	if bare == 0 {
		t.Error("no cell inherits the terminal's own background — the no-ground exception is not holding")
	}
	// and the View must not claim a background color it did not paint
	if v := m.View(); v.BackgroundColor != nil {
		t.Errorf("View().BackgroundColor = %v, want nil for the terminal palette", v.BackgroundColor)
	}
}
