// Package textkit is the TUI's text layer: display-cell width, the PS app's
// word-aware wrap with its back-pointer cache, and the sanitiser every line of
// captured script output passes through before it is ever buffered.
//
// The rules here are frozen by the parity inventory §12.5-12.8 — they decide
// where a frame's columns land, so drift shows up as sheared borders rather
// than as a test failure somewhere sensible.
package textkit

import (
	"regexp"
	"strings"
)

// ansiRE is inventory §12.8 verbatim, three alternatives:
//
//	CSI  — ESC [ params letter
//	OSC  — ESC ] anything-but-BEL/ESC, terminated by BEL or ST (ESC \)
//	the catch-all — ESC + any single other byte, so a lone ESC can never
//	escape into the frame and corrupt every downstream width computation.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\a\x1b]*(?:\a|\x1b\\)|\x1b.`)

// c0RE is the leftover-control sweep Add-TuiOutput runs after tab expansion.
// LF and CR are absent on purpose: line splitting already consumed them.
var c0RE = regexp.MustCompile("[\x00-\x08\x0b-\x1f\x7f]")

// StripANSI removes terminal escape sequences from captured output. The TUI's
// OWN rendering-time colors are applied later and are never re-stripped.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// Expand replaces tabs with spaces to 8-column stops (§12.7). The running
// column is counted in DISPLAY cells, not characters, so wide text before a
// tab consumes two columns of the run-up.
func Expand(s string) string {
	parts := strings.Split(s, "\t")
	if len(parts) == 1 {
		return s
	}
	var b strings.Builder
	col := 0
	for i, p := range parts {
		b.WriteString(p)
		if i == len(parts)-1 {
			break
		}
		col += Width(p)
		pad := 8 - col%8
		b.WriteString(strings.Repeat(" ", pad))
		col += pad
	}
	return b.String()
}

// Clean is Add-TuiOutput's sanitiser, in its order: strip escapes, expand tabs
// (only when one survived — a raw tab reaching the terminal shears the padded
// row off the panel edge), then delete the remaining C0 controls outright.
func Clean(s string) string {
	s = StripANSI(s)
	if strings.ContainsRune(s, '\t') {
		s = Expand(s)
	}
	return c0RE.ReplaceAllString(s, "")
}
