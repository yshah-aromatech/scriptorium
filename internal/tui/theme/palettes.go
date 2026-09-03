package theme

// The day-one palettes (design §4). Night Owl is the identity; the other three
// are the alternates, each a single Register call against its project's own
// published hexes — no re-tinting, no invention beyond CardBg, which every
// palette derives the same way (see blend), and the two derived disciplines
// below.
//
// v1.0.1 (2026-09-03) applies two rules on top of the published hexes, both
// verifiable with ContrastRatio (contrast.go):
//
//   - Border sits JUST ABOVE 3:1 against Bg — quiet chrome that still meets
//     the non-text contrast floor. A published border that was louder is
//     darkened toward Bg (Night Owl); one that was quieter is lifted toward
//     Fg (the other three). Chrome must never out-saturate data.
//   - Muted meets 4.5:1 against Bg (it carries real text: descriptions, ages,
//     paths). A published shade that fails is lifted toward Fg to the nearest
//     passing 1%-blend step.
//
// Each adjusted token notes the published hex it replaced.

func init() {
	// Night Owl, the PS app's embedded palette: src/Core.psm1:13-29
	// ($script:NightOwl), rebound to semantic roles per inventory §12.2 as
	// corrected 2026-09-03: Primary=Blue and Pulse=BrCyan restore the PS
	// Blue↔BrCyan focus interplay (Tui.psm1:1476-1481) that v1.0.0's Accent
	// fold had replaced with Magenta. Every raw color now has a role.
	Register(Default, Palette{
		Bg:      "#011627", // Core.psm1:14  Bg
		Fg:      "#d6deeb", // Core.psm1:15  Fg
		Muted:   "#708284", // lifted from Core.psm1:25 BrBlack #637777 (3.87:1 → 4.55:1)
		Border:  "#4c6981", // quieted from Core.psm1:28 Border #5f7e97 (4.29:1 → 3.18:1)
		Primary: "#82aaff", // Core.psm1:21  Blue     — focus, selection, hint keys
		Pulse:   "#7fdbca", // Core.psm1:27  BrCyan   — spinner, focused title
		Accent:  "#c792ea", // Core.psm1:22  Magenta  — brand chip + overlay highlight
		Success: "#22da6e", // Core.psm1:19  Green
		Warning: "#ffeb95", // Core.psm1:26  BrYellow
		Danger:  "#ef5350", // Core.psm1:18  Red
		Info:    "#21c7a8", // Core.psm1:23  Cyan
		SelBg:   "#093b5e", // Core.psm1:16  SelBg
		CardBg:  cardBg("#011627"),
		// Blue also keeps its PS-app list meaning: "this is a PowerShell
		// script". Primary and RuntimePS sharing a hex is the original design.
		RuntimePS: "#82aaff", // Core.psm1:21  Blue
		RuntimePy: "#c5e478", // Core.psm1:20  Yellow
	})

	// Catppuccin Mocha — https://catppuccin.com/palette
	Register("catppuccin-mocha", Palette{
		Bg:        "#1e1e2e", // base
		Fg:        "#cdd6f4", // text
		Muted:     "#81869e", // lifted from overlay0 #6c7086 (3.36:1 → 4.56:1)
		Border:    "#66697f", // lifted from surface1 #45475a (1.80:1 → 3.04:1)
		Primary:   "#89b4fa", // blue
		Pulse:     "#89dceb", // sky
		Accent:    "#cba6f7", // mauve
		Success:   "#a6e3a1", // green
		Warning:   "#f9e2af", // yellow
		Danger:    "#f38ba8", // red
		Info:      "#94e2d5", // teal (was blue, which Primary now owns)
		SelBg:     "#313244", // surface0
		CardBg:    cardBg("#1e1e2e"),
		RuntimePS: "#89b4fa", // blue
		RuntimePy: "#f9e2af", // yellow
	})

	// Gruvbox Dark — https://github.com/morhetz/gruvbox (bright variants for
	// the status roles, which is how the theme means them to read on bg0).
	Register("gruvbox-dark", Palette{
		Bg:        "#282828", // bg0
		Fg:        "#ebdbb2", // fg1
		Muted:     "#9c8d7b", // lifted from gray #928374 (4.02:1 → 4.57:1)
		Border:    "#7a7062", // lifted from bg2 #504945 (1.67:1 → 3.03:1)
		Primary:   "#83a598", // bright blue
		Pulse:     "#8ec07c", // bright aqua
		Accent:    "#d3869b", // bright purple
		Success:   "#b8bb26", // bright green
		Warning:   "#fabd2f", // bright yellow
		Danger:    "#fb4934", // bright red
		Info:      "#83a598", // bright blue — gruvbox has no second cool hue
		SelBg:     "#3c3836", // bg1
		CardBg:    cardBg("#282828"),
		RuntimePS: "#83a598", // bright blue
		RuntimePy: "#fabd2f", // bright yellow
	})

	// Tokyo Night (the "night" variant) — https://github.com/folke/tokyonight.nvim
	Register("tokyo-night", Palette{
		Bg:        "#1a1b26", // bg
		Fg:        "#c0caf5", // fg
		Muted:     "#7982ad", // lifted from comment #565f89 (2.76:1 → 4.57:1)
		Border:    "#5e6688", // lifted from terminal black #414868 (1.91:1 → 3.04:1)
		Primary:   "#7aa2f7", // blue
		Pulse:     "#7dcfff", // cyan
		Accent:    "#bb9af7", // magenta
		Success:   "#9ece6a", // green
		Warning:   "#e0af68", // yellow
		Danger:    "#f7768e", // red
		Info:      "#1abc9c", // teal (was blue, which Primary now owns)
		SelBg:     "#283457", // bg_visual
		CardBg:    cardBg("#1a1b26"),
		RuntimePS: "#7aa2f7", // blue
		RuntimePy: "#e0af68", // yellow
	})

	// terminal — the user's own scheme, wholesale. Tokens map to ANSI 0-15 and
	// to the terminal's default fg/bg (""), and the ground is deliberately NOT
	// painted (GroundRow no-ops on an empty Bg): a user who picks this palette
	// is asking for their terminal's background, not ours. CardBg is also ""
	// — there is no way to sit "one step above" a background we cannot see.
	Register("terminal", Palette{
		Bg:        "",   // terminal default background — no ground paint, by design
		Fg:        "",   // terminal default foreground
		Muted:     "8",  // bright black
		Border:    "8",  // bright black — the quietest visible line available
		Primary:   "12", // bright blue
		Pulse:     "14", // bright cyan
		Accent:    "13", // bright magenta
		Success:   "2",  // green
		Warning:   "3",  // yellow
		Danger:    "1",  // red
		Info:      "6",  // cyan
		SelBg:     "8",  // bright black as the selection band
		CardBg:    "",   // no card tint — whitespace carries the separation
		RuntimePS: "4",  // blue
		RuntimePy: "11", // bright yellow
	})
}

// cardBg lifts a palette's ground by 4.5% toward white — the PS app's rule for
// "cards sit just above the bg" (src/Core.psm1:109), applied to every palette
// so the card/zebra tint is derived rather than guessed.
func cardBg(bg string) string { return blend(bg, "#ffffff", 0.045) }
