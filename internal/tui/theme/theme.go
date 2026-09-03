// Package theme is the TUI's color layer: fifteen semantic tokens, a registry
// of palettes that fill them, and the lipgloss styles built once per palette
// and profile.
//
// Nothing outside this package names a color. Views ask for a role — Success,
// Muted, BorderOn — so adding a palette is one Register call (design §4) and
// the 256-color downsampling is lipgloss v2's job, not ours (inventory §12.3,
// which the PS app hand-rolled as ConvertTo-Ansi256Index).
package theme

import (
	"fmt"
	"image/color"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Default is the palette the app ships with — Night Owl remains the identity
// (design §4).
const Default = "night-owl"

// Palette is the fifteen semantic tokens, as #rrggbb strings (or, for the
// `terminal` palette only, ANSI indices "0"-"15" and "" for the terminal's own
// default fg/bg).
//
// The mapping from the PS app's raw color names is inventory §12.2, corrected
// 2026-09-03 (v1.0.1): v1.0.0 folded the focused-pane accent onto Accent
// (Magenta), which put purple on every border, hint and selection — the
// "purple voice" defect. Primary and Pulse restore the PS app's split
// (Tui.psm1:1476-1481, the Blue↔BrCyan focus interplay): Primary carries
// focus, selection accent, hint keys and the active tab; Pulse carries the
// spinner and the focused pane's title; Accent retreats to the brand chip,
// the palette overlay's highlight and the marquee glow.
type Palette struct {
	Bg        string // window ground
	Fg        string // body text
	Muted     string // secondary text, inactive glyphs — genuine metadata only
	Border    string // unfocused panel borders and rules, quiet (just above 3:1)
	Primary   string // focus borders, selection accent, hint keys, active tab
	Pulse     string // spinner, focused-pane title (the Blue↔BrCyan interplay)
	Accent    string // brand chip, palette-overlay highlight, marquee glow
	Success   string // success status
	Warning   string // killed / timeout / skipped, sparkline peak heat
	Danger    string // failure, missed fire
	Info      string // scheduled, queued, neutral notices, sparkline body
	SelBg     string // selected row background
	CardBg    string // card + zebra background, one step above Bg
	RuntimePS string // the "ps" runtime tag
	RuntimePy string // the "py" runtime tag
}

// Tokens exposes the palette as a name→hex map, for validation and for a
// future palette picker. Ordering is the struct's.
func (p Palette) Tokens() map[string]string {
	return map[string]string{
		"Bg": p.Bg, "Fg": p.Fg, "Muted": p.Muted, "Border": p.Border,
		"Primary": p.Primary, "Pulse": p.Pulse,
		"Accent": p.Accent, "Success": p.Success, "Warning": p.Warning,
		"Danger": p.Danger, "Info": p.Info, "SelBg": p.SelBg,
		"CardBg": p.CardBg, "RuntimePS": p.RuntimePS, "RuntimePy": p.RuntimePy,
	}
}

// Colors is Palette resolved through a color profile: every token already
// downsampled, so a style built from these renders correctly on a 256-color
// terminal without any per-render conversion.
type Colors struct {
	Bg, Fg, Muted, Border, Accent       color.Color
	Primary, Pulse                      color.Color
	Success, Warning, Danger, Info      color.Color
	SelBg, CardBg, RuntimePS, RuntimePy color.Color
}

// Styles are the prebuilt lipgloss styles every view renders through.
type Styles struct {
	Base      lipgloss.Style // body text (the ground comes from Theme.GroundRow)
	Muted     lipgloss.Style
	Primary   lipgloss.Style // the focus voice: selection bar, cursor
	Pulse     lipgloss.Style // the spinner
	Accent    lipgloss.Style // brand + palette-overlay highlight only
	Success   lipgloss.Style
	Warning   lipgloss.Style
	Danger    lipgloss.Style
	Info      lipgloss.Style
	Border    lipgloss.Style // unfocused panel frame, quiet
	BorderOn  lipgloss.Style // focused panel frame — Primary
	Title     lipgloss.Style // panel title, inset in the frame
	TitleOn   lipgloss.Style // focused panel title — Pulse (the PS interplay)
	Card      lipgloss.Style // card / zebra ground
	Sel       lipgloss.Style // selected row
	Chip      lipgloss.Style // the brand chip
	TabOn     lipgloss.Style // the active view tab — Primary on SelBg
	ChipOff   lipgloss.Style // an inactive view tab
	Key       lipgloss.Style // a key hint's key — Primary
	Desc      lipgloss.Style // a key hint's description
	RuntimePS lipgloss.Style
	RuntimePy lipgloss.Style
}

// Theme is a palette bound to a profile: the tokens, the resolved colors and
// the styles, built once at startup and again only on a theme change.
type Theme struct {
	Name    string
	P       Palette
	C       Colors
	S       Styles
	Profile colorprofile.Profile

	// groundSGR arms the window ground (Fg on Bg) at the head of every frame
	// row; empty when the palette paints no ground (the `terminal` palette by
	// design, and every no-colour profile). See GroundRow.
	groundSGR string
}

var (
	mu       sync.RWMutex
	registry = map[string]Palette{}
)

// Register adds (or replaces) a palette. Adding a theme is exactly this call.
func Register(name string, p Palette) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = p
}

