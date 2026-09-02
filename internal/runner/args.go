package runner

import "unicode"

// SplitArguments is the port of Split-StoArguments (src/Core.psm1:617-638):
// quote-aware whitespace tokenizing with no escape processing. A `'` or `"`
// opens a run that ends at its matching close (the quote characters
// themselves are dropped, and whitespace inside the run does not split);
// text outside quotes ends at the next whitespace run. Adjacent
// quoted/unquoted runs with no whitespace between them concatenate into one
// token (`a"b c"d` -> "ab cd"), and an empty quoted pair (`""`) still
// produces one empty token. A nil, empty, or whitespace-only text yields no
// tokens (PS's `@()`). Used by the TUI's extra-args prompt and the --args
// CLI flag.
func SplitArguments(text string) []string {
	if isBlank(text) {
		return nil
	}
	var result []string
	var cur []rune
	var quote rune
	hasToken := false
	flush := func() {
		if len(cur) > 0 || hasToken {
			result = append(result, string(cur))
			cur = cur[:0]
			hasToken = false
		}
	}
	for _, ch := range text {
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				cur = append(cur, ch)
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			hasToken = true
			continue
		}
		if unicode.IsSpace(ch) {
			flush()
			continue
		}
		cur = append(cur, ch)
	}
	flush()
	return result
}

// isBlank mirrors PS's [string]::IsNullOrWhiteSpace — true for "" and any
// string made up entirely of whitespace runes.
func isBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
