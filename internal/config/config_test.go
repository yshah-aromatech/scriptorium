package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

func appDirWith(t *testing.T, cfgJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if cfgJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDefaults(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, paths, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	// spot-check the defaults table (full table asserted below)
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Branch", cfg.Branch, "main"}, {"PwshBin", cfg.PwshBin, "pwsh"},
		{"PythonBin", cfg.PythonBin, "python3"}, {"MonitorIntervalMs", cfg.MonitorIntervalMs, 1000},
		{"LogTailKb", cfg.LogTailKb, 64}, {"RunTimeoutMinutes", cfg.RunTimeoutMinutes, 0.0},
		{"MaxOutputLines", cfg.MaxOutputLines, 5000},
		{"OpenRouterModel", cfg.OpenRouterModel, "google/gemini-3.1-flash-lite"},
		{"SyncOnLaunch", cfg.SyncOnLaunch, false}, {"LogRetentionDays", cfg.LogRetentionDays, 30.0},
		{"HistoryMaxLines", cfg.HistoryMaxLines, 50000}, {"HistoryDays", cfg.HistoryDays, 30.0},
		{"WebhookTimeoutSec", cfg.WebhookTimeoutSec, 15}, {"MissedGraceMinutes", cfg.MissedGraceMinutes, 5.0},
		{"ColorMode", cfg.ColorMode, "auto"}, {"McpPort", cfg.McpPort, 8765}, {"McpBind", cfg.McpBind, "all"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	for _, d := range []string{paths.DataDir, paths.ModulesDir, paths.LogsDir, paths.LocksDir, paths.VenvsDir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("directory not created: %s (%v)", d, err)
		}
	}
	if paths.HistoryFile != filepath.Join(data, "history.jsonl") {
		t.Errorf("HistoryFile = %q", paths.HistoryFile)
	}
	if paths.WebhookQueueFile != filepath.Join(data, "webhook-queue.jsonl") {
		t.Errorf("WebhookQueueFile = %q", paths.WebhookQueueFile)
	}
}

// warnings.txt fixture rows are "<file>\t<warning>" — byte-identical contract.
func TestWarningsMatchPSFixtures(t *testing.T) {
	dir := fixtureDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "config-corpus", "warnings.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantByFile := map[string][]string{}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		wantByFile[parts[0]] = append(wantByFile[parts[0]], parts[1])
	}
	for _, file := range []string{"valid.json", "unknown-key.json", "bad-numeric.json", "legacy-repo.json"} {
		t.Run(file, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(dir, "config-corpus", file))
			if err != nil {
				t.Fatal(err)
			}
			// corpus files carry dataDir "~/x" — rewrite to a temp dir so the
			// test never touches the real home (mirrors the generator's ruling)
			data := filepath.Join(t.TempDir(), "data")
			cfgJSON := strings.Replace(string(src), `"~/x"`, `"`+data+`"`, 1)
			_, _, warns, err := config.Load(appDirWith(t, cfgJSON))
			if err != nil {
				t.Fatal(err)
			}
			want := wantByFile[file]
			if len(warns) != len(want) {
				t.Fatalf("got %d warnings %v, want %d %v", len(warns), warns, len(want), want)
			}
			for i := range want {
				if warns[i] != want[i] {
					t.Errorf("warning %d:\n got  %q\n want %q", i, warns[i], want[i])
				}
			}
		})
	}
}

func TestBadNumericKeepsDefault(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","runTimeoutMinutes":"lots"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || cfg.RunTimeoutMinutes != 0 {
		t.Fatalf("warns=%v RunTimeoutMinutes=%v", warns, cfg.RunTimeoutMinutes)
	}
}

// PS's `-as [double]` cast accepts a quoted numeric string, so a
// numeric-gated key given as e.g. "5" must be silently honored, not warned.
func TestQuotedNumericStringIsAccepted(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","runTimeoutMinutes":"5"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 || cfg.RunTimeoutMinutes != 5.0 {
		t.Fatalf("warns=%v RunTimeoutMinutes=%v", warns, cfg.RunTimeoutMinutes)
	}
}

