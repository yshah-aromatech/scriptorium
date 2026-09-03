package theme_test

import (
	"image/color"
	"slices"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// Every Night Owl token pinned against src/Core.psm1's $script:NightOwl table
// (lines 13-29) and the CardBg blend at line 109. Copied here so a palette
// edit has to be deliberate; the mapping from those raw color names to these
// semantic tokens is inventory §12.2, as corrected 2026-09-03 (v1.0.1): the
// Primary/Pulse unfold, the quieted Border and the contrast-lifted Muted.
func TestNightOwlHexes(t *testing.T) {
	p, ok := theme.Get(theme.Default)
	if !ok {
		t.Fatalf("%q is not registered", theme.Default)
	}
	want := map[string]string{
		"Bg":        "#011627", // Core.psm1:14  Bg
		"Fg":        "#d6deeb", // Core.psm1:15  Fg
		"SelBg":     "#093b5e", // Core.psm1:16  SelBg
		"Danger":    "#ef5350", // Core.psm1:18  Red      — failure, missed
		"Success":   "#22da6e", // Core.psm1:19  Green    — success
		"RuntimePy": "#c5e478", // Core.psm1:20  Yellow   — python tag
		"RuntimePS": "#82aaff", // Core.psm1:21  Blue     — powershell tag
		"Primary":   "#82aaff", // Core.psm1:21  Blue     — focus, selection, hint keys
		"Accent":    "#c792ea", // Core.psm1:22  Magenta  — brand chip + overlay highlight
		"Info":      "#21c7a8", // Core.psm1:23  Cyan     — scheduled, queued
		"Muted":     "#708284", // Core.psm1:25  BrBlack #637777 lifted to 4.5:1 on Bg
		"Warning":   "#ffeb95", // Core.psm1:26  BrYellow — killed/timeout/skipped
		"Pulse":     "#7fdbca", // Core.psm1:27  BrCyan   — spinner, focused title
		"Border":    "#4c6981", // Core.psm1:28  Border #5f7e97 darkened toward Bg (chrome budget)
		"CardBg":    "#0c2031", // Core.psm1:109 blend(Bg, #ffffff, 0.045)
	}
	got := p.Tokens()
	for k, w := range want {
		if got[k] != w {
			t.Errorf("night-owl %s = %s, want %s", k, got[k], w)
		}
	}
}

// The two v1.0.1 shade adjustments are exactly the "nearest passing shade"
// rule: the published hex blended toward Fg in 1% steps until the floor holds.
// Pinning the derivation (not just the result) keeps the note in palettes.go
// honest.
func TestNightOwlAdjustedShadesAreTheNearestPassing(t *testing.T) {
	p, _ := theme.Get(theme.Default)
	if r := theme.ContrastRatio(p.Muted, p.Bg); r < 4.5 || r > 4.7 {
		t.Errorf("lifted Muted ratio = %.2f, want just above 4.5", r)
	}
	if r := theme.ContrastRatio(p.Border, p.Bg); r < 3.0 || r > 3.3 {
		t.Errorf("quieted Border ratio = %.2f, want just above 3", r)
	}
	// the original shades really did fail, or the lift notes are fiction
	if r := theme.ContrastRatio("#637777", p.Bg); r >= 4.5 {
		t.Errorf("original Muted #637777 rates %.2f — the lift note is wrong", r)
	}
}

// The alternates are day-one registrations, each one Register call; every token
// must be a parseable hex so a typo fails here and not as an invisible cell.
// The `terminal` palette is the deliberate exception: its tokens are ANSI
// indices 0-15 plus "" for the terminal's own default fg/bg.
func TestAllPalettesComplete(t *testing.T) {
	names := theme.Names()
	for _, want := range []string{"night-owl", "catppuccin-mocha", "gruvbox-dark", "tokyo-night", "terminal"} {
		if !slices.Contains(names, want) {
			t.Errorf("palette %q not registered (have %v)", want, names)
		}
	}
	for _, name := range names {
		p, _ := theme.Get(name)
		for tok, v := range p.Tokens() {
			if name == "terminal" {
				if !validANSIToken(tok, v) {
					t.Errorf("terminal.%s = %q, want \"\" (default) or an ANSI index 0-15", tok, v)
				}
				continue
			}
			if len(v) != 7 || v[0] != '#' || strings.ToLower(v) != v {
				t.Errorf("%s.%s = %q, want a lowercase #rrggbb", name, tok, v)
			}
		}
	}
}

func validANSIToken(tok, v string) bool {
	if v == "" {
		// only the tokens that mean "the terminal's own default" may be empty
		return tok == "Bg" || tok == "Fg" || tok == "CardBg"
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 0 && n <= 15
}

// The style roles behind the v1.0.1 re-voicing: focus chrome speaks Primary,
// the focused title and spinner speak Pulse (the PS Blue↔BrCyan interplay,
// Tui.psm1:1476-1481), and Accent survives only on the brand chip (bg) and
// the palette overlay's highlight.
func TestFocusVoiceIsPrimaryAndPulse(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.TrueColor)
	if got, want := th.S.BorderOn.Render("─"), th.S.Primary.Render("─"); got != want {
		t.Errorf("BorderOn = %q, want the Primary voice %q", got, want)
	}
	if got := th.S.TitleOn.Render("t"); !strings.Contains(got, "38;2;127;219;202") {
		t.Errorf("TitleOn = %q, want Pulse #7fdbca", got)
	}
	if got := th.S.Key.Render("k"); !strings.Contains(got, "38;2;130;170;255") {
		t.Errorf("Key = %q, want Primary #82aaff", got)
	}
	if got := th.S.TabOn.Render("1"); !strings.Contains(got, "38;2;130;170;255") {
		t.Errorf("TabOn = %q, want Primary on the selection band", got)
	}
	if got := th.S.Chip.Render("b"); !strings.Contains(got, "48;2;199;146;234") {
		t.Errorf("Chip = %q, want the Accent background — the brand keeps the magenta", got)
	}
	if got := th.S.Pulse.Render("⠋"); !strings.Contains(got, "38;2;127;219;202") {
		t.Errorf("Pulse = %q, want BrCyan #7fdbca", got)
	}
}

// GroundRow is the v1.0.1 "no ground" fix: pad to width, arm Fg-on-Bg at the
// head, re-arm after every reset a styled span leaves, close with a reset —
// so no cell of a grounded row can fall back to the terminal's own colors.
func TestGroundRow(t *testing.T) {
	th := theme.New(theme.Default, colorprofile.TrueColor)
	row := th.GroundRow("ab "+th.S.Success.Render("ok")+" tail", 12)

	arm := "\x1b[38;2;214;222;235;48;2;1;22;39m"
	if !strings.HasPrefix(row, arm) {
		t.Errorf("row does not open by arming the ground:\n%q", row)
	}
	if !strings.HasSuffix(row, "\x1b[m") {
		t.Errorf("row does not close with a reset:\n%q", row)
	}
	for _, reset := range []string{"\x1b[m", "\x1b[0m"} {
		at := 0
		for {
			i := strings.Index(row[at:], reset)
			if i < 0 {
				break
			}
			rest := row[at+i+len(reset):]
			if rest != "" && !strings.HasPrefix(rest, arm) {
				t.Errorf("a %q reset is not re-armed with the ground:\n%q", reset, row)
			}
			at += i + len(reset)
		}
	}
	if got := lipgloss.Width(row); got != 12 {
		t.Errorf("grounded row is %d cells, want padded to 12", got)
	}
}

// The two no-ground worlds: the `terminal` palette (by design — it inherits
// the user's scheme) and any no-colour profile.
func TestGroundRowNoGround(t *testing.T) {
	for name, th := range map[string]theme.Theme{
		"terminal palette": theme.New("terminal", colorprofile.TrueColor),
		"ascii profile":    theme.New(theme.Default, colorprofile.Ascii),
	} {
		if got := th.GroundRow("plain", 10); got != "plain" {
			t.Errorf("%s: GroundRow = %q, want the row untouched (no padding, no SGR)", name, got)
		}
	}
}

// §12.4: colorMode forces a profile; auto delegates detection to lipgloss v2's
// colorprofile (design §2) but keeps PS's floor — auto never renders worse than
// 256 colors just because TERM is unhelpful.
func TestProfile(t *testing.T) {
	cases := []struct {
		name string
		mode string
		env  []string
		want colorprofile.Profile
	}{
		{"forced truecolor beats env", "truecolor", []string{"TERM=dumb"}, colorprofile.TrueColor},
		{"forced 256 beats COLORTERM", "256", []string{"COLORTERM=truecolor"}, colorprofile.ANSI256},
		{"auto with COLORTERM", "auto", []string{"TERM=xterm-256color", "COLORTERM=truecolor"}, colorprofile.TrueColor},
		{"auto with 24bit", "auto", []string{"TERM=xterm-256color", "COLORTERM=24bit"}, colorprofile.TrueColor},
		{"auto without COLORTERM", "auto", []string{"TERM=xterm-256color"}, colorprofile.ANSI256},
		{"auto floors a bare TERM at 256", "auto", []string{"TERM=xterm"}, colorprofile.ANSI256},
		{"auto honours NO_COLOR", "auto", []string{"TERM=xterm-256color", "NO_COLOR=1"}, colorprofile.Ascii},
		{"empty mode is auto", "", []string{"TERM=xterm-256color"}, colorprofile.ANSI256},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := theme.Profile(c.mode, c.env); got != c.want {
				t.Errorf("Profile(%q, %v) = %v, want %v", c.mode, c.env, got, c.want)
			}
		})
	}
}