// Get returns a registered palette.
func Get(name string) (Palette, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[name]
	return p, ok
}

// Names lists the registered palettes, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Profile resolves config.colorMode to a lipgloss color profile (§12.4).
//
// 'truecolor' and '256' force; 'auto' (the default) delegates detection to
// lipgloss v2's colorprofile, which implements PS's COLORTERM=truecolor|24bit
// rule and, for free, NO_COLOR / CLICOLOR / terminfo. The one thing kept from
// the PS semantics on top of that is the floor: auto never lands below 256
// colors just because TERM is unhelpful — but a terminal that actually said
// "no color" is believed.
func Profile(colorMode string, env []string) colorprofile.Profile {
	switch colorMode {
	case "truecolor":
		return colorprofile.TrueColor
	case "256":
		return colorprofile.ANSI256
	}
	p := colorprofile.Env(env)
	if p <= colorprofile.Ascii {
		return p
	}
	return max(p, colorprofile.ANSI256)
}

// New builds a theme. The name resolves curated-first, then through the
// bubbletint registry (see Resolve); an unknown name falls back to Default —
// a stale config value must not black out the UI. Theme.Name carries the
// canonical resolved spelling, which is what the live cycler indexes on.
func New(name string, prof colorprofile.Profile) Theme {
	p, canonical, ok := Resolve(name)
	if !ok {
		canonical = Default
		p, _ = Get(Default)
	}
	name = canonical
	c := Colors{
		Bg: conv(prof, p.Bg), Fg: conv(prof, p.Fg), Muted: conv(prof, p.Muted),
		Border: conv(prof, p.Border), Accent: conv(prof, p.Accent),
		Primary: conv(prof, p.Primary), Pulse: conv(prof, p.Pulse),
		Success: conv(prof, p.Success), Warning: conv(prof, p.Warning),
		Danger: conv(prof, p.Danger), Info: conv(prof, p.Info),
		SelBg: conv(prof, p.SelBg), CardBg: conv(prof, p.CardBg),
		RuntimePS: conv(prof, p.RuntimePS), RuntimePy: conv(prof, p.RuntimePy),
	}
	fg := func(x color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(x) }
	ground := ""
	if c.Bg != nil && c.Fg != nil {
		ground = ansi.Style{}.ForegroundColor(c.Fg).BackgroundColor(c.Bg).String()
	}
	return Theme{
		Name: name, P: p, C: c, Profile: prof, groundSGR: ground,
		S: Styles{
			Base:      fg(c.Fg),
			Muted:     fg(c.Muted),
			Primary:   fg(c.Primary),
			Pulse:     fg(c.Pulse),
			Accent:    fg(c.Accent),
			Success:   fg(c.Success),
			Warning:   fg(c.Warning),
			Danger:    fg(c.Danger),
			Info:      fg(c.Info),
			Border:    fg(c.Border),
			BorderOn:  fg(c.Primary),
			Title:     fg(c.Muted),
			TitleOn:   lipgloss.NewStyle().Foreground(c.Pulse).Bold(true),
			Card:      lipgloss.NewStyle().Foreground(c.Fg).Background(c.CardBg),
			Sel:       lipgloss.NewStyle().Foreground(c.Fg).Background(c.SelBg).Bold(true),
			Chip:      lipgloss.NewStyle().Foreground(c.Bg).Background(c.Accent).Bold(true),
			TabOn:     lipgloss.NewStyle().Foreground(c.Primary).Background(c.SelBg).Bold(true),
			ChipOff:   fg(c.Muted),
			Key:       lipgloss.NewStyle().Foreground(c.Primary).Bold(true),
			Desc:      fg(c.Muted),
			RuntimePS: fg(c.RuntimePS),
			RuntimePy: fg(c.RuntimePy),
		},
	}
}

