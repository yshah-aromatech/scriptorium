package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// discoverRoot builds a single-repo (legacy layout) app dir and returns the
// scripts root to populate plus a discover func over it.
func discoverRoot(t *testing.T) (root string, discover func() []scripts.Script) {
	t.Helper()
	cfg, paths := loadWithDataDir(t, "")
	root = paths.ScriptsDir
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	discover = func() []scripts.Script {
		return scripts.Discover(scripts.Repos(cfg, paths), paths)
	}
	return root, discover
}

// Ported from tests/Scripts.Tests.ps1 'Get-StoScripts discovery'.

func TestDiscoverUsesMainByConvention(t *testing.T) {
	root, discover := discoverRoot(t)
	writeFile(t, filepath.Join(root, "a", "main.ps1"), "x")
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "main.ps1") {
		t.Fatalf("got %+v", s)
	}
}

func TestDiscoverPrefersScriptJSONEntry(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "b")
	writeFile(t, filepath.Join(d, "main.ps1"), "x")
	writeFile(t, filepath.Join(d, "custom.ps1"), "x")
	writeFile(t, filepath.Join(d, "script.json"), `{"entry": "custom.ps1", "description": "desc", "timeoutMinutes": 15}`)
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "custom.ps1") {
		t.Fatalf("got %+v", s)
	}
	if s[0].Description != "desc" {
		t.Errorf("Description = %q", s[0].Description)
	}
	if s[0].TimeoutMinutes == nil || *s[0].TimeoutMinutes != 15 {
		t.Errorf("TimeoutMinutes = %v", s[0].TimeoutMinutes)
	}
}

func TestDiscoverFallsBackToSolePS1(t *testing.T) {
	root, discover := discoverRoot(t)
	writeFile(t, filepath.Join(root, "c", "whatever.ps1"), "x")
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "whatever.ps1") {
		t.Fatalf("got %+v", s)
	}
}

// M4: script.json "args" coercion mirrors PS's
// `@($Meta.args | ForEach-Object { "$_" })` — a bare non-array value wraps
// to one arg, and an array element-wise stringifies (numbers included).
//
// r2: PS's actual gate is `$Meta.args -and ...` — a PS-falsy value (null,
// false, "", numeric zero, an empty array) must yield NO args, not a
// spurious one-element wrap. A non-empty string (even "0") and a truthy
// scalar like `true` still wrap to one arg (verified against live pwsh:
// `@($true | ForEach-Object { "$_" })` -> "True").
func TestDiscoverArgsCoercion(t *testing.T) {
	cases := []struct {
		name, argsJSON string
		want           []string
	}{
		{"bare string wraps to one arg", `"one two"`, []string{"one two"}},
		{"numbers stringify", `[1,2]`, []string{"1", "2"}},
		{"strings pass through", `["a","b"]`, []string{"a", "b"}},
		{"empty string is falsy", `""`, []string{}},
		{"numeric zero is falsy", `0`, []string{}},
		{"false is falsy", `false`, []string{}},
		{"null is falsy", `null`, []string{}},
		{"empty array is falsy", `[]`, []string{}},
		{"true is truthy and stringifies capitalized", `true`, []string{"True"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, discover := discoverRoot(t)
			d := filepath.Join(root, "argscase")
			writeFile(t, filepath.Join(d, "main.ps1"), "x")
			writeFile(t, filepath.Join(d, "script.json"), `{"args":`+c.argsJSON+`}`)
			s := discover()
			if len(s) != 1 {
				t.Fatalf("got %+v", s)
			}
			if len(s[0].Args) != len(c.want) {
				t.Fatalf("Args = %v, want %v", s[0].Args, c.want)
			}
			for i := range c.want {
				if s[0].Args[i] != c.want[i] {
					t.Errorf("Args[%d] = %q, want %q", i, s[0].Args[i], c.want[i])
				}
			}
		})
	}
}

