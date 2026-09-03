//go:build race

package tui

// raceEnabled lets timing-sensitive tests skip under the race detector, whose
// instrumentation multiplies frame-build cost several-fold.
const raceEnabled = true
