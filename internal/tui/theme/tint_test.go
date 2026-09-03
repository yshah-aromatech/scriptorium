package theme_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	tint "github.com/lrstanley/bubbletint/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/theme"
)

// The adapter's direct mappings, pinned against Dracula's published values —
// the end-to-end tint the release is judged on.
func TestTintAdapterMapsDracula(t *testing.T) {
	p, name, ok := theme.Resolve("dracula")
	if !ok || name != "dracula" {
		t.Fatalf("Resolve(dracula) = %q, %v", name, ok)
	}
	want := map[string]string{
		"Bg":        "#1e1f28", // Bg direct
		"Fg":        "#f8f8f2", // Fg direct
		"SelBg":     "#444759", // SelectionBg direct
		"Success":   "#50fa7b", // Green
		"Warning":   "#f1fa8c", // BrightYellow
		"Danger":    "#ff5555", // Red
		"Info":      "#8be9fd", // Cyan
		"Primary":   "#bd93f9", // Blue (dracula's blue is its purple — its call)
		"Pulse":     "#8be9fd", // BrightCyan
		"Accent":    "#ff79c6", // Purple
		"RuntimePS": "#bd93f9", // Blue
		"RuntimePy": "#f1fa8c", // Yellow
	}
	got := p.Tokens()
	for k, w := range want {
		if got[k] != w {
			t.Errorf("dracula %s = %s, want %s", k, got[k], w)
		}
	}
	// the derived trio honours the same floors the curated palettes prove:
	// dracula's own BrightBlack (#555555, 2.2:1) must have been lifted
	if r := theme.ContrastRatio(p.Muted, p.Bg); r < 4.5 {
		t.Errorf("dracula Muted %s = %.2f:1, want the 4.5 lift", p.Muted, r)
	}
	if r := theme.ContrastRatio(p.Border, p.Bg); r < 3.0 || r > 3.6 {
		t.Errorf("dracula Border %s = %.2f:1, want just above 3", p.Border, r)
	}
	if p.CardBg == p.Bg {
		t.Error("dracula CardBg did not derive a step above the ground")
	}
}

// Nil-field fallbacks: a bare tint must still produce a complete, readable
// palette — every fallback documented on fromTint, exercised here.
func TestTintAdapterNilFields(t *testing.T) {
	bare := &tint.Tint{ID: "bare", Dark: true}
	pal := theme.FromTintForTest(bare)
	if pal.Bg != "#000000" || pal.Fg != "#ffffff" {
		t.Errorf("dark tint with no Bg/Fg = %s on %s, want white on black", pal.Fg, pal.Bg)
	}
	for tok, v := range pal.Tokens() {
		if len(v) != 7 || v[0] != '#' {
			t.Errorf("bare tint %s = %q, want a derived hex, never empty", tok, v)
		}
	}
	if r := theme.ContrastRatio(pal.Muted, pal.Bg); r < 4.5 {
		t.Errorf("bare Muted = %.2f:1, want the floor to hold with every field nil", r)
	}
	if pal.SelBg == pal.Bg {
		t.Error("bare SelBg did not derive a band off the ground")
	}

	light := &tint.Tint{ID: "bare-light", Dark: false}
	lp := theme.FromTintForTest(light)
	if lp.Bg != "#ffffff" || lp.Fg != "#000000" {
		t.Errorf("light tint with no Bg/Fg = %s on %s, want black on white", lp.Fg, lp.Bg)
	}
}

