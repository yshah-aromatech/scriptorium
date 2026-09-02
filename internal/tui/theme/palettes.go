package theme

// The day-one palettes (design §4). Night Owl is the identity; the other three
// are the alternates, each a single Register call against its project's own
// published hexes — no re-tinting, no invention beyond CardBg, which every
// palette derives the same way (see blend).

func init() {
	// Night Owl, the PS app's embedded palette verbatim: src/Core.psm1:13-29
	// ($script:NightOwl), rebound to semantic roles per inventory §12.2.
	// Two raw colors are unused here — White (#ffffff) survives as blend's
	// target, and BrCyan (#7fdbca) has no distinct role once Accent carries
	// the focus/hint voice.
	Register(Default, Palette{
		Bg:        "#011627",         // Core.psm1:14  Bg
		Fg:        "#d6deeb",         // Core.psm1:15  Fg
		Muted:     "#637777",         // Core.psm1:25  BrBlack
		Border:    "#5f7e97",         // Core.psm1:28  Border
		Accent:    "#c792ea",         // Core.psm1:22  Magenta
		Success:   "#22da6e",         // Core.psm1:19  Green
		Warning:   "#ffeb95",         // Core.psm1:26  BrYellow
		Danger:    "#ef5350",         // Core.psm1:18  Red
		Info:      "#21c7a8",         // Core.psm1:23  Cyan
		SelBg:     "#093b5e",         // Core.psm1:16  SelBg
		CardBg:    cardBg("#011627"), // Core.psm1:109 blend(Bg, #ffffff, 0.045)
		RuntimePS: "#82aaff",         // Core.psm1:21  Blue
		RuntimePy: "#c5e478",         // Core.psm1:20  Yellow
	})

	// Catppuccin Mocha — https://catppuccin.com/palette
	Register("catppuccin-mocha", Palette{
		Bg:        "#1e1e2e", // base
		Fg:        "#cdd6f4", // text
		Muted:     "#6c7086", // overlay0
		Border:    "#45475a", // surface1
		Accent:    "#cba6f7", // mauve
		Success:   "#a6e3a1", // green
		Warning:   "#f9e2af", // yellow
		Danger:    "#f38ba8", // red
		Info:      "#89b4fa", // blue
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
		Muted:     "#928374", // gray
		Border:    "#504945", // bg2
		Accent:    "#d3869b", // bright purple
		Success:   "#b8bb26", // bright green
		Warning:   "#fabd2f", // bright yellow
		Danger:    "#fb4934", // bright red
		Info:      "#83a598", // bright blue
		SelBg:     "#3c3836", // bg1
		CardBg:    cardBg("#282828"),
		RuntimePS: "#83a598", // bright blue
		RuntimePy: "#fabd2f", // bright yellow
	})

	// Tokyo Night (the "night" variant) — https://github.com/folke/tokyonight.nvim
	Register("tokyo-night", Palette{
		Bg:        "#1a1b26", // bg
		Fg:        "#c0caf5", // fg
		Muted:     "#565f89", // comment
		Border:    "#414868", // terminal black
		Accent:    "#bb9af7", // magenta
		Success:   "#9ece6a", // green
		Warning:   "#e0af68", // yellow
		Danger:    "#f7768e", // red
		Info:      "#7aa2f7", // blue
		SelBg:     "#283457", // bg_visual
		CardBg:    cardBg("#1a1b26"),
		RuntimePS: "#7aa2f7", // blue
		RuntimePy: "#e0af68", // yellow
	})
}

// cardBg lifts a palette's ground by 4.5% toward white — the PS app's rule for
// "cards sit just above the bg" (src/Core.psm1:109), applied to every palette
// so the card/zebra tint is derived rather than guessed.
func cardBg(bg string) string { return blend(bg, "#ffffff", 0.045) }
