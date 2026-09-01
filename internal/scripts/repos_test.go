package scripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

func load(t *testing.T, cfgJSON string) (*config.Config, config.Paths) {
	t.Helper()
	dir := t.TempDir()
	if cfgJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, paths, _, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, paths
}

// Ported from tests/Scripts.Tests.ps1 'multi-repo config' / 'normalizes repo
// entries with per-repo roots and branches'.
func TestReposMultiRepoNormalization(t *testing.T) {
	cfg, paths := load(t, `{
		"repos": [
			{"name": "psrepo", "url": "https://github.com/org/ps-scripts"},
			{"name": "pyrepo", "url": "https://github.com/org/py-scripts", "branch": "dev"}
		]
	}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}
	if repos[0].Root != filepath.Join(paths.ScriptsDir, "psrepo") {
		t.Errorf("repos[0].Root = %q", repos[0].Root)
	}
	if repos[0].Branch != "main" {
		t.Errorf("repos[0].Branch = %q, want default main", repos[0].Branch)
	}
	if repos[1].Branch != "dev" {
		t.Errorf("repos[1].Branch = %q, want dev", repos[1].Branch)
	}
	if repos[0].Legacy {
		t.Error("repos[0].Legacy = true, want false")
	}
}

// Legacy single-repo entry: no `repos` configured -> the legacy repo, even
// with an empty URL (discovery still reads a hand-populated dir).
func TestReposLegacyFallback(t *testing.T) {
	cfg, paths := load(t, `{"scriptsRepo":"https://github.com/org/legacy-scripts","branch":"dev"}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1: %+v", len(repos), repos)
	}
	r := repos[0]
	if r.Name != "scripts" || r.URL != "https://github.com/org/legacy-scripts" || r.Branch != "dev" || r.Root != paths.ScriptsDir || !r.Legacy {
		t.Fatalf("legacy repo = %+v", r)
	}
}