// Resolution order: curated always beats a tint of the same family, in either
// spelling; unknowns fail with suggestions.
func TestResolveOrder(t *testing.T) {
	curated, _ := theme.Get("tokyo-night")
	for _, spelling := range []string{"tokyo-night", "tokyo_night"} {
		p, name, ok := theme.Resolve(spelling)
		if !ok || name != "tokyo-night" || p != curated {
			t.Errorf("Resolve(%q) = %q — the curated palette must win over the tint", spelling, name)
		}
	}
	// a tint-only name resolves through the adapter in both spellings
	for _, spelling := range []string{"rose_pine", "rose-pine"} {
		if _, name, ok := theme.Resolve(spelling); !ok || name != "rose_pine" {
			t.Errorf("Resolve(%q) = %q, %v — want the tint id rose_pine", spelling, name, ok)
		}
	}
	if _, _, ok := theme.Resolve("no-such-theme-at-all"); ok {
		t.Error("an unknown name resolved")
	}
	// theme.New falls back to the default on the same unknown
	if th := theme.New("no-such-theme-at-all", colorprofile.TrueColor); th.Name != theme.Default {
		t.Errorf("New(unknown).Name = %q, want %q", th.Name, theme.Default)
	}
	// ...and canonicalizes a tint name
	if th := theme.New("dracula", colorprofile.TrueColor); th.Name != "dracula" {
		t.Errorf("New(dracula).Name = %q", th.Name)
	}
}

// Near matches: a plausible typo names its family; the count is capped at 3.
func TestNearMatches(t *testing.T) {
	got := theme.NearMatches("solarised", 3)
	if len(got) != 3 {
		t.Fatalf("NearMatches(solarised) = %v, want 3 suggestions", got)
	}
	for _, g := range got {
		if !strings.Contains(g, "solar") {
			t.Errorf("suggestion %q is not from the solarized family (%v)", g, got)
		}
	}
	if got := theme.NearMatches("drac", 3); len(got) == 0 || !strings.Contains(got[0], "dracula") {
		t.Errorf("NearMatches(drac) = %v, want dracula first", got)
	}
}

// The cycle set: curated palettes first, then every tint, and it is large —
// the "340+ schemes" the README claims had better exist.
func TestCycleNames(t *testing.T) {
	names := theme.CycleNames()
	curated := theme.Names()
	if !slices.Equal(names[:len(curated)], curated) {
		t.Errorf("cycle does not start with the curated palettes: %v", names[:len(curated)])
	}
	if len(names) < 340 {
		t.Errorf("cycle set has %d names, want the full tint registry (340+)", len(names))
	}
	if !slices.Contains(names, "dracula") {
		t.Error("dracula is missing from the cycle")
	}
}

// A light tint stays readable end-to-end: the contrast derivation must run
// toward the light scheme's dark Fg. Spot-tested on Github (Dark: false).
func TestLightTintContrast(t *testing.T) {
	p, _, ok := theme.Resolve("github")
	if !ok {
		t.Fatal("the github tint is gone from bubbletint")
	}
	if theme.ContrastRatio(p.Fg, p.Bg) < 7 {
		t.Errorf("github Fg:Bg = %.2f", theme.ContrastRatio(p.Fg, p.Bg))
	}
	if r := theme.ContrastRatio(p.Muted, p.Bg); r < 4.5 {
		t.Errorf("github Muted %s = %.2f:1 — the lift must work on a light ground too", p.Muted, r)
	}
	if r := theme.ContrastRatio(p.Border, p.Bg); r < 3.0 {
		t.Errorf("github Border %s = %.2f:1", p.Border, r)
	}
	// the card must step toward black on a light ground, not toward white
	if theme.ContrastRatio(p.CardBg, "#ffffff") < theme.ContrastRatio(p.Bg, "#ffffff") {
		t.Errorf("github CardBg %s stepped toward white on a light ground", p.CardBg)
	}
}

// bubbletint is pure data — its package (and our adapter's whole dependency
// closure below the stdlib) must never import a network package. `go list`
// answers from the module cache; no network is touched by the check itself.
func TestBubbletintIsPureData(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/lrstanley/bubbletint/v2").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, banned := range []string{"net", "net/http", "os/exec"} {
		if slices.Contains(deps, banned) {
			t.Errorf("bubbletint's dependency closure contains %q — it is supposed to be pure data", banned)
		}
	}
}
