// Package envfile parses .env files with the exact semantics of the
// PowerShell app's Read-StoEnvFile / Read-StoEnvDoc (src/Core.psm1):
// trim the line; skip blanks and #-comments; the first '=' must be at
// index >= 1; key and value are trimmed; surrounding quotes are stripped
// only when both ends are the same quote character; no escape processing;
// last key wins. A missing file reads as empty, not an error.
package envfile

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DocEntry is one documented key from a .env.example: the KEY=VALUE line
// plus the #-comment block immediately above it (joined with spaces).
type DocEntry struct {
	Key     string
	Default string
	Comment string
}

// Read parses path into key/value pairs. Later duplicate keys overwrite
// earlier ones. A missing file returns an empty, non-nil map.
func Read(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	for _, line := range splitLines(string(data)) {
		k, v, ok := parseLine(line)
		if ok {
			out[k] = stripMatchedQuotes(v)
		}
	}
	return out, nil
}

// ReadDoc parses a .env.example, attaching each key's preceding comment
// block. Blank lines and malformed lines clear the pending comment block,
// matching Read-StoEnvDoc. Values are quote-trimmed using PS semantics
// (.Trim() any count, both quote chars independently from each end).
func ReadDoc(path string) ([]DocEntry, error) {
	var entries []DocEntry
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return entries, nil
		}
		return entries, err
	}
	var pending []string
	for _, raw := range splitLines(string(data)) {
		t := strings.TrimSpace(raw)
		if t == "" {
			pending = nil
			continue
		}
		if strings.HasPrefix(t, "#") {
			c := strings.TrimPrefix(t, "#")
			// strip at most one leading whitespace char (space, tab, etc.),
			// like the PS regex '^#\s?' — \s is not just a space.
			if r, size := utf8.DecodeRuneInString(c); size > 0 && unicode.IsSpace(r) {
				c = c[size:]
			}
			pending = append(pending, c)
			continue
		}
		k, v, ok := parseLine(raw)
		if !ok {
			pending = nil
			continue
		}
		entries = append(entries, DocEntry{Key: k, Default: strings.Trim(v, "\"'"), Comment: strings.Join(pending, " ")})
		pending = nil
	}
	return entries, nil
}

// parseLine implements the shared KEY=VALUE rule, returning the raw trimmed value.
func parseLine(raw string) (key, val string, ok bool) {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	idx := strings.Index(t, "=")
	if idx < 1 {
		return "", "", false
	}
	key = strings.TrimSpace(t[:idx])
	val = strings.TrimSpace(t[idx+1:])
	return key, val, true
}

// stripMatchedQuotes removes outer quotes only if both ends are the same quote character.
func stripMatchedQuotes(val string) string {
	if len(val) >= 2 {
		first, last := val[0], val[len(val)-1]
		if first == last && (first == '\'' || first == '"') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

func splitLines(s string) []string {
	s = strings.TrimPrefix(s, "\ufeff") // strip a leading UTF-8 BOM
	// handle \n and \r\n; a lone trailing newline must not yield a ghost line
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}