func TestReposLegacyPresentEvenWithEmptyURL(t *testing.T) {
	cfg, paths := load(t, `{}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 || repos[0].URL != "" || !repos[0].Legacy {
		t.Fatalf("repos = %+v, want one legacy repo with empty URL", repos)
	}
}

// SCRIPTS_REPO env overrides cfg.ScriptsRepo for the legacy entry.
func TestReposLegacyEnvOverride(t *testing.T) {
	t.Setenv("SCRIPTS_REPO", "https://github.com/org/env-scripts")
	cfg, paths := load(t, `{"scriptsRepo":"https://github.com/org/cfg-scripts"}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 || repos[0].URL != "https://github.com/org/env-scripts" {
		t.Fatalf("repos = %+v, want env override", repos)
	}
}

func TestReposBadNameSkipped(t *testing.T) {
	cfg, paths := load(t, `{"repos":[{"name":"bad name!","url":"https://github.com/org/a"},{"name":"ok","url":"https://github.com/org/b"}]}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 || repos[0].Name != "ok" {
		t.Fatalf("repos = %+v, want only the valid-named entry", repos)
	}
}

func TestReposEmptyURLSkipped(t *testing.T) {
	cfg, paths := load(t, `{"repos":[{"name":"noturl"},{"name":"ok","url":"https://github.com/org/b"}]}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 || repos[0].Name != "ok" {
		t.Fatalf("repos = %+v, want only the entry with a url", repos)
	}
}

// Default name is the URL basename minus .git.
func TestReposDefaultNameFromURL(t *testing.T) {
	cfg, paths := load(t, `{"repos":[{"url":"https://github.com/org/my-repo.git"}]}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 || repos[0].Name != "my-repo" {
		t.Fatalf("repos = %+v, want default name my-repo", repos)
	}
}

// A literal JSON null repos value: PS wraps it into ONE entry with an
// empty url, so it does NOT fall back to the legacy single repo (the
// `repos.Count -gt 0` PS branch is taken but yields zero real repos).
func TestReposNullReposIsEmptyNotLegacy(t *testing.T) {
	cfg, paths := load(t, `{"repos":null}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 0 {
		t.Fatalf("repos = %+v, want empty (not legacy fallback)", repos)
	}
}

// I1: a bare "repos" string wraps to one all-empty entry the same way — the
// `@($cfg.repos)` wrap law applies to every non-array shape, not just null —
// so this must ALSO not fall back to the legacy single repo.
func TestReposBareStringIsEmptyNotLegacy(t *testing.T) {
	cfg, paths := load(t, `{"repos":"https://example.com/some-url"}`)
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 0 {
		t.Fatalf("repos = %+v, want empty (not legacy fallback)", repos)
	}
}

// ---------------------------------------------------------------------
// AddRepoConfig — ported case-for-case from tests/Core.Tests.ps1
// 'Add-StoRepoConfig'.
// ---------------------------------------------------------------------

func addRepoAppDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfgJSON := `{"dataDir":"` + filepath.Join(dir, "data") + `","scriptsRepo":"https://github.com/org/powershell-scripts"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readConfigJSON(t *testing.T, appDir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(appDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("config.json is not valid JSON: %v\n%s", err, data)
	}
	return m
}

func TestAddRepoConfigConvertsLegacyAndAppends(t *testing.T) {
	appDir := addRepoAppDir(t)
	ok, _, name := scripts.AddRepoConfig(appDir, "https://github.com/org/python-scripts", "python", "")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if name != "python" {
		t.Errorf("resolvedName = %q, want python", name)
	}
	m := readConfigJSON(t, appDir)
	repos, _ := m["repos"].([]any)
	if len(repos) != 2 {
		t.Fatalf("repos = %v, want 2 entries", repos)
	}
	r0 := repos[0].(map[string]any)
	if r0["url"] != "https://github.com/org/powershell-scripts" {
		t.Errorf("repos[0].url = %v", r0["url"])
	}
	r1 := repos[1].(map[string]any)
	if r1["name"] != "python" {
		t.Errorf("repos[1].name = %v", r1["name"])
	}

	// reload and confirm scripts.Repos sees both, non-legacy
	cfg, paths, _, err := config.Load(appDir)
	if err != nil {
		t.Fatal(err)
	}
	got := scripts.Repos(cfg, paths)
	if len(got) != 2 || got[0].Legacy {
		t.Fatalf("scripts.Repos = %+v, want 2 non-legacy repos", got)
	}
}

func TestAddRepoConfigDerivesNameFromURL(t *testing.T) {
	appDir := addRepoAppDir(t)
	ok, _, name := scripts.AddRepoConfig(appDir, "https://github.com/org/python-scripts.git", "", "")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if name != "python-scripts" {
		t.Errorf("resolvedName = %q, want python-scripts", name)
	}
}

func TestAddRepoConfigRejectsDuplicateNameAndURL(t *testing.T) {
	appDir := addRepoAppDir(t)
	if ok, msg, _ := scripts.AddRepoConfig(appDir, "https://github.com/org/a", "x", ""); !ok {
		t.Fatalf("first add should succeed: %s", msg)
	}
	if ok, _, _ := scripts.AddRepoConfig(appDir, "https://github.com/org/b", "x", ""); ok {
		t.Fatal("duplicate name should be rejected")
	}
	if ok, _, _ := scripts.AddRepoConfig(appDir, "https://github.com/org/a.git", "y", ""); ok {
		t.Fatal("normalized-duplicate URL should be rejected")
	}
}

// I2: duplicate-URL detection is case-insensitive, matching PS's default
// `-eq` string comparison in Add-StoRepoConfig.
func TestAddRepoConfigRejectsDuplicateURLCaseInsensitive(t *testing.T) {
	appDir := addRepoAppDir(t)
	if ok, msg, _ := scripts.AddRepoConfig(appDir, "https://GitHub.com/org/a", "x", ""); !ok {
		t.Fatalf("first add should succeed: %s", msg)
	}
	if ok, _, _ := scripts.AddRepoConfig(appDir, "https://github.COM/ORG/A", "y", ""); ok {
		t.Fatal("case-variant duplicate URL should be rejected")
	}
}

func TestAddRepoConfigRejectsInvalidName(t *testing.T) {
	appDir := addRepoAppDir(t)
	ok, msg, _ := scripts.AddRepoConfig(appDir, "https://github.com/org/c", "bad name!", "")
	if ok {
		t.Fatal("expected ok=false for invalid name")
	}
	if !strings.Contains(msg, "invalid repo name") {
		t.Errorf("message = %q", msg)
	}
}

// Unknown top-level keys in config.json must survive the read-modify-write.
func TestAddRepoConfigPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	cfgJSON := `{"dataDir":"` + filepath.Join(dir, "data") + `","someFutureKey":{"nested":42},"otherKey":"value"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, msg, _ := scripts.AddRepoConfig(dir, "https://github.com/org/a", "a", "")
	if !ok {
		t.Fatalf("expected ok=true: %s", msg)
	}
	m := readConfigJSON(t, dir)
	if m["otherKey"] != "value" {
		t.Errorf("otherKey = %v, want preserved", m["otherKey"])
	}
	nested, _ := m["someFutureKey"].(map[string]any)
	if nested["nested"] != float64(42) {
		t.Errorf("someFutureKey.nested = %v, want preserved", nested["nested"])
	}
}
