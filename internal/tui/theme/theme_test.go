package theme_test

import (
	"image/color"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// Every Night Owl token pinned against src/Core.psm1's $script:NightOwl table
// (lines 13-29) and the CardBg blend at line 109. Copied here so a palette
// edit has to be deliberate; the mapping from those raw color names to these
// semantic tokens is inventory §12.2.
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
		"Accent":    "#c792ea", // Core.psm1:22  Magenta  — key hints + focus
		"Info":      "#21c7a8", // Core.psm1:23  Cyan     — scheduled, queued
		"Muted":     "#637777", // Core.psm1:25  BrBlack  — muted text
		"Warning":   "#ffeb95", // Core.psm1:26  BrYellow — killed/timeout/skipped
		"Border":    "#5f7e97", // Core.psm1:28  Border
		"CardBg":    "#0c2031", // Core.psm1:109 blend(Bg, #ffffff, 0.045)
	}
	got := map[string]string{
		"Bg": p.Bg, "Fg": p.Fg, "SelBg": p.SelBg, "Danger": p.Danger,
		"Success": p.Success, "RuntimePy": p.RuntimePy, "RuntimePS": p.RuntimePS,
		"Accent": p.Accent, "Info": p.Info, "Muted": p.Muted,
		"Warning": p.Warning, "Border": p.Border, "CardBg": p.CardBg,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("night-owl %s = %s, want %s", k, got[k], w)
		}
	}
}

// The alternates are day-one registrations, each one Register call; every token
// must be a parseable hex so a typo fails here and not as an invisible cell.
func TestAllPalettesComplete(t *testing.T) {
	names := theme.Names()
	for _, want := range []string{"night-owl", "catppuccin-mocha", "gruvbox-dark", "tokyo-night"} {
		if !slices.Contains(names, want) {
			t.Errorf("palette %q not registered (have %v)", want, names)
		}
	}
	for _, name := range names {
		p, _ := theme.Get(name)
		for tok, hex := range p.Tokens() {
			if len(hex) != 7 || hex[0] != '#' || strings.ToLower(hex) != hex {
				t.Errorf("%s.%s = %q, want a lowercase #rrggbb", name, tok, hex)
			}
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