// M5: script.json keys are matched case-insensitively, mirroring PS's
// PSObject property access.
func TestDiscoverScriptJSONKeysCaseInsensitive(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "casekey")
	writeFile(t, filepath.Join(d, "x.ps1"), "x")
	writeFile(t, filepath.Join(d, "script.json"), `{"Entry": "x.ps1"}`)
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "x.ps1") {
		t.Fatalf("got %+v, want the capitalized 'Entry' key honored", s)
	}
}

func TestDiscoverIgnoresNonNumericTimeout(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "d")
	writeFile(t, filepath.Join(d, "main.ps1"), "x")
	writeFile(t, filepath.Join(d, "script.json"), `{"timeoutMinutes": "soon"}`)
	s := discover()
	if len(s) != 1 || s[0].TimeoutMinutes != nil {
		t.Fatalf("got %+v", s)
	}
}

func TestDiscoverLooseFiles(t *testing.T) {
	root, discover := discoverRoot(t)
	writeFile(t, filepath.Join(root, "loose.ps1"), "x")
	s := discover()
	if len(s) != 1 || s[0].Name != "loose" || !s[0].Loose {
		t.Fatalf("got %+v", s)
	}
	if !strings.HasSuffix(s[0].EnvFile, "loose.env") {
		t.Errorf("EnvFile = %q", s[0].EnvFile)
	}
}

// M8: a malformed script.json (invalid JSON) doesn't sink the folder —
// loadScriptMeta returns nil, and resolveEntry falls through to the
// conventional entry names.
func TestDiscoverMalformedScriptJSONFallsBackToConventionalEntry(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "brokenmeta")
	writeFile(t, filepath.Join(d, "main.ps1"), "x")
	writeFile(t, filepath.Join(d, "script.json"), `{not valid json`)
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "main.ps1") {
		t.Fatalf("got %+v, want the folder still discovered via main.ps1", s)
	}
}

// M8: script.json's timeoutMinutes accepts a quoted numeric string too,
// via numericCast's `-as [double]`-equivalent string branch.
func TestDiscoverTimeoutMinutesAcceptsQuotedNumericString(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "qtimeout")
	writeFile(t, filepath.Join(d, "main.ps1"), "x")
	writeFile(t, filepath.Join(d, "script.json"), `{"timeoutMinutes": "15"}`)
	s := discover()
	if len(s) != 1 || s[0].TimeoutMinutes == nil || *s[0].TimeoutMinutes != 15 {
		t.Fatalf("got %+v, want TimeoutMinutes=15", s)
	}
}

// M8: a residual same-base collision AFTER duplicate qualification (a
// folder and a loose file sharing one base name, in the same repo) gets a
// "-2" suffix on the second occurrence — folders are candidates before
// loose files, so the folder claims the qualified name first.
func TestDiscoverResidualCollisionGetsDashTwoSuffix(t *testing.T) {
	root, discover := discoverRoot(t)
	writeFile(t, filepath.Join(root, "x", "main.ps1"), "folder")
	writeFile(t, filepath.Join(root, "x.ps1"), "loose")
	s := discover()
	if len(s) != 2 {
		t.Fatalf("got %+v, want 2 scripts", s)
	}
	byName := map[string]scripts.Script{}
	for _, sc := range s {
		byName[sc.Name] = sc
	}
	folder, ok := byName["scripts-x"]
	if !ok || folder.Loose {
		t.Fatalf("got %+v, want a non-loose 'scripts-x' (the folder)", s)
	}
	loose, ok := byName["scripts-x-2"]
	if !ok || !loose.Loose {
		t.Fatalf("got %+v, want a loose 'scripts-x-2' (the file)", s)
	}
}

