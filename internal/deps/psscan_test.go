package deps

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// ---------------------------------------------------------------------------
// THE GATE ORACLE: every pscorpus fixture must produce the identical
// deps/missing/params result from our embedded scanner.ps1 (via ScanPS) and
// from the REAL src/Deps.psm1 + src/Scripts.psm1 functions run live in pwsh.
// ---------------------------------------------------------------------------

type corpusCase struct {
	file  string
	loose []bool // defaults to {false} when nil
	setup func(t *testing.T, dir string)
}

var pscorpus = []corpusCase{
	{file: "requires_exact.ps1"},
	{file: "requires_minmax.ps1"},
	{file: "requires_min_only.ps1"},
	{file: "using_module.ps1"},
	{file: "ipmo.ps1"},
	{file: "name_param.ps1"},
	{file: "colon_param.ps1"},
	{file: "valueparam_trap.ps1"},
	{file: "array_expression.ps1"},
	{file: "bare_list.ps1"},
	{file: "stray_positional.ps1"},
	{file: "builtin_exclusion.ps1"},
	{file: "namemap.ps1"},
	{file: "param_block.ps1"},
	{file: "comment_help.ps1"},
	{file: "parse_warning.ps1"},
	{file: "mixed_no_deps.ps1"},
	{file: "union_versioned_first.ps1"},
	{file: "union_bare_first.ps1"},
	{
		file: "sibling_psm1.ps1",
		setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "Helper.psm1"), []byte("# x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	},
	{
		file:  "folder_exclusion.ps1",
		loose: []bool{false, true},
		setup: func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "SubMod"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	},
}

func TestScanPSMatchesRealDepsAndScriptsPsm1(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)
	repoRoot := findRepoRoot(t)
	oracle := writeOracleDriver(t, repoRoot)

	for _, c := range pscorpus {
		looseValues := c.loose
		if looseValues == nil {
			looseValues = []bool{false}
		}
		for _, loose := range looseValues {
			c, loose := c, loose
			t.Run(fmt.Sprintf("%s/loose=%v", c.file, loose), func(t *testing.T) {
				dir := t.TempDir()
				src, err := os.ReadFile(filepath.Join("testdata", "pscorpus", c.file))
				if err != nil {
					t.Fatal(err)
				}
				entry := filepath.Join(dir, "main.ps1")
				if err := os.WriteFile(entry, src, 0o644); err != nil {
					t.Fatal(err)
				}
				if c.setup != nil {
					c.setup(t, dir)
				}
				moduleDir := filepath.Join(dir, "mods")

				scanner := &Scanner{PwshBin: pwsh}
				got, err := scanner.ScanPS(entry, dir, moduleDir, loose)
				if err != nil {
					t.Fatalf("ScanPS: %v", err)
				}

				want := runOracle(t, pwsh, oracle, entry, dir, moduleDir, loose)

				if !reflect.DeepEqual(got.Deps, want.Deps) {
					t.Errorf("Deps mismatch:\n go=%+v\n ps=%+v", got.Deps, want.Deps)
				}
				if !reflect.DeepEqual(got.Missing, want.Missing) {
					t.Errorf("Missing mismatch:\n go=%+v\n ps=%+v", got.Missing, want.Missing)
				}
				if !reflect.DeepEqual(got.Params, want.Params) {
					t.Errorf("Params mismatch:\n go=%+v\n ps=%+v", got.Params, want.Params)
				}
				if got.Synopsis != want.Synopsis {
					t.Errorf("Synopsis mismatch: go=%q ps=%q", got.Synopsis, want.Synopsis)
				}
				if got.Help != want.Help {
					t.Errorf("Help mismatch: go=%q ps=%q", got.Help, want.Help)
				}
				if got.ParseWarnings != want.ParseWarnings {
					t.Errorf("ParseWarnings mismatch: go=%d ps=%d", got.ParseWarnings, want.ParseWarnings)
				}
			})
		}
	}
}

