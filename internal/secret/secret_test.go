package secret_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

// Ported from tests/Core.Tests.ps1 'Register-StoSecret / Hide-StoSecret'.
func TestNamePatternGate(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("MY_API_TOKEN", "supersecret123", false)
	if got := r.Redact("the value is supersecret123 ok"); got != "the value is *** ok" {
		t.Fatalf("got %q", got)
	}
	r.Add("GREETING", "hello-world-value", false)
	if got := r.Redact("hello-world-value"); got != "hello-world-value" {
		t.Fatalf("non-secret-looking name must not register without force: %q", got)
	}
	r.Add("GREETING", "forced-secret-value", true)
	if got := r.Redact("x forced-secret-value y"); got != "x *** y" {
		t.Fatalf("force must register any name: %q", got)
	}
}

// I1: PS -notmatch is case-insensitive, so a lowercase secret-ish name must
// still register, and a lowercase non-secret-ish name must still not.
func TestNamePatternGateCaseInsensitive(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("github_token", "lowercase-name-val", false)
	if got := r.Redact("x lowercase-name-val y"); got != "x *** y" {
		t.Fatalf("lowercase TOKEN-ish name must register: %q", got)
	}
	r.Add("greeting", "still-not-secret-value", false)
	if got := r.Redact("still-not-secret-value"); got != "still-not-secret-value" {
		t.Fatalf("non-secret-looking lowercase name must not register: %q", got)
	}
}

func TestShortValuesIgnored(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("SHORT_TOKEN", "abc", true)
	if got := r.Redact("abc"); got != "abc" {
		t.Fatalf("values under 8 chars must not register: %q", got)
	}
}

func TestBroadenedNamePatterns(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("DB_CONN", "connstring-value-1", false)
	r.Add("SMTP_PASS", "smtppass-value-22", false)
	if got := r.Redact("connstring-value-1 smtppass-value-22"); got != "*** ***" {
		t.Fatalf("got %q", got)
	}
}

// Spec §3: longest-first replacement — a short secret that is a prefix of a
// longer one must not mangle the longer replacement.
func TestLongestFirst(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("A_TOKEN", "secretvalue", true)
	r.Add("B_TOKEN", "secretvalue-extended", true)
	if got := r.Redact("use secretvalue-extended here"); got != "use *** here" {
		t.Fatalf("got %q (short-first replacement would leave '***-extended')", got)
	}
}

func TestDuplicateAddIsIdempotent(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("X_KEY", "duplicated-value", true)
	r.Add("Y_KEY", "duplicated-value", true)
	if got := r.Redact("duplicated-value"); got != "***" {
		t.Fatalf("got %q", got)
	}
}

func TestLineWriterRedacts(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("T_TOKEN", "writer-secret-1", true)
	var buf bytes.Buffer
	w := r.LineWriter(&buf)
	if _, err := w.Write([]byte("a writer-secret-1 b\nsecond writer-secret-1\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := "a *** b\nsecond ***\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

// flakyWriter errors on its call index `failOn`, then succeeds on every
// other call.
type flakyWriter struct {
	buf    bytes.Buffer
	failOn int
	calls  int
}

func (f *flakyWriter) Write(p []byte) (int, error) {
	defer func() { f.calls++ }()
	if f.calls == f.failOn {
		return 0, errors.New("boom")
	}
	return f.buf.Write(p)
}

// M7: a write error partway through a batch must not cause an
// already-flushed line to re-emit on a later successful flush.
func TestLineWriterErrorDoesNotDuplicateFlushedLine(t *testing.T) {
	r := secret.NewRegistry()
	r.Add("T_TOKEN", "line-secret-val", true)
	fw := &flakyWriter{failOn: 1} // line1 flushes fine (call 0), line2 fails (call 1)
	w := r.LineWriter(fw)
	if _, err := w.Write([]byte("line-secret-val one\nline-secret-val two\n")); err == nil {
		t.Fatal("expected a write error")
	}
	fw.failOn = -1 // stop failing
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := "*** one\n*** two\n"
	if fw.buf.String() != want {
		t.Fatalf("got %q, want %q (line one must not repeat)", fw.buf.String(), want)
	}
}

func TestConcurrentAddAndRedact(t *testing.T) {
	r := secret.NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); r.Add("K_TOKEN", strings.Repeat("x", 8+i%5)+"suffix", true) }(i)
		go func() { defer wg.Done(); _ = r.Redact("xxxxxxxxsuffix and text") }()
	}
	wg.Wait()
}