func TestQuotedNumericStringMcpPort(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","mcpPort":"9443"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 || cfg.McpPort != 9443 {
		t.Fatalf("warns=%v McpPort=%v", warns, cfg.McpPort)
	}
}

func TestInvalidJSONIsHardError(t *testing.T) {
	_, _, _, err := config.Load(appDirWith(t, `{not json`))
	if err == nil || !strings.Contains(err.Error(), "config.json is not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingConfigUsesDefaultsAndTildeExpansion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, paths, warns, err := config.Load(appDirWith(t, ""))
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if !strings.HasPrefix(paths.DataDir, os.Getenv("HOME")) || !strings.HasSuffix(paths.DataDir, ".scriptorium") {
		t.Fatalf("DataDir = %q", paths.DataDir)
	}
}

func TestLoadAppEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PHASE1_TEST_VAR=fromfile\nPHASE1_TEST_TOKEN=filesecret99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHASE1_TEST_VAR", "fromprocess") // existing process env wins
	t.Setenv("PHASE1_TEST_TOKEN", "")
	os.Unsetenv("PHASE1_TEST_TOKEN")
	reg := secret.NewRegistry()
	if err := config.LoadAppEnv(dir, reg); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PHASE1_TEST_VAR"); got != "fromprocess" {
		t.Errorf("existing env must win: %q", got)
	}
	if got := os.Getenv("PHASE1_TEST_TOKEN"); got != "filesecret99" {
		t.Errorf("missing env set from file: %q", got)
	}
	if got := reg.Redact("x filesecret99 y"); got != "x *** y" {
		t.Errorf("TOKEN-named value must register: %q", got)
	}
	t.Cleanup(func() { os.Unsetenv("PHASE1_TEST_TOKEN") })
}

// I1: "repos" decode is per-entry and PS-shaped — @($cfg.repos) wraps EVERY
// non-array shape (object, string, number, null) into a one-element array,
// and every array element decodes independently AND SURVIVES — a malformed
// sibling never sinks the whole list; it decodes to a zero-valued entry
// (verified against live pwsh: this exact mixed array yields 3 raw repos
// entries, the bare string producing an all-empty one).
func TestReposDecodeMixedArray(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, _, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":[{"name":"a","url":"u"},"bare-string",{"url":""}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 3 {
		t.Fatalf("got %d repos %+v, want 3 (the bare string survives as a zero-valued entry)", len(cfg.Repos), cfg.Repos)
	}
	if cfg.Repos[0].Name != "a" || cfg.Repos[0].URL != "u" {
		t.Errorf("repo 0 = %+v", cfg.Repos[0])
	}
	if cfg.Repos[1] != (config.RepoEntry{}) {
		t.Errorf("repo 1 (from the bare string) = %+v, want zero-valued", cfg.Repos[1])
	}
	if cfg.Repos[2].URL != "" {
		t.Errorf("repo 2 = %+v", cfg.Repos[2])
	}
}

// A bare "repos" string (not even an object) still wraps to ONE entry —
// every field ends up empty (a string has no .name/.url properties in PS
// either), so it warns for the missing url and, since decodeRepos returned a
// non-empty slice, scripts.Repos must NOT fall back to the legacy repo (see
// scripts.TestReposBareStringIsEmptyNotLegacy).
func TestReposDecodeBareStringWrapsToOneEmptyEntry(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":"https://example.com/some-url"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != (config.RepoEntry{}) {
		t.Fatalf("got %+v, want one zero-valued entry", cfg.Repos)
	}
	want := "config.json: repos entry missing 'url' — skipped"
	if len(warns) != 1 || warns[0] != want {
		t.Fatalf("warns = %v, want [%q]", warns, want)
	}
}

func TestReposDecodeSingleObjectWraps(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, _, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":{"name":"a","url":"u"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Name != "a" || cfg.Repos[0].URL != "u" {
		t.Fatalf("got %+v, want one repo {a, u}", cfg.Repos)
	}
}

// I1 (P4 correction of the P1-era assumption): a repos-entry field type
// mismatch does NOT drop the entry, and a numeric url is stringified rather
// than zeroed — matching PS's "$($e.url)" interpolation exactly (verified
// against live pwsh: Get-StoRepos on this config yields one repo
// {Name:"a", Url:"123", Branch:"main"}).
func TestReposDecodeNumericURLIsStringified(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, _, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":[{"name":"a","url":123}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Name != "a" || cfg.Repos[0].URL != "123" {
		t.Fatalf("got %+v, want one repo {Name:a, URL:123}", cfg.Repos)
	}
}