// M8: a repo root that doesn't exist, or that exists as a plain file rather
// than a directory, is skipped rather than panicking or erroring.
func TestDiscoverRepoRootMissingOrIsFile(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		cfg, paths := loadWithDataDir(t, "") // paths.ScriptsDir intentionally never created
		if s := scripts.Discover(scripts.Repos(cfg, paths), paths); len(s) != 0 {
			t.Fatalf("got %+v, want none", s)
		}
	})
	t.Run("is a file", func(t *testing.T) {
		cfg, paths := loadWithDataDir(t, "")
		writeFile(t, paths.ScriptsDir, "not a directory") // DataDir (its parent) already exists
		if s := scripts.Discover(scripts.Repos(cfg, paths), paths); len(s) != 0 {
			t.Fatalf("got %+v, want none", s)
		}
	})
}

func TestDiscoverSkipsEmptyFolder(t *testing.T) {
	root, discover := discoverRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s := discover(); len(s) != 0 {
		t.Fatalf("got %+v, want none", s)
	}
}

// python discovery

func TestDiscoverPythonMainConvention(t *testing.T) {
	root, discover := discoverRoot(t)
	writeFile(t, filepath.Join(root, "pya", "main.py"), "print(1)")
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "main.py") || s[0].Runtime != "python" {
		t.Fatalf("got %+v", s)
	}
	if !strings.HasSuffix(s[0].VenvDir, filepath.Join("venvs", "pya")) {
		t.Errorf("VenvDir = %q", s[0].VenvDir)
	}
}

func TestDiscoverDunderMainAndSoleFallback(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "pyb")
	writeFile(t, filepath.Join(d, "__main__.py"), "print(1)")
	s := discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "__main__.py") {
		t.Fatalf("got %+v", s)
	}
	if err := os.Remove(filepath.Join(d, "__main__.py")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d, "oddname.py"), "print(1)")
	s = discover()
	if len(s) != 1 || !strings.HasSuffix(s[0].Entry, "oddname.py") {
		t.Fatalf("got %+v", s)
	}
}

func TestDiscoverPrefersPS1OverPythonMixed(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "mixed")
	writeFile(t, filepath.Join(d, "main.ps1"), "x")
	writeFile(t, filepath.Join(d, "main.py"), "print(1)")
	s := discover()
	if len(s) != 1 || s[0].Runtime != "powershell" {
		t.Fatalf("got %+v", s)
	}
}

func TestDiscoverScriptJSONEntryPointsAtPy(t *testing.T) {
	root, discover := discoverRoot(t)
	d := filepath.Join(root, "pyc")
	writeFile(t, filepath.Join(d, "main.ps1"), "x")
	writeFile(t, filepath.Join(d, "actual.py"), "print(1)")
	writeFile(t, filepath.Join(d, "script.json"), `{"entry": "actual.py"}`)
	s := discover()
	if len(s) != 1 || s[0].Runtime != "python" || !strings.HasSuffix(s[0].Entry, "actual.py") {
		t.Fatalf("got %+v", s)
	}
}

func TestDiscoverSkipsSkipDirsAndFindsLoosePy(t *testing.T) {
	root, discover := discoverRoot(t)
	for _, skip := range []string{"__pycache__", ".venv", "node_modules"} {
		writeFile(t, filepath.Join(root, skip, "main.py"), "print(1)")
	}
	writeFile(t, filepath.Join(root, "loosepy.py"), "print(1)")
	s := discover()
	if len(s) != 1 || s[0].Name != "loosepy" || s[0].Runtime != "python" {
		t.Fatalf("got %+v", s)
	}
	if !strings.HasSuffix(s[0].EnvFile, "loosepy.env") {
		t.Errorf("EnvFile = %q", s[0].EnvFile)
	}
}

