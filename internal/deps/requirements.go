package deps

import (
	"os"
	"strings"
)

// requirementsSplit is Read-StoRequirements' split character class:
// `[;\[<>=!~ ]` — everything after the first of these on a line is a marker,
// extra, or version specifier and is discarded.
func requirementsSplit(r rune) bool {
	switch r {
	case ';', '[', '<', '>', '=', '!', '~', ' ':
		return true
	}
	return false
}

// ReadRequirements is the port of Read-StoRequirements: package names from a
// requirements.txt with comments/options skipped and markers/extras/version
// specifiers stripped, verbatim (no pip-name mapping — requirements.txt
// names are already real package names). A missing file yields an empty
// list (PS: Get-Content -ErrorAction SilentlyContinue), not an error.
func ReadRequirements(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "-") {
			continue
		}
		name := strings.TrimSpace(firstToken(t))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// firstToken mirrors `($t -split '[;\[<>=!~ ]')[0]`: everything before the
// first split character (an empty leading token — e.g. a line starting with
// one of those characters — yields "", matching PS's -split semantics,
// unlike strings.FieldsFunc which would skip straight to the next field).
func firstToken(t string) string {
	if i := strings.IndexFunc(t, requirementsSplit); i >= 0 {
		return t[:i]
	}
	return t
}