// M2: a leading UTF-8 BOM must not break JSON decoding, and trailing
// garbage after the top-level object must still be a hard error.
func TestConfigJSONStripsLeadingBOM(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("\ufeff{\"dataDir\":\""+data+"\"}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, warns, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if cfg.DataDir != data {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, data)
	}
}

func TestConfigJSONTrailingGarbageIsHardError(t *testing.T) {
	_, _, _, err := config.Load(appDirWith(t, `{"mcpPort":1} trailing`))
	if err == nil || !strings.Contains(err.Error(), "config.json is not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

// M3: numeric-key int conversion rounds half-to-even like PS's [int] cast,
// not truncates.
func TestNumericIntConversionRoundsToEven(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","mcpPort":8765.7}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 || cfg.McpPort != 8766 {
		t.Fatalf("warns=%v McpPort=%v, want 8766", warns, cfg.McpPort)
	}
}

// M4: non-finite and out-of-int32-range numeric values must warn and keep
// the default rather than producing undefined int-conversion garbage.
func TestNumericRejectsNonFiniteAndOverflow(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","mcpPort":"Infinity"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || cfg.McpPort != 8765 {
		t.Fatalf("warns=%v McpPort=%v, want 1 warning and default 8765", warns, cfg.McpPort)
	}

	data2 := filepath.Join(t.TempDir(), "data")
	cfg2, _, warns2, err := config.Load(appDirWith(t, `{"dataDir":"`+data2+`","mcpPort":1e300}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns2) != 1 || cfg2.McpPort != 8765 {
		t.Fatalf("warns=%v McpPort=%v, want 1 warning and default 8765", warns2, cfg2.McpPort)
	}
}

// M5: config key matching is case-insensitive, mirroring PS's [ordered]
// hashtable lookup semantics.
func TestConfigKeyMatchingIsCaseInsensitive(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg, _, warns, err := config.Load(appDirWith(t, `{"DataDir":"`+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if cfg.DataDir != data {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, data)
	}
}

// Phase 4 carry: config.Load's repos entry validation warnings, appended
// after key warnings (PS order: key warnings -> repos), ported from
// src/Core.psm1's Initialize-Sto repos-sanity block. Entries stay in
// cfg.Repos — normalization skipping is scripts.Repos' concern; these
// warnings are advisory PS parity only.
func TestReposWarningMissingURL(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	_, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":[{"name":"a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "config.json: repos entry missing 'url' — skipped"
	if len(warns) != 1 || warns[0] != want {
		t.Fatalf("warns = %v, want [%q]", warns, want)
	}
}

func TestReposWarningBadName(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	_, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":[{"name":"bad name!","url":"https://x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "config.json: repos entry name 'bad name!' must match [A-Za-z0-9_-]+ — skipped"
	if len(warns) != 1 || warns[0] != want {
		t.Fatalf("warns = %v, want [%q]", warns, want)
	}
}

// A literal JSON null "repos" value is PS's `@($cfg.repos)` wrapping $null
// into a single-element array — one entry, empty url, warns once.
func TestReposWarningNullReposWarnsOnce(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	_, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":null}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "config.json: repos entry missing 'url' — skipped"
	if len(warns) != 1 || warns[0] != want {
		t.Fatalf("warns = %v, want [%q]", warns, want)
	}
}

// A single entry can warn twice: missing url AND a bad name, url checked
// first (PS order).
func TestReposWarningBothOnOneEntry(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	_, _, warns, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`","repos":[{"name":"bad!"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"config.json: repos entry missing 'url' — skipped",
		"config.json: repos entry name 'bad!' must match [A-Za-z0-9_-]+ — skipped",
	}
	if len(warns) != len(want) {
		t.Fatalf("warns = %v, want %v", warns, want)
	}
	for i := range want {
		if warns[i] != want[i] {
			t.Errorf("warns[%d] = %q, want %q", i, warns[i], want[i])
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
