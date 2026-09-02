// Package format ports the two display-time helpers of the PowerShell app
// (src/Core.psm1 Format-StoDuration / Format-StoRelativeTime), used
// throughout the CLI and TUI to render run durations and ages/etas.
package format

import (
	"fmt"
	"math"
)

// Duration ports Format-StoDuration: seconds < 60 renders as one decimal
// place ("5.3s" — PS's '{0:n1}' InvariantCulture rendering never reaches
// the thousands-grouping comma below 60); seconds >= 60 renders as
// TimeSpan-style "{m}m{ss}s", and >= 1h as "{h}h{mm}m{ss}s".
func Duration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	total := int64(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h >= 1 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}

// RelativeTime ports Format-StoRelativeTime: a compact age/eta such as
// "45s", "5m", "3h", "1h30m" (minutes shown only under 10 hours), or "5d".
func RelativeTime(seconds float64) string {
	s := math.Abs(seconds)
	switch {
	case s < 60:
		// PS's `[int]$s` cast on a double rounds half-to-even rather than
		// truncating (same cast config.toInt documents) — 59.94s reads "60s".
		return fmt.Sprintf("%ds", int64(math.RoundToEven(s)))
	case s < 3600:
		return fmt.Sprintf("%dm", int64(s/60))
	case s < 86400:
		h := int64(s / 3600)
		m := int64(math.Mod(s, 3600) / 60)
		if m > 0 && h < 10 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dd", int64(s/86400))
	}
}
