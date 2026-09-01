package envfile_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/envfile"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Ported from tests/Core.Tests.ps1 'Read-StoEnvFile'.
func TestReadBasics(t *testing.T) {
	cases := []struct {
		name, content string
		want          map[string]string
	}{
		{"kv lines", "A=1\nB=two words", map[string]string{"A": "1", "B": "two words"}},
		{"comments and blanks", "# comment\n\nA=1", map[string]string{"A": "1"}},
		{"matched quotes stripped", "A='quoted'\nB=\"dquoted\"", map[string]string{"A": "quoted", "B": "dquoted"}},
		{"equals kept in values", "A=x=y", map[string]string{"A": "x=y"}},
		{"first equals must be at index >= 1", "=noval\nX=ok", map[string]string{"X": "ok"}},
		{"unmatched quotes kept", "A='unbalanced\nB=\"mixed'", map[string]string{"A": "'unbalanced", "B": "\"mixed'"}},
		{"empty quoted value", "E=''", map[string]string{"E": ""}},
		{"whitespace trimmed around key and value", "A = spaced \nB=trail  ", map[string]string{"A": "spaced", "B": "trail"}},
		{"last key wins", "K=first\nK=second", map[string]string{"K": "second"}},
		{"stacked quotes keep inner quotes", "A=\"\"x\"\"", map[string]string{"A": "\"x\""}}, // Read uses matched-pair: outer "" removed, inner remains
		{"trailing quote unmatched", "B=abc\"", map[string]string{"B": "abc\""}},             // no matched pair, quote kept
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := envfile.Read(write(t, c.content))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// M1: a leading UTF-8 BOM must not become part of the first key.
func TestReadStripsLeadingBOM(t *testing.T) {
	got, err := envfile.Read(write(t, "\ufeffA=1\nB=2"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
}

func TestReadMissingFileIsEmptyNotError(t *testing.T) {
	got, err := envfile.Read(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

// The PS-generated corpus is the parity contract.
func TestReadMatchesPSFixtures(t *testing.T) {
	dir := fixtureDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "env-corpus", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]map[string]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	for file, want := range expected {
		t.Run(file, func(t *testing.T) {
			got, err := envfile.Read(filepath.Join(dir, "env-corpus", file))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestReadDoc(t *testing.T) {
	p := write(t, "# API key for the thing\n# second line\nAPI_KEY=abc\n\n# orphan comment reset by blank\n\nPLAIN=1\nnot a kv line\nAFTER=2\n")
	entries, err := envfile.ReadDoc(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []envfile.DocEntry{
		{Key: "API_KEY", Default: "abc", Comment: "API key for the thing second line"},
		{Key: "PLAIN", Default: "1", Comment: ""},
		{Key: "AFTER", Default: "2", Comment: ""}, // pending comments cleared by the malformed line
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries (%v), want %d", len(entries), entries, len(want))
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, entries[i], want[i])
		}
	}
}

// D1: the pending-comment strip must match PS '^#\s?' — one whitespace
// char of any kind (space, tab, ...), not just a literal space.
func TestReadDocTabCommentPrefix(t *testing.T) {
	p := write(t, "#\tTabbed\nKEY=1\n")
	entries, err := envfile.ReadDoc(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Comment != "Tabbed" {
		t.Fatalf("got %+v, want Comment %q", entries, "Tabbed")
	}
}

// TestReadDocQuoteTrimDivergence verifies ReadDoc uses PS .Trim() semantics
// (any count of quote chars trimmed from both ends independently), which
// diverges from Read's matched-pair stripping.
func TestReadDocQuoteTrimDivergence(t *testing.T) {
	p := write(t, "STACKED=\"\"x\"\"\nSTRAY=abc\"\n")
	entries, err := envfile.ReadDoc(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []envfile.DocEntry{
		{Key: "STACKED", Default: "x", Comment: ""}, // ReadDoc trims all quotes: ""x"" -> x
		{Key: "STRAY", Default: "abc", Comment: ""}, // ReadDoc trims trailing quote: abc" -> abc
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
