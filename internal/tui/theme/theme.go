// Package theme is the TUI's color layer: thirteen semantic tokens, a registry
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
)

// Default is the palette the app ships with — Night Owl remains the identity
// (design §4).
const Default = "night-owl"

// Palette is the thirteen semantic tokens, as #rrggbb strings.
//
// The mapping from the PS app's raw color names is inventory §12.2. Two of its
// roles fold together to fit thirteen tokens: the focused-pane accent (PS: Blue)
// joins the key-hint color on Accent, which in Night Owl is Magenta — so a
// focused pane and its key hints share one voice, and Blue stays free to mean
// exactly one thing in the list, "this is a PowerShell script".
type Palette struct {
	Bg        string // window ground
	Fg        string // body text
	Muted     string // secondary text, inactive glyphs, scrollbar track
	Border    string // unfocused panel borders and rules
	Accent    string // focus, selection bar, key hints, the brand chip
	Success   string // success status
	Warning   string // killed / timeout / skipped
	Danger    string // failure, missed fire
	Info      string // scheduled, queued, neutral notices
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
	Success, Warning, Danger, Info      color.Color
	SelBg, CardBg, RuntimePS, RuntimePy color.Color
}

// Styles are the prebuilt lipgloss styles every view renders through.
type Styles struct {
	Base      lipgloss.Style // body text (the ground comes from the View)
	Muted     lipgloss.Style
	Accent    lipgloss.Style
	Success   lipgloss.Style
	Warning   lipgloss.Style
	Danger    lipgloss.Style
	Info      lipgloss.Style
	Border    lipgloss.Style // unfocused panel frame
	BorderOn  lipgloss.Style // focused panel frame
	Title     lipgloss.Style // panel title, inset in the frame
	TitleOn   lipgloss.Style
	Card      lipgloss.Style // card / zebra ground
	Sel       lipgloss.Style // selected row
	Chip      lipgloss.Style // the brand chip and the active view tab
	ChipOff   lipgloss.Style // an inactive view tab
	Key       lipgloss.Style // a key hint's key
	Desc      lipgloss.Style // a key hint's description
	RuntimePS lipgloss.Style
	RuntimePy lipgloss.Style

	// Heat is the eight-level sparkline ramp, cool to hot: Success through
	// Warning to Danger (inventory §1.12's green→bright-yellow→red). Built
	// once per theme so a row of sparklines is not blending colors per cell.
	Heat [heatLevels]lipgloss.Style
}

// heatLevels is the number of block glyphs a sparkline can draw, and so the
// number of stops the ramp needs.
const heatLevels = 8

// Theme is a palette bound to a profile: the tokens, the resolved colors and
// the styles, built once at startup and again only on a theme change.
type Theme struct {
	Name    string
	P       Palette
	C       Colors
	S       Styles
	Profile colorprofile.Profile
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

// New builds a theme. An unknown name falls back to Default: a stale config
// value must not black out the UI.
func New(name string, prof colorprofile.Profile) Theme {
	p, ok := Get(name)
	if !ok {
		name = Default
		p, _ = Get(Default)
	}
	c := Colors{
		Bg: conv(prof, p.Bg), Fg: conv(prof, p.Fg), Muted: conv(prof, p.Muted),
		Border: conv(prof, p.Border), Accent: conv(prof, p.Accent),
		Success: conv(prof, p.Success), Warning: conv(prof, p.Warning),
		Danger: conv(prof, p.Danger), Info: conv(prof, p.Info),
		SelBg: conv(prof, p.SelBg), CardBg: conv(prof, p.CardBg),
		RuntimePS: conv(prof, p.RuntimePS), RuntimePy: conv(prof, p.RuntimePy),
	}
	fg := func(x color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(x) }
	var heat [heatLevels]lipgloss.Style
	for i, step := range lipgloss.Blend1D(heatLevels, c.Success, c.Warning, c.Danger) {
		heat[i] = fg(prof.Convert(step))
	}
	return Theme{
		Name: name, P: p, C: c, Profile: prof,
		S: Styles{
			Base:      fg(c.Fg),
			Muted:     fg(c.Muted),
			Accent:    fg(c.Accent),
			Success:   fg(c.Success),
			Warning:   fg(c.Warning),
			Danger:    fg(c.Danger),
			Info:      fg(c.Info),
			Border:    fg(c.Border),
			BorderOn:  fg(c.Accent),
			Title:     fg(c.Muted),
			TitleOn:   lipgloss.NewStyle().Foreground(c.Accent).Bold(true),
			Card:      lipgloss.NewStyle().Foreground(c.Fg).Background(c.CardBg),
			Sel:       lipgloss.NewStyle().Foreground(c.Fg).Background(c.SelBg).Bold(true),
			Chip:      lipgloss.NewStyle().Foreground(c.Bg).Background(c.Accent).Bold(true),
			ChipOff:   fg(c.Muted),
			Key:       lipgloss.NewStyle().Foreground(c.Accent).Bold(true),
			Desc:      fg(c.Muted),
			RuntimePS: fg(c.RuntimePS),
			RuntimePy: fg(c.RuntimePy),
			Heat:      heat,
		},
	}
}

func conv(prof colorprofile.Profile, hex string) color.Color {
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