// multi-repo qualification, ported from tests/Scripts.Tests.ps1 'multi-repo
// config' / 'discovers across repos, tags Repo, and qualifies duplicate
// names' (post-P0 naming: psrepo-foo / bar / pyrepo-foo).
func TestDiscoverMultiRepoQualification(t *testing.T) {
	cfg, paths := loadWithDataDir(t, `,"repos":[{"name":"psrepo","url":"https://github.com/org/ps-scripts"},{"name":"pyrepo","url":"https://github.com/org/py-scripts","branch":"dev"}]`)
	root := paths.ScriptsDir
	writeFile(t, filepath.Join(root, "psrepo", "foo", "main.ps1"), "x")
	writeFile(t, filepath.Join(root, "pyrepo", "foo", "main.py"), "print(1)")
	writeFile(t, filepath.Join(root, "pyrepo", "bar", "main.py"), "print(1)")

	s := scripts.Discover(scripts.Repos(cfg, paths), paths)
	var names []string
	for _, sc := range s {
		names = append(names, sc.Name)
	}
	want := []string{"psrepo-foo", "bar", "pyrepo-foo"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
	for _, sc := range s {
		if sc.Name == "psrepo-foo" && sc.Repo != "psrepo" {
			t.Errorf("psrepo-foo.Repo = %q", sc.Repo)
		}
		if sc.Name == "pyrepo-foo" && sc.Runtime != "python" {
			t.Errorf("pyrepo-foo.Runtime = %q", sc.Runtime)
		}
	}
}

// entry-resolution precedence table.
func TestEntryResolutionPrecedence(t *testing.T) {
	root, discover := discoverRoot(t)

	// script.json beats main.ps1
	d1 := filepath.Join(root, "p1")
	writeFile(t, filepath.Join(d1, "main.ps1"), "x")
	writeFile(t, filepath.Join(d1, "custom.ps1"), "x")
	writeFile(t, filepath.Join(d1, "script.json"), `{"entry":"custom.ps1"}`)

	// main.ps1 beats <folder>.ps1
	d2 := filepath.Join(root, "p2")
	writeFile(t, filepath.Join(d2, "main.ps1"), "x")
	writeFile(t, filepath.Join(d2, "p2.ps1"), "x")

	// ps beats py in a mixed folder
	d3 := filepath.Join(root, "p3")
	writeFile(t, filepath.Join(d3, "run.ps1"), "x")
	writeFile(t, filepath.Join(d3, "run.py"), "print(1)")

	// __main__.py reached only when the three py names miss
	d4 := filepath.Join(root, "p4")
	writeFile(t, filepath.Join(d4, "__main__.py"), "print(1)")
	writeFile(t, filepath.Join(d4, "zzz.py"), "print(1)") // not a convention name; __main__.py must still win

	s := discover()
	byName := map[string]scripts.Script{}
	for _, sc := range s {
		byName[sc.Name] = sc
	}
	if !strings.HasSuffix(byName["p1"].Entry, "custom.ps1") {
		t.Errorf("p1 entry = %q, want script.json to win", byName["p1"].Entry)
	}
	if !strings.HasSuffix(byName["p2"].Entry, "main.ps1") {
		t.Errorf("p2 entry = %q, want main.ps1 to win over <folder>.ps1", byName["p2"].Entry)
	}
	if !strings.HasSuffix(byName["p3"].Entry, "run.ps1") {
		t.Errorf("p3 entry = %q, want ps1 to win over py", byName["p3"].Entry)
	}
	if !strings.HasSuffix(byName["p4"].Entry, "__main__.py") {
		t.Errorf("p4 entry = %q, want __main__.py", byName["p4"].Entry)
	}
}

// containment: an entry escaping the folder is ignored, falling through to
// conventional resolution.
func TestEntryContainmentGuard(t *testing.T) {
	root, discover := discoverRoot(t)
	// "../../evil.ps1" from a folder directly under root resolves to
	// root's PARENT dir — the traversal target actually exists there, so
	// the guard (not a missing file) is what must reject it.
	writeFile(t, filepath.Join(filepath.Dir(root), "evil.ps1"), "x")
	d := filepath.Join(root, "escaping")
	writeFile(t, filepath.Join(d, "main.ps1"), "conventional")
	writeFile(t, filepath.Join(d, "script.json"), `{"entry": "../../evil.ps1"}`)

	s := discover()
	var found *scripts.Script
	for i := range s {
		if s[i].Name == "escaping" {
			found = &s[i]
		}
	}
	if found == nil {
		t.Fatal("escaping script not discovered")
	}
	if !strings.HasSuffix(found.Entry, filepath.Join("escaping", "main.ps1")) {
		t.Fatalf("entry = %q, want the traversal ignored in favor of main.ps1", found.Entry)
	}
}

// skip-dirs (folder-level, single-repo layout — module/repo hygiene dirs
// never become scripts even when a ps1/py sits inside).
func TestDiscoverSkipDirsAtFolderLevel(t *testing.T) {
	root, discover := discoverRoot(t)
	writeFile(t, filepath.Join(root, ".git", "hooks", "main.ps1"), "x")
	writeFile(t, filepath.Join(root, ".github", "main.ps1"), "x")
	writeFile(t, filepath.Join(root, "real", "main.ps1"), "x")
	s := discover()
	if len(s) != 1 || s[0].Name != "real" {
		t.Fatalf("got %+v, want only 'real'", s)
	}
}

// ---------------------------------------------------------------------
// pwsh cross-check: Go's Discover must equal PS's Get-StoScripts over the
// identical tree, in order.
// ---------------------------------------------------------------------

func TestDiscoverMatchesPowerShellCrossCheck(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)
	repoRoot := findRepoRoot(t)

	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	cfgJSON := `{"dataDir":"` + dataDir + `","repos":[` +
		`{"name":"repoa","url":"https://example.invalid/a"},` +
		`{"name":"repob","url":"https://example.invalid/b"}]}`
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, paths, _, err := config.Load(appDir)
	if err != nil {
		t.Fatal(err)
	}
	root := paths.ScriptsDir

	// repo A: foo(ps), zeta(script.json entry), both(ps+py), a
	// script.json-only folder with no entry.
	writeFile(t, filepath.Join(root, "repoa", "foo", "main.ps1"), "x")
	writeFile(t, filepath.Join(root, "repoa", "zeta", "custom.ps1"), "x")
	writeFile(t, filepath.Join(root, "repoa", "zeta", "script.json"), `{"entry":"custom.ps1"}`)
	writeFile(t, filepath.Join(root, "repoa", "both", "main.ps1"), "x")
	writeFile(t, filepath.Join(root, "repoa", "both", "main.py"), "print(1)")
	writeFile(t, filepath.Join(root, "repoa", "onlymeta", "script.json"), `{"description":"no entry here"}`)

	// repo B: foo(py) (collides with repoA's foo), bar(py), loose tool.ps1.
	writeFile(t, filepath.Join(root, "repob", "foo", "main.py"), "print(1)")
	writeFile(t, filepath.Join(root, "repob", "bar", "main.py"), "print(1)")
	writeFile(t, filepath.Join(root, "repob", "tool.ps1"), "x")

	goScripts := scripts.Discover(scripts.Repos(cfg, paths), paths)
	type triple struct{ name, runtime, entryBase string }
	var got []triple
	for _, s := range goScripts {
		got = append(got, triple{s.Name, s.Runtime, filepath.Base(s.Entry)})
	}

	script := `Import-Module '` + repoRoot + `/src/Core.psm1','` + repoRoot + `/src/Scripts.psm1' -Force -DisableNameChecking
Initialize-Sto -AppDir '` + appDir + `'
foreach ($s in @(Get-StoScripts)) {
    Write-Output "$($s.Name)|$($s.Runtime)|$([IO.Path]::GetFileName($s.Entry))"
}`
	out, err := exec.Command(pwsh, "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh failed: %v\n%s", err, out)
	}
	var want []triple
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			t.Fatalf("unexpected PS output line: %q\nfull output:\n%s", line, out)
		}
		want = append(want, triple{parts[0], parts[1], parts[2]})
	}

	if len(got) != len(want) {
		t.Fatalf("Go found %d scripts, PS found %d\nGo:   %v\nPS:   %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: Go=%+v PS=%+v\nfull Go: %v\nfull PS: %v", i, got[i], want[i], got, want)
		}
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("repo root not found")
		}
		d = p
	}
}
