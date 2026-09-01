// Package secret is the redaction registry: every value that must never
// appear in logs, webhooks, or tool output is registered here, and Redact
// is the single chokepoint that scrubs them (spec §3). Semantics match
// Register-StoSecret / Hide-StoSecret (src/Core.psm1): values under 8
// characters never register; without force, only names matching the
// secret-ish pattern register. Replacement is longest-first (deterministic;
// a substring secret cannot mangle a longer one's replacement).
package secret

import (
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var namePattern = regexp.MustCompile(`TOKEN|KEY|SECRET|PASSWORD|PASSWD|PASS|PAT|CREDENTIAL|WEBHOOK|AUTH|CONN|DSN|BEARER`)

type Registry struct {
	mu     sync.RWMutex
	values map[string]struct{}
	sorted []string // longest-first snapshot, rebuilt on Add
}

func NewRegistry() *Registry {
	return &Registry{values: map[string]struct{}{}}
}

// Add registers value. force bypasses the name gate (used for per-script
// .env values and per-run env, which are secrets by definition).
func (r *Registry) Add(name, value string, force bool) {
	if len(value) < 8 {
		return
	}
	if !force && name != "" && !namePattern.MatchString(name) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[value]; ok {
		return
	}
	r.values[value] = struct{}{}
	r.sorted = append(r.sorted, value)
	sort.Slice(r.sorted, func(i, j int) bool {
		if len(r.sorted[i]) != len(r.sorted[j]) {
			return len(r.sorted[i]) > len(r.sorted[j])
		}
		return r.sorted[i] < r.sorted[j]
	})
}

// Redact replaces every registered value in s with "***".
func (r *Registry) Redact(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.sorted {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// LineWriter wraps w so every complete line written through it is redacted.
// A trailing partial line is redacted and flushed on Close.
func (r *Registry) LineWriter(w io.Writer) io.WriteCloser {
	return &lineWriter{r: r, w: w}
}

type lineWriter struct {
	r   *Registry
	w   io.Writer
	buf strings.Builder
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf.Write(p)
	s := lw.buf.String()
	for {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		line := s[:idx]
		s = s[idx+1:]
		if _, err := io.WriteString(lw.w, lw.r.Redact(line)+"\n"); err != nil {
			return len(p), err
		}
	}
	lw.buf.Reset()
	lw.buf.WriteString(s)
	return len(p), nil
}

func (lw *lineWriter) Close() error {
	if lw.buf.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(lw.w, lw.r.Redact(lw.buf.String()))
	lw.buf.Reset()
	return err
}