// writeOracleDriver materializes a small pwsh driver that calls the REAL
// Get-StoScriptDeps / Get-StoMissingDeps / Get-StoScriptParameters from the
// repo's own src/Deps.psm1 + src/Scripts.psm1 (not a copy) and emits the
// exact same JSON shape scanner.ps1 does, so the two can be deep-compared.
func writeOracleDriver(t *testing.T, repoRoot string) string {
	t.Helper()
	script := `param([string]$Entry,[string]$Dir,[string]$ModuleDir,[string]$Loose)
$ErrorActionPreference = 'Stop'
Import-Module '` + filepath.ToSlash(repoRoot) + `/src/Deps.psm1','` + filepath.ToSlash(repoRoot) + `/src/Scripts.psm1' -Force -DisableNameChecking
$Script = [pscustomobject]@{ Dir = $Dir; Entry = $Entry; ModuleDir = $ModuleDir; Loose = ($Loose -eq 'true') }
$deps = @(Get-StoScriptDeps -Script $Script)
$missing = @(Get-StoMissingDeps -Script $Script)
$paramScan = Get-StoScriptParameters -Entry $Entry
$params = @($paramScan.Parameters)
function ConvertTo-DepRecord {
    param($D)
    [ordered]@{ name = $D.Name; requiredVersion = $D.RequiredVersion; minimumVersion = $D.MinimumVersion; maximumVersion = $D.MaximumVersion; display = $D.Display }
}
$result = [ordered]@{
    deps    = @($deps | ForEach-Object { ConvertTo-DepRecord $_ })
    missing = @($missing | ForEach-Object { ConvertTo-DepRecord $_ })
    params  = @($params | ForEach-Object {
            [ordered]@{ name = $_.Name; type = $_.Type; mandatory = [bool]$_.Mandatory; default = $_.Default; validateSet = @($_.ValidateSet); isSwitch = [bool]$_.IsSwitch; description = $_.Description }
        })
    synopsis      = $paramScan.Synopsis
    help          = $paramScan.Help
    parseWarnings = $paramScan.ParseWarnings
}
$result | ConvertTo-Json -Depth 8 -Compress
`
	path := filepath.Join(t.TempDir(), "oracle.ps1")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runOracle(t *testing.T, pwsh, oracle, entry, dir, moduleDir string, loose bool) PSScanResult {
	t.Helper()
	looseArg := "false"
	if loose {
		looseArg = "true"
	}
	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", oracle, entry, dir, moduleDir, looseArg).CombinedOutput()
	if err != nil {
		t.Fatalf("oracle pwsh failed: %v\n%s", err, out)
	}
	line := lastNonEmptyLine(out)
	var doc psScanDoc
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("oracle output not valid JSON: %v\nraw: %s", err, out)
	}
	return doc.toResult()
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

// ---------------------------------------------------------------------------
// Missing-vs-installed (§9.3 satisfaction cases), seeded directly via
// ModuleDir layout — no pwsh needed since it exercises the fully embedded
// pipeline end to end via ScanPS itself (still pwsh-gated: the scan runs
// through the real embedded scanner.ps1).
// ---------------------------------------------------------------------------

func TestScanPSSatisfaction(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ps1")
	src := "#Requires -Modules @{ ModuleName = 'ModA'; RequiredVersion = '1.5.0' }\n" +
		"#Requires -Modules @{ ModuleName = 'ModB'; ModuleVersion = '2.0.0' }\n" +
		"#Requires -Modules @{ ModuleName = 'ModC'; RequiredVersion = '9.9.9' }\n" +
		"Write-Host hi\n"
	if err := os.WriteFile(entry, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleDir := filepath.Join(dir, "mods")
	// ModA: valid version dir satisfying RequiredVersion 1.5.0
	mustMkdir(t, filepath.Join(moduleDir, "ModA", "1.5.0"))
	// ModB: a garbage (non-version) dir -> treated as version 0.0, which does
	// NOT satisfy MinimumVersion 2.0.0
	mustMkdir(t, filepath.Join(moduleDir, "ModB", "not-a-version"))
	// ModC: not installed at all -> unsatisfied regardless of version

	scanner := &Scanner{PwshBin: pwsh}
	got, err := scanner.ScanPS(entry, dir, moduleDir, false)
	if err != nil {
		t.Fatalf("ScanPS: %v", err)
	}
	if len(got.Deps) != 3 {
		t.Fatalf("Deps = %+v, want 3 entries", got.Deps)
	}
	missingNames := map[string]bool{}
	for _, d := range got.Missing {
		missingNames[d.Name] = true
	}
	if missingNames["ModA"] {
		t.Errorf("ModA should be satisfied (exact version dir present), Missing=%+v", got.Missing)
	}
	if !missingNames["ModB"] {
		t.Errorf("ModB should be unsatisfied (garbage version dir -> 0.0 < min 2.0.0), Missing=%+v", got.Missing)
	}
	if !missingNames["ModC"] {
		t.Errorf("ModC should be unsatisfied (not installed), Missing=%+v", got.Missing)
	}
}

// fails-open: an unparseable RequiredVersion is treated as satisfied the
// moment the module is present at all, regardless of installed version.
func TestScanPSSatisfactionFailsOpenOnUnparseableVersion(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ps1")
	src := "#Requires -Modules @{ ModuleName = 'ModX'; RequiredVersion = 'not-a-version' }\nWrite-Host hi\n"
	if err := os.WriteFile(entry, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleDir := filepath.Join(dir, "mods")
	mustMkdir(t, filepath.Join(moduleDir, "ModX", "1.0.0"))

	scanner := &Scanner{PwshBin: pwsh}
	got, err := scanner.ScanPS(entry, dir, moduleDir, false)
	if err != nil {
		t.Fatalf("ScanPS: %v", err)
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %+v, want empty (unparseable RequiredVersion fails open)", got.Missing)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Cache: same (size, mtime) serves the cached result without re-invoking
// pwsh; a touched mtime rescans.
// ---------------------------------------------------------------------------

func TestScanPSCache(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ps1")
	if err := os.WriteFile(entry, []byte("Import-Module ModA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	countFile := filepath.Join(dir, "count")
	stub := writeCountingStub(t, dir, countFile)

	scanner := &Scanner{PwshBin: stub}
	if _, err := scanner.ScanPS(entry, dir, filepath.Join(dir, "mods"), false); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if _, err := scanner.ScanPS(entry, dir, filepath.Join(dir, "mods"), false); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if n := readCount(t, countFile); n != 1 {
		t.Fatalf("invocation count = %d, want 1 (cache hit)", n)
	}

	// touch mtime forward -> rescan
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(entry, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ScanPS(entry, dir, filepath.Join(dir, "mods"), false); err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if n := readCount(t, countFile); n != 2 {
		t.Fatalf("invocation count = %d, want 2 (mtime changed -> rescan)", n)
	}
}

// writeCountingStub writes a stub "pwsh" shell script that appends a line to
// countFile every invocation and prints a valid empty-result JSON doc,
// regardless of the arguments it's given.
func writeCountingStub(t *testing.T, dir, countFile string) string {
	t.Helper()
	stub := filepath.Join(dir, "stub-pwsh.sh")
	content := "#!/bin/sh\necho x >> '" + countFile + "'\necho '{\"deps\":[],\"missing\":[],\"params\":[]}'\n"
	if err := os.WriteFile(stub, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

func readCount(t *testing.T, countFile string) int {
	t.Helper()
	data, err := os.ReadFile(countFile)
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimRight(string(data), "\n"), "\n"))
}

// ---------------------------------------------------------------------------
// Fallback: an unrunnable PwshBin degrades to the regex scan.
// ---------------------------------------------------------------------------

func TestScanPSFallbackWhenPwshMissing(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ps1")
	src := "#Requires -Modules Foo\nusing module Bar\nImport-Module Baz -ErrorAction Stop\nImport-Module Microsoft.PowerShell.Utility\nImport-Module pester\n"
	if err := os.WriteFile(entry, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	scanner := &Scanner{PwshBin: "/nonexistent-pwsh-binary"}
	got, err := scanner.ScanPS(entry, dir, filepath.Join(dir, "mods"), false)
	if err != nil {
		t.Fatalf("ScanPS: %v", err)
	}
	if !got.Degraded {
		t.Errorf("Degraded = false, want true")
	}
	const wantWarning = "pwsh not found — dependency scan degraded (regex), install checks skipped"
	if got.Warning != wantWarning {
		t.Errorf("Warning = %q, want %q", got.Warning, wantWarning)
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %+v, want empty in degraded mode", got.Missing)
	}
	if got.Params != nil {
		t.Errorf("Params = %+v, want nil in degraded mode", got.Params)
	}

	names := map[string]bool{}
	for _, d := range got.Deps {
		names[d.Name] = true
	}
	for _, want := range []string{"Foo", "Bar", "Baz"} {
		if !names[want] {
			t.Errorf("degraded Deps = %+v, missing %q", got.Deps, want)
		}
	}
	if names["Microsoft.PowerShell.Utility"] {
		t.Errorf("degraded Deps should exclude builtin, got %+v", got.Deps)
	}
	if !names["Pester"] {
		t.Errorf("degraded Deps should apply the name map (pester->Pester), got %+v", got.Deps)
	}
}

// ---------------------------------------------------------------------------
// I1: Get-StoMissingDeps' zero-dep short-circuit (Deps.psm1:210) — a dep-free
// script must never call Get-StoInstalledModules (which, unfiltered, walks
// the whole PSModulePath and can cost multiple seconds on a real machine).
// A JSON-output assertion can't distinguish "skipped entirely" from "ran
// with an empty -Names filter" — both produce empty Missing — so this pins
// the guard structurally in scanner.ps1's source, on top of the oracle
// (mixed_no_deps.ps1) continuing to exercise the now-short-circuited path.
// ---------------------------------------------------------------------------

func TestScannerPSShortCircuitsOnZeroDeps(t *testing.T) {
	if !strings.Contains(string(scannerPS1), "$allDeps.Count -eq 0") {
		t.Fatal("scanner.ps1 must skip Get-StoInstalledModules entirely when Get-StoScriptDeps found nothing (Deps.psm1:210 parity)")
	}
}

// ---------------------------------------------------------------------------
// I3: the embedded scanner is materialized to a content-addressed temp path
// once per machine, not once per Scanner/process — no leak, and two
// different Scanner instances resolve to the identical file.
// ---------------------------------------------------------------------------

func TestScanPSMaterializedScriptIsContentAddressedNotLeaked(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ps1")
	if err := os.WriteFile(entry, []byte("Write-Host hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := filepath.Glob(filepath.Join(os.TempDir(), "scriptorium-deps-scanner-*.ps1"))
	if err != nil {
		t.Fatal(err)
	}

	s1 := &Scanner{PwshBin: pwsh}
	s2 := &Scanner{PwshBin: pwsh}
	if _, err := s1.ScanPS(entry, dir, filepath.Join(dir, "mods"), false); err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if _, err := s2.ScanPS(entry, dir, filepath.Join(dir, "mods"), false); err != nil {
		t.Fatalf("scan 2: %v", err)
	}

	p1, err := s1.materializedScript()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s2.materializedScript()
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("two Scanner instances embedding identical bytes resolved to different paths: %q vs %q", p1, p2)
	}

	after, err := filepath.Glob(filepath.Join(os.TempDir(), "scriptorium-deps-scanner-*.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	// Content-addressed: running two more scans (even from fresh Scanners)
	// must not grow the count of distinct materialized-scanner files beyond
	// what a single one already established.
	if len(after) > len(before)+1 {
		t.Errorf("materializedScript leaked files: before=%v after=%v", before, after)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Errorf("materialized script should still exist on disk (content-addressed, not a leak): %v", err)
	}
}

// ---------------------------------------------------------------------------
// I4: the (size, mtime) cache only observes the entry file, not moduleDir —
// so an install that satisfies a dep must invalidate the cached entry, or a
// long-lived Scanner keeps serving the pre-install Missing list forever.
// ---------------------------------------------------------------------------

func TestScanPSCacheInvalidateAfterInstallReflectsNewlyInstalledModule(t *testing.T) {
	pwsh := pwshtest.RequirePwsh(t)
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ps1")
	src := "#Requires -Modules @{ ModuleName = 'ModA'; RequiredVersion = '1.5.0' }\nWrite-Host hi\n"
	if err := os.WriteFile(entry, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleDir := filepath.Join(dir, "mods")

	scanner := &Scanner{PwshBin: pwsh}
	first, err := scanner.ScanPS(entry, dir, moduleDir, false)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first.Missing) != 1 {
		t.Fatalf("first scan Missing = %+v, want [ModA] before install", first.Missing)
	}

	// Simulate the install succeeding (the actual side effect an install
	// command has: a new <ModuleDir>/<Name>/<Version>/ directory).
	mustMkdir(t, filepath.Join(moduleDir, "ModA", "1.5.0"))

	// Without invalidation, the (size, mtime) cache — keyed only on entry,
	// which never changed — must still serve the stale pre-install Missing.
	stale, err := scanner.ScanPS(entry, dir, moduleDir, false)
	if err != nil {
		t.Fatalf("stale rescan: %v", err)
	}
	if len(stale.Missing) != 1 {
		t.Fatalf("stale rescan Missing = %+v, want the cache to still report [ModA] (proves the cache is real)", stale.Missing)
	}

	scanner.Invalidate(entry)
	fresh, err := scanner.ScanPS(entry, dir, moduleDir, false)
	if err != nil {
		t.Fatalf("fresh rescan: %v", err)
	}
	if len(fresh.Missing) != 0 {
		t.Errorf("fresh rescan Missing = %+v, want empty (ModA is now installed, cache was invalidated)", fresh.Missing)
	}
}
