package envfile_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/envfile"
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

// fixtureDir walks up to testdata/psfixtures (same idiom as internal/psfixtures).
func fixtureDir(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(d, "testdata", "psfixtures")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("testdata/psfixtures not found")
		}
		d = parent
	}
}
