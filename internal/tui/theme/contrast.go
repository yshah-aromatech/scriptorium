package theme

import "math"

// ContrastRatio is the WCAG 2.x contrast ratio between two #rrggbb colors,
// 1 (identical) to 21 (black on white). It backs the palette floors the
// curated palettes are tested against — Fg:Bg ≥ 7, Muted:Bg ≥ 4.5,
// Border:Bg ≥ 3 — and the tint adapter's runtime lifts. Either argument
// failing to parse yields 1: "no contrast", the failing answer.
func ContrastRatio(a, b string) float64 {
	la, okA := luminance(a)
	lb, okB := luminance(b)
	if !okA || !okB {
		return 1
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// luminance is WCAG relative luminance for a #rrggbb string.
func luminance(hex string) (float64, bool) {
	ch, ok := parseHex(hex)
	if !ok {
		return 0, false
	}
	var lin [3]float64
	for i, v := range ch {
		c := float64(v) / 255
		if c <= 0.04045 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		lin[i] = c
	}
	return 0.2126*lin[0] + 0.7152*lin[1] + 0.0722*lin[2], true
}

// lift blends c toward `toward` in 1% steps until it reaches `floor` contrast
// against bg — the "nearest passing shade" rule used for the curated palettes'
// noted adjustments and, at runtime, for the tint adapter's Muted and Border.
// If even `toward` cannot reach the floor (a low-contrast scheme), `toward`
// itself is returned: the most readable shade available.
func lift(c, toward, bg string, floor float64) string {
	for t := 0.0; t <= 1.0; t += 0.01 {
		h := blend(c, toward, t)
		if ContrastRatio(h, bg) >= floor {
			return h
		}
	}
	return toward
}