// The profile is baked into the styles, so the same style renders 24-bit SGR
// under truecolor and an indexed one under 256 — which is what makes the two
// golden sets (COLORTERM set and unset) differ at all.
func TestStylesFollowProfile(t *testing.T) {
	tc := theme.New(theme.Default, colorprofile.TrueColor)
	a := theme.New(theme.Default, colorprofile.ANSI256)
	no := theme.New(theme.Default, colorprofile.Ascii)

	if got := tc.S.Success.Render("ok"); !strings.Contains(got, "38;2;34;218;110") {
		t.Errorf("truecolor success = %q, want a 24-bit SGR", got)
	}
	if got := a.S.Success.Render("ok"); !strings.Contains(got, "38;5;") {
		t.Errorf("256 success = %q, want an indexed SGR", got)
	}
	if got := no.S.Success.Render("ok"); got != "ok" {
		t.Errorf("ascii success = %q, want unstyled text", got)
	}
}

// An unknown name falls back to the default rather than rendering a blank
// palette — a bad config value must not black out the UI.
func TestUnknownPaletteFallsBack(t *testing.T) {
	got := theme.New("no-such-theme", colorprofile.TrueColor)
	if got.Name != theme.Default {
		t.Errorf("New(unknown).Name = %q, want %q", got.Name, theme.Default)
	}
}