// GroundRow paints the window ground under one frame row: the row is padded to
// w cells, the ground (Fg on Bg) is armed at its head and re-armed after every
// SGR reset a styled span left behind, and the row ends on a clean reset. This
// is the v1.0.1 fix for the "no ground" defect — without it every unstyled
// cell (and every span end) fell back to the terminal's own colors, and Night
// Owl rendered as fragments on an alien background.
//
// A theme with no ground — the `terminal` palette, whose whole point is to
// inherit the user's scheme, or any no-colour profile — returns the row
// untouched.
func (t Theme) GroundRow(row string, w int) string {
	if t.groundSGR == "" {
		return row
	}
	if gap := w - lipgloss.Width(row); gap > 0 {
		row += strings.Repeat(" ", gap)
	}
	// lipgloss emits "\x1b[m"; some components spell it "\x1b[0m". Neither
	// string contains the other, so two passes cannot double-arm.
	row = strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+t.groundSGR)
	row = strings.ReplaceAll(row, "\x1b[m", "\x1b[m"+t.groundSGR)
	return t.groundSGR + row + "\x1b[m"
}

// Fade returns c blended amount (0..1) of the way into the background — the
// status line's dissolve. amount 0 is the colour itself; 1 is invisible.
//
// The nil guard is the no-colour profile's whole shape: under NO_COLOR or
// TERM=dumb every token resolves to nil, and lipgloss.Blend1D cannot build a
// ramp from nil stops (it returns an empty slice, and the caller indexes it —
// the phase-10 crash class). A profile with no colours has nothing to fade, so
// it gets the unstyled text it asked for.
func (t Theme) Fade(c color.Color, amount float64) lipgloss.Style {
	if c == nil || t.C.Bg == nil {
		return lipgloss.NewStyle()
	}
	amount = min(max(amount, 0), 1)
	const steps = 17
	ramp := lipgloss.Blend1D(steps, c, t.C.Bg)
	if len(ramp) == 0 {
		return lipgloss.NewStyle().Foreground(c)
	}
	i := min(int(amount*float64(len(ramp)-1)+0.5), len(ramp)-1)
	return lipgloss.NewStyle().Foreground(t.Profile.Convert(ramp[i]))
}

// conv resolves a token through the profile. "" is the `terminal` palette's
// "use the terminal's own default" — nil, which lipgloss renders as no SGR at
// all, exactly like a no-colour profile does for every token.
func conv(prof colorprofile.Profile, hex string) color.Color {
	if hex == "" {
		return nil
	}
	return prof.Convert(lipgloss.Color(hex))
}

// ---------------------------------------------------------------------------

// blend mixes two #rrggbb colors, t of the way from→to. It exists for exactly
// one job: deriving CardBg, the "one step above the ground" card/zebra tint, so
// every palette gets it the same way rather than by eyeballing a hex.
//
// It is the PS app's Get-StoBlendHex (src/Core.psm1:60), whose [int] cast on
// the mixed channel is banker's rounding — which is why night-owl's CardBg
// lands on #0c2031 exactly.
func blend(from, to string, t float64) string {
	f, ok1 := parseHex(from)
	g, ok2 := parseHex(to)
	if !ok1 || !ok2 {
		return from
	}
	var out [3]int64
	for i := range out {
		out[i] = int64(math.RoundToEven(float64(f[i]) + (float64(g[i])-float64(f[i]))*t))
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
}

func parseHex(s string) ([3]int64, bool) {
	s = strings.ToLower(s)
	if len(s) != 7 || s[0] != '#' {
		return [3]int64{}, false
	}
	var out [3]int64
	for i := range out {
		v, err := strconv.ParseInt(s[1+i*2:3+i*2], 16, 32)
		if err != nil {
			return [3]int64{}, false
		}
		out[i] = v
	}
	return out, true
}
