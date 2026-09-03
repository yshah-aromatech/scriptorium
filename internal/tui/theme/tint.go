package theme

// The bubbletint adapter (v1.0.1 task 3): any of the ~340 terminal schemes in
// github.com/lrstanley/bubbletint/v2 (v2.0.2 — generated static data, zero
// dependencies, no network) can fill the fifteen tokens. Curated palettes
// always win the name lookup; the tint registry is the long tail.

import (
	"sort"
	"strings"

	tint "github.com/lrstanley/bubbletint/v2"
)

// fromTint maps one bubbletint scheme onto the semantic tokens.
//
// Direct mappings (task-3 contract): Bg, Fg, SelectionBg→SelBg, Green→Success,
// BrightYellow→Warning, Red→Danger, Cyan→Info, Blue→Primary, BrightCyan→Pulse,
// Purple→Accent, BrightBlack→Muted, Blue→RuntimePS, Yellow→RuntimePy.
//
// Derived (via the same blend/lift the curated palettes use):
//   - Border: the quietest shade of Bg-toward-Fg that clears 3:1 — identical
//     to the curated "just above the floor" chrome rule.
//   - CardBg: Bg lifted 4.5% toward white on a dark scheme, toward black on a
//     light one (blending a light ground toward white would erase the card).
//   - Muted: whatever BrightBlack (or its fallback) gives is then lifted to
//     4.5:1 against Bg — most schemes' BrightBlack is a legibility hazard as
//     text (Dracula's #555555 rates 2.2:1), and light schemes need the lift
//     to run toward their dark Fg, which the blend direction handles for free.
//
// Nil-field fallbacks, each chosen to keep the frame readable rather than to
// guess the scheme's taste:
//   - Bg nil → black on a dark scheme, white on a light one; Fg nil → the
//     opposite pole. (Dark==false with no Bg reads as a light scheme.)
//   - SelectionBg nil ("missing from most themes") → Bg blended 18% toward Fg:
//     a band one step off the ground in the scheme's own direction.
//   - BrightBlack nil → Bg blended 55% toward Fg, then the usual 4.5:1 lift.
//   - Green/Red/Cyan/Yellow nil → Fg: the status keeps its glyph and its
//     legibility, and only loses its hue.
//   - BrightYellow nil → Yellow, then Fg.
//   - BrightCyan nil → Cyan, then Blue, then Fg (Pulse degrades toward the
//     focus voice before it degrades to plain text).
//   - Blue nil → Fg; Purple nil → Blue's resolution (the brand accent joins
//     the focus voice rather than inventing a hue).
func fromTint(t *tint.Tint) Palette {
	dark := t.Dark
	bgDefault, fgDefault := "#ffffff", "#000000"
	if dark {
		bgDefault, fgDefault = "#000000", "#ffffff"
	}
	bg := hexOr(t.Bg, bgDefault)
	fg := hexOr(t.Fg, fgDefault)

	primary := hexOr(t.Blue, fg)
	cyan := hexOr(t.Cyan, fg)
	yellow := hexOr(t.Yellow, fg)
	cardTarget := "#ffffff"
	if !dark {
		cardTarget = "#000000"
	}
	return Palette{
		Bg:        bg,
		Fg:        fg,
		Muted:     lift(hexOr(t.BrightBlack, blend(bg, fg, 0.55)), fg, bg, 4.5),
		Border:    lift(bg, fg, bg, 3.02),
		Primary:   primary,
		Pulse:     hexOr(t.BrightCyan, hexOr(t.Cyan, primary)),
		Accent:    hexOr(t.Purple, primary),
		Success:   hexOr(t.Green, fg),
		Warning:   hexOr(t.BrightYellow, yellow),
		Danger:    hexOr(t.Red, fg),
		Info:      cyan,
		SelBg:     hexOr(t.SelectionBg, blend(bg, fg, 0.18)),
		CardBg:    blend(bg, cardTarget, 0.045),
		RuntimePS: primary,
		RuntimePy: yellow,
	}
}

func hexOr(c *tint.Color, fallback string) string {
	if c == nil {
		return fallback
	}
	return c.Hex()
}

// Resolve is the theme-name lookup, in the order the config contract
// promises: the curated registry first (night-owl, catppuccin-mocha,
// gruvbox-dark, tokyo-night, terminal), then bubbletint IDs. Both `-` and `_`
// spellings are accepted on either side — `tokyo_night` finds the CURATED
// tokyo-night, `rose-pine` finds the tint `rose_pine`.
func Resolve(name string) (Palette, string, bool) {
	if p, ok := Get(name); ok {
		return p, name, true
	}
	if kebab := strings.ReplaceAll(name, "_", "-"); kebab != name {
		if p, ok := Get(kebab); ok {
			return p, kebab, true
		}
	}
	if tn := tint.DefaultTintsByID(strings.ReplaceAll(name, "-", "_")); tn != nil {
		return fromTint(tn), tn.ID, true
	}
	return Palette{}, "", false
}

// CycleNames is the full set the live cycler walks: the curated palettes, then
// every tint ID, each list in its own sorted order — curated first so `]` from
// the default lands on the house palettes before the long tail.
func CycleNames() []string {
	return append(Names(), tint.DefaultTintIDs()...)
}

// NearMatches suggests up to n known names for a misspelled one — simple
// prefix/contains matching over the full cycle set, both spellings
// normalized, with a shared-prefix-length tiebreak so a one-letter typo
// ("solarised") still finds its family ("solarized_*"). It exists for exactly
// one caller: the unknown-theme warning.
func NearMatches(name string, n int) []string {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }
	q := norm(name)
	type scored struct {
		name  string
		score int
	}
	var hits []scored
	for _, cand := range CycleNames() {
		c := norm(cand)
		score := 0
		switch {
		case strings.HasPrefix(c, q) || strings.HasPrefix(q, c):
			score = 1000
		case strings.Contains(c, q) || strings.Contains(q, c):
			score = 500
		default:
			score = lcp(c, q) // a typo's family shares its head
		}
		if score >= 3 {
			hits = append(hits, scored{cand, score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	out := make([]string, 0, n)
	for _, h := range hits {
		if len(out) == n {
			break
		}
		out = append(out, h.name)
	}
	return out
}

// lcp is the length of the longest common prefix of two strings.
func lcp(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}