// The contrast floor (v1.0.1 task 2), computed rather than eyeballed: in every
// curated palette, body text clears WCAG AAA (7:1), metadata text clears AA
// (4.5:1), and borders clear the non-text minimum (3:1) — all against the
// palette's own ground. The `terminal` palette is exempt by construction: its
// tokens are ANSI indices whose actual colors belong to the user's terminal.
func TestCuratedPalettesMeetTheContrastFloor(t *testing.T) {
	for _, name := range []string{"night-owl", "catppuccin-mocha", "gruvbox-dark", "tokyo-night"} {
		p, ok := theme.Get(name)
		if !ok {
			t.Fatalf("%q is not registered", name)
		}
		for _, c := range []struct {
			tok, hex string
			floor    float64
		}{
			{"Fg", p.Fg, 7},
			{"Muted", p.Muted, 4.5},
			{"Border", p.Border, 3},
		} {
			if r := theme.ContrastRatio(c.hex, p.Bg); r < c.floor {
				t.Errorf("%s.%s %s on %s = %.2f:1, floor is %.1f:1", name, c.tok, c.hex, p.Bg, r, c.floor)
			}
		}
	}
}

func TestRegisterAndColors(t *testing.T) {
	theme.Register("test-only", theme.Palette{
		Bg: "#000000", Fg: "#ffffff", Muted: "#808080", Border: "#404040",
		Accent: "#ff00ff", Success: "#00ff00", Warning: "#ffff00",
		Danger: "#ff0000", Info: "#00ffff", SelBg: "#202020",
		CardBg: "#101010", RuntimePS: "#0000ff", RuntimePy: "#ffff00",
	})
	th := theme.New("test-only", colorprofile.TrueColor)
	r, g, b, _ := th.C.Accent.RGBA()
	if want := (color.RGBA{R: 0xff, G: 0x00, B: 0xff, A: 0xff}); r>>8 != uint32(want.R) || g>>8 != uint32(want.G) || b>>8 != uint32(want.B) {
		t.Errorf("Accent = %v %v %v, want ff00ff", r>>8, g>>8, b>>8)
	}
}
