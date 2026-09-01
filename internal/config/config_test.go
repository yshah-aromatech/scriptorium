package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/config"
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

func TestLegacyDataDirMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".psscripts")
	if err := os.MkdirAll(filepath.Join(legacy, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "history.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, paths, warns, err := config.Load(appDirWith(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "history.jsonl")); err != nil {
		t.Fatalf("migrated history missing: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy dir still present: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.HasPrefix(w, "migrated data dir: ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("migration warning missing: %v", warns)
	}
}

func TestExplicitDataDirNeverMigrates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".psscripts")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "explicit")
	_, _, _, err := config.Load(appDirWith(t, `{"dataDir":"`+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("explicit dataDir must not trigger migration: %v", err)
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
