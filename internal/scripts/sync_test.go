package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFixtureRemote git-inits a local "remote" repo with a script folder and
// a tracked file, on branch main.
func newFixtureRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, filepath.Join(dir, "myscript", "main.ps1"), "Write-Output 1")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "v1")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func loadWithDataDir(t *testing.T, extraJSON string) (*config.Config, config.Paths) {
	t.Helper()
	appDir := t.TempDir()
	data := filepath.Join(appDir, "data")
	cfgJSON := `{"dataDir":"` + data + `"` + extraJSON + `}`
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, paths, _, err := config.Load(appDir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, paths
}

func containsLine(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// Full lifecycle against a local git fixture: clone, then a re-sync that
// hard-resets to an upstream change while a local untracked .env survives
// and a stray untracked file gets cleaned, then a broken remote fails
// loudly. Ported semantics of tests/Scripts.Tests.ps1's sync coverage plus
// the Task 2 brief's explicit local-fixture scenario.
func TestSyncCloneResetEnvSurvivalAndFailure(t *testing.T) {
	remote := newFixtureRemote(t)
	cfg, paths := loadWithDataDir(t, `,"repos":[{"name":"myrepo","url":"`+remote+`"}]`)
	reg := secret.NewRegistry()

	// --- clone ---
	var lines []string
	ok := scripts.Sync(cfg, paths, reg, func(l string) { lines = append(lines, l) })
	if !ok {
		t.Fatalf("clone sync failed: %v", lines)
	}
	if !containsLine(lines, "[myrepo] cloning "+remote+" (branch main)...") {
		t.Errorf("missing clone line: %v", lines)
	}
	if !containsLine(lines, "[myrepo] sync complete") {
		t.Errorf("missing complete line: %v", lines)
	}
	repos := scripts.Repos(cfg, paths)
	if len(repos) != 1 {
		t.Fatalf("repos = %+v", repos)
	}
	root := repos[0].Root
	if data, err := os.ReadFile(filepath.Join(root, "myscript", "main.ps1")); err != nil || string(data) != "Write-Output 1" {
		t.Fatalf("clone missing script: %v %q", err, data)
	}

	// --- upstream change + local untracked files, then re-sync ---
	writeFile(t, filepath.Join(remote, "tracked.txt"), "v2")
	runGit(t, remote, "commit", "-am", "update")
	writeFile(t, filepath.Join(root, "myscript", ".env"), "SECRET=abc")
	writeFile(t, filepath.Join(root, "junk.txt"), "should be cleaned")

	lines = nil
	ok = scripts.Sync(cfg, paths, reg, func(l string) { lines = append(lines, l) })
	if !ok {
		t.Fatalf("re-sync failed: %v", lines)
	}
	if !containsLine(lines, "[myrepo] syncing "+remote+" (hard reset to origin/main)...") {
		t.Errorf("missing syncing line: %v", lines)
	}
	if !containsLine(lines, "[myrepo] sync complete") {
		t.Errorf("missing complete line: %v", lines)
	}
	if data, err := os.ReadFile(filepath.Join(root, "tracked.txt")); err != nil || string(data) != "v2" {
		t.Fatalf("reset did not pick up upstream change: %v %q", err, data)
	}
	if _, err := os.Stat(filepath.Join(root, "myscript", ".env")); err != nil {
		t.Fatalf(".env did not survive the reset/clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "junk.txt")); !os.IsNotExist(err) {
		t.Fatalf("junk.txt should have been cleaned, err=%v", err)
	}

	// --- broken remote ---
	badCfg, badPaths := loadWithDataDir(t, `,"repos":[{"name":"myrepo","url":"`+filepath.Join(t.TempDir(), "does-not-exist")+`"}]`)
	// point the broken config at the SAME already-cloned root so the sync
	// path (not clone) runs and fails at `fetch`
	badRepos := scripts.Repos(badCfg, badPaths)
	if err := os.MkdirAll(filepath.Dir(badRepos[0].Root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, badRepos[0].Root); err != nil {
		t.Fatal(err)
	}
	lines = nil
	ok = scripts.Sync(badCfg, badPaths, reg, func(l string) { lines = append(lines, l) })
	if ok {
		t.Fatalf("expected sync to fail against a broken remote: %v", lines)
	}
	if !containsLine(lines, "[myrepo] git fetch failed (exit") {
		t.Errorf("missing fetch-failed line: %v", lines)
	}
	if !containsLine(lines, "[myrepo] sync FAILED — check GITHUB_TOKEN in .env") {
		t.Errorf("missing FAILED line: %v", lines)
	}
}

func TestSyncNoRepoConfigured(t *testing.T) {
	cfg, paths := loadWithDataDir(t, "")
	reg := secret.NewRegistry()
	var lines []string
	ok := scripts.Sync(cfg, paths, reg, func(l string) { lines = append(lines, l) })
	if ok {
		t.Fatal("expected false with no url configured")
	}
	want := "no scripts repo configured — set `repos` (or scriptsRepo) in config.json, or SCRIPTS_REPO in .env"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines = %v, want [%q]", lines, want)
	}
}

// The GITHUB_TOKEN-injected clone URL must never appear unredacted in any
// emitted line, and must be registered as a secret before any git call.
// The remote is an unreachable https URL (loopback, closed port — no DNS,
// fails fast) so git's own output has a real chance to echo the URL back;
// redaction must still catch it.
func TestSyncRedactsInjectedTokenURL(t *testing.T) {
	httpsURL := "https://127.0.0.1:1/org/repo" // https, no '@' -> eligible for injection
	cfg, paths := loadWithDataDir(t, `,"repos":[{"name":"tokrepo","url":"`+httpsURL+`"}]`)
	t.Setenv("GITHUB_TOKEN", "supersecrettoken123")
	reg := secret.NewRegistry()
	repos := scripts.Repos(cfg, paths)
	var lines []string
	ok := scripts.SyncOne(repos[0], reg, func(l string) { lines = append(lines, l) })
	if ok {
		t.Fatal("expected the clone against an unreachable host to fail")
	}
	for _, l := range lines {
		if strings.Contains(l, "supersecrettoken123") {
			t.Fatalf("token leaked unredacted: %q", l)
		}
		if strings.Contains(l, "x-access-token:supersecrettoken123@") {
			t.Fatalf("injected URL leaked unredacted: %q", l)
		}
	}
}

// Ported from tests/Scripts.Tests.ps1 'migrates a legacy root-level clone
// into the first repo subdir', extended to prove remote-URL matching (not
// just "the first repo") picks the right target.
func TestMigrateLayoutMatchesByRemoteURL(t *testing.T) {
	remoteA := newFixtureRemote(t)
	remoteB := newFixtureRemote(t)
	cfg, paths := loadWithDataDir(t, `,"repos":[{"name":"repoa","url":"`+remoteA+`"},{"name":"repob","url":"`+remoteB+`"}]`)

	// simulate a pre-migration root-level clone whose origin is remoteB —
	// migration must pick repob, not the first configured repo (repoa).
	if err := os.MkdirAll(paths.ScriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, paths.ScriptsDir, "init", "-b", "main")
	runGit(t, paths.ScriptsDir, "remote", "add", "origin", remoteB)
	writeFile(t, filepath.Join(paths.ScriptsDir, "oldscript", "main.ps1"), "x")

	repos := scripts.Repos(cfg, paths)
	var lines []string
	scripts.MigrateLayout(repos, paths, func(l string) { lines = append(lines, l) })

	if _, err := os.Stat(filepath.Join(paths.ScriptsDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("root .git should be gone after migration, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ScriptsDir, "repob", ".git")); err != nil {
		t.Fatalf("expected migrated clone under repob (remote-url match): %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ScriptsDir, "repob", "oldscript", "main.ps1")); err != nil {
		t.Fatalf("expected oldscript to move with the clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ScriptsDir, "repoa")); !os.IsNotExist(err) {
		t.Fatalf("repoa must not be used (remote matched repob), err=%v", err)
	}
	if !containsLine(lines, "migrating scripts clone to multi-repo layout: scripts/ -> scripts/repob/") {
		t.Errorf("missing migration line: %v", lines)
	}
}

// I2: remote-URL matching is case-insensitive, matching PS's default `-eq`
// string comparison in Update-StoRepoLayout.
func TestMigrateLayoutMatchesByRemoteURLCaseInsensitive(t *testing.T) {
	remote := newFixtureRemote(t)
	upper := strings.ToUpper(remote)
	cfg, paths := loadWithDataDir(t, `,"repos":[{"name":"repoa","url":"`+upper+`"}]`)

	if err := os.MkdirAll(paths.ScriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, paths.ScriptsDir, "init", "-b", "main")
	runGit(t, paths.ScriptsDir, "remote", "add", "origin", remote) // lower-case remote; configured repo URL is upper-cased
	writeFile(t, filepath.Join(paths.ScriptsDir, "oldscript", "main.ps1"), "x")

	repos := scripts.Repos(cfg, paths)
	var lines []string
	scripts.MigrateLayout(repos, paths, func(l string) { lines = append(lines, l) })

	if _, err := os.Stat(filepath.Join(paths.ScriptsDir, "repoa", ".git")); err != nil {
		t.Fatalf("expected migrated clone under repoa (case-insensitive remote-url match): %v", err)
	}
	if !containsLine(lines, "migrating scripts clone to multi-repo layout: scripts/ -> scripts/repoa/") {
		t.Errorf("missing migration line: %v", lines)
	}
}

func TestMigrateLayoutSkippedForLegacy(t *testing.T) {
	cfg, paths := loadWithDataDir(t, "")
	repos := scripts.Repos(cfg, paths)
	if !repos[0].Legacy {
		t.Fatal("expected legacy repo")
	}
	if err := os.MkdirAll(filepath.Join(paths.ScriptsDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	scripts.MigrateLayout(repos, paths, func(l string) { lines = append(lines, l) })
	if len(lines) != 0 {
		t.Fatalf("expected no migration for a legacy layout: %v", lines)
	}
}

func TestLastSyncTimeZeroWhenNone(t *testing.T) {
	_, paths := loadWithDataDir(t, "")
	repos := []scripts.Repo{{Name: "x", Root: filepath.Join(paths.ScriptsDir, "x")}}
	if got := scripts.LastSyncTime(repos); !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

func TestLastSyncTimeUsesFetchHead(t *testing.T) {
	_, paths := loadWithDataDir(t, "")
	root := filepath.Join(paths.ScriptsDir, "x")
	writeFile(t, filepath.Join(root, ".git", "FETCH_HEAD"), "x")
	repos := []scripts.Repo{{Name: "x", Root: root}}
	if got := scripts.LastSyncTime(repos); got.IsZero() {
		t.Fatal("expected a non-zero time")
	}
}
