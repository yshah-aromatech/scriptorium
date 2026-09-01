package scripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

// Ported semantics of tests/Scripts.Tests.ps1 'Get-StoScriptDetail': readme
// capped at 16KB with a truncation marker, and redacted.
func TestGetDetailReadmeCapTruncationAndRedaction(t *testing.T) {
	dir := t.TempDir()
	secretVal := "supersecretvalue123"
	// the secret sits near the START so it survives truncation and gives
	// redaction something real to catch; filler pushes the file past 16KB.
	content := "MY_TOKEN=" + secretVal + "\n" + strings.Repeat("a", 20*1024)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := secret.NewRegistry()
	reg.Add("MY_TOKEN", secretVal, false)

	s := scripts.Script{Name: "x", Dir: dir, EnvFile: filepath.Join(dir, ".env"), EnvExample: filepath.Join(dir, ".env.example")}
	d := scripts.GetDetail(s, reg)

	if !strings.HasSuffix(d.Readme, "\n[truncated]") {
		t.Fatalf("missing truncation marker, tail: %q", tail(d.Readme, 30))
	}
	if len(d.Readme) > 16*1024+len("\n[truncated]") {
		t.Fatalf("readme not capped: len=%d", len(d.Readme))
	}
	if strings.Contains(d.Readme, secretVal) {
		t.Fatal("secret leaked unredacted in readme")
	}
	if !strings.Contains(d.Readme, "***") {
		t.Fatal("expected the secret's redaction marker in readme")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// A README.md that fits comfortably under the cap passes through unmodified
// (besides redaction), with no truncation marker.
func TestGetDetailReadmeUnderCapPassesThrough(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hello\nsmall doc"), 0o644); err != nil { // lower-case name: case-insensitive match
		t.Fatal(err)
	}
	reg := secret.NewRegistry()
	s := scripts.Script{Name: "x", Dir: dir, EnvFile: filepath.Join(dir, ".env"), EnvExample: filepath.Join(dir, ".env.example")}
	d := scripts.GetDetail(s, reg)
	if d.Readme != "# hello\nsmall doc" {
		t.Fatalf("readme = %q", d.Readme)
	}
}

// Loose root scripts get no readme, even when the repo root has one — Dir
// is the whole repo root for a loose script, and shipping the repo's README
// as one script's doc would mislead an agent.
func TestGetDetailLooseScriptGetsNoReadme(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("repo readme"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := secret.NewRegistry()
	s := scripts.Script{Name: "loose", Dir: dir, Loose: true, EnvFile: filepath.Join(dir, "loose.env"), EnvExample: filepath.Join(dir, "loose.env.example")}
	d := scripts.GetDetail(s, reg)
	if d.Readme != "" {
		t.Fatalf("loose script must get no readme, got %q", d.Readme)
	}
}

func TestGetDetailNoReadmeFile(t *testing.T) {
	dir := t.TempDir()
	reg := secret.NewRegistry()
	s := scripts.Script{Name: "x", Dir: dir, EnvFile: filepath.Join(dir, ".env"), EnvExample: filepath.Join(dir, ".env.example")}
	d := scripts.GetDetail(s, reg)
	if d.Readme != "" {
		t.Fatalf("readme = %q, want empty", d.Readme)
	}
}

// env doc + configured ordering: EnvExample carries the .env.example docs;
// EnvConfigured is .env's key names, deduped in first-appearance order.
func TestGetDetailEnvDocAndConfiguredOrdering(t *testing.T) {
	dir := t.TempDir()
	example := "# tenant id\nTENANT=default\n\nOTHER=1\n"
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	envContent := "K=1\nA=2\nK=3\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := secret.NewRegistry()
	s := scripts.Script{Name: "x", Dir: dir, EnvFile: filepath.Join(dir, ".env"), EnvExample: filepath.Join(dir, ".env.example")}
	d := scripts.GetDetail(s, reg)

	if len(d.EnvExample) != 2 || d.EnvExample[0].Key != "TENANT" || d.EnvExample[0].Comment != "tenant id" || d.EnvExample[0].Default != "default" {
		t.Fatalf("EnvExample = %+v", d.EnvExample)
	}
	want := []string{"K", "A"}
	if len(d.EnvConfigured) != len(want) {
		t.Fatalf("EnvConfigured = %v, want %v", d.EnvConfigured, want)
	}
	for i := range want {
		if d.EnvConfigured[i] != want[i] {
			t.Errorf("EnvConfigured[%d] = %q, want %q", i, d.EnvConfigured[i], want[i])
		}
	}
}

// Field passthrough + JSON shape (MCP spellings).
func TestGetDetailFieldsAndJSONShape(t *testing.T) {
	dir := t.TempDir()
	timeout := 12.5
	s := scripts.Script{
		Name: "myscript", Description: "does things", Runtime: "python", Repo: "myrepo",
		Dir: dir, Entry: filepath.Join(dir, "main.py"), TimeoutMinutes: &timeout,
		Args:       []string{"--flag", "value"},
		EnvFile:    filepath.Join(dir, ".env"),
		EnvExample: filepath.Join(dir, ".env.example"),
	}
	reg := secret.NewRegistry()
	d := scripts.GetDetail(s, reg)

	if d.Name != "myscript" || d.Description != "does things" || d.Runtime != "python" || d.Repo != "myrepo" {
		t.Fatalf("d = %+v", d)
	}
	if d.Entry != "main.py" {
		t.Errorf("Entry = %q, want basename main.py", d.Entry)
	}
	if d.TimeoutMinutes == nil || *d.TimeoutMinutes != 12.5 {
		t.Errorf("TimeoutMinutes = %v", d.TimeoutMinutes)
	}
	if len(d.DefaultArgs) != 2 || d.DefaultArgs[0] != "--flag" {
		t.Errorf("DefaultArgs = %v", d.DefaultArgs)
	}

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "description", "runtime", "repo", "entry", "timeoutMinutes", "defaultArgs", "readme", "envExample", "envConfigured"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing key %q: %s", key, raw)
		}
	}
}

func TestGetDetailEmptyDefaultArgsAndEnvAreEmptyArraysNotNull(t *testing.T) {
	dir := t.TempDir()
	s := scripts.Script{Name: "x", Dir: dir, EnvFile: filepath.Join(dir, ".env"), EnvExample: filepath.Join(dir, ".env.example")}
	reg := secret.NewRegistry()
	d := scripts.GetDetail(s, reg)

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"defaultArgs":[]`, `"envExample":[]`, `"envConfigured":[]`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("expected %s in JSON, got %s", key, raw)
		}
	}
}
