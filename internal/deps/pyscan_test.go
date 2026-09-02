package deps

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// SCANNER_PY byte identity: the embedded scanner.py must be exactly the
// heredoc src/Deps.psm1 carries between @' and '@ (the PythonScanner
// verbatim port), sliced fresh from the .psm1 file itself — not a copy of a
// copy. Trailing-newline differences between "as saved on disk" and "as
// sliced from a single-quoted PS heredoc" are normalized away on both
// sides; every other byte must match.
func TestScannerPyByteIdentity(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "src", "Deps.psm1"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?s)@'\n(.*?)\n'@`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatal("no @'...'@ heredoc found in src/Deps.psm1")
	}
	extracted := string(m[1])
	embedded := strings.TrimRight(string(scannerPy), "\n")
	if extracted != embedded {
		t.Errorf("scanner.py drifted from src/Deps.psm1's PythonScanner heredoc:\n--- extracted ---\n%s\n--- embedded ---\n%s", extracted, embedded)
	}
}

func copyPycorpus(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		src, err := os.ReadFile(filepath.Join("testdata", "pycorpus", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func depNames(deps []Dep) map[string]Dep {
	m := map[string]Dep{}
	for _, d := range deps {
		m[d.Name] = d
	}
	return m
}

// hasStdlibModuleNames reports whether pythonBin's sys module exposes
// stdlib_module_names (Python >= 3.10) — scanner.py's stdlib filtering only
// works with it present; below that, every stdlib import leaks through as
// "third party" (installed, since find_spec finds it). Mirrors the known PS
// test-suite skip on macOS's stock Python 3.9.
func hasStdlibModuleNames(t *testing.T, pythonBin string) bool {
	t.Helper()
	out, err := exec.Command(pythonBin, "-c", "import sys; print(hasattr(sys, 'stdlib_module_names'))").Output()
	if err != nil {
		t.Fatalf("probing sys.stdlib_module_names: %v", err)
	}
	ok, _ := strconv.ParseBool(strings.TrimSpace(string(out)))
	return ok
}

func TestScanPythonASTNoVenvFindsThirdPartyImports(t *testing.T) {
	python3 := pwshtest.RequirePython(t)
	dir := copyPycorpus(t, "main.py", "localhelper.py")

	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, filepath.Join(dir, "novenv"), python3)
	if err != nil {
		t.Fatalf("ScanPython: %v", err)
	}
	byName := depNames(got)
	if _, ok := byName["requests"]; !ok {
		t.Errorf("missing should contain requests (no venv -> missing+installed combined), got %+v", got)
	}
	dot, ok := byName["dotenv"]
	if !ok {
		t.Errorf("missing should contain dotenv, got %+v", got)
	} else if dot.PipName != "python-dotenv" || dot.Display != "dotenv (pip: python-dotenv)" {
		t.Errorf("dotenv Dep = %+v, want PipName=python-dotenv and the mapped Display", dot)
	}
	if hasStdlibModuleNames(t, python3) {
		if _, ok := byName["os"]; ok {
			t.Errorf("stdlib import 'os' must never appear, got %+v", got)
		}
	} else {
		t.Log("python < 3.10 has no sys.stdlib_module_names — 'os' leaking through as third-party is the known degraded behavior (matches the PS suite's macOS 3.9 skip), not asserted here")
	}
	if _, ok := byName["localhelper"]; ok {
		t.Errorf("local sibling module must never appear, got %+v", got)
	}
}

// I2: Deps.psm1:454's `Sort-Object` is case-insensitive; sort.Strings is
// ordinal and puts every uppercase name first. Verified live against the
// real Get-StoMissingPythonDeps with this exact import set: PS orders
// "attr, Crypto, numpy, PIL"; an ordinal sort would produce
// "Crypto, PIL, attr, numpy" instead — a byte divergence in both the
// "installing missing modules: ..." line and the generated
// `pip install @(...)` package list.
func TestScanPythonMissingOrderIsCaseInsensitive(t *testing.T) {
	python3 := pwshtest.RequirePython(t)
	dir := copyPycorpus(t, "mixed_case_imports.py")

	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, filepath.Join(dir, "novenv"), python3)
	if err != nil {
		t.Fatalf("ScanPython: %v", err)
	}
	names := make([]string, len(got))
	for i, d := range got {
		names[i] = d.Name
	}
	want := []string{"attr", "Crypto", "numpy", "PIL"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("order = %v, want %v (case-insensitive, PS-verified)", names, want)
			break
		}
	}
}

// Python < 3.10's sys module lacks stdlib_module_names, so scanner.py's
// stdlib set is effectively empty and a stdlib-only import leaks through as
// "third party". This assertion is only meaningful on 3.10+.
func TestScanPythonExcludesStdlibOnModernPython(t *testing.T) {
	python3 := pwshtest.RequirePython(t)
	if !hasStdlibModuleNames(t, python3) {
		t.Skip("python < 3.10 has no sys.stdlib_module_names — stdlib filtering degrades on this interpreter (matches the known PS suite skip on macOS's stock Python 3.9)")
	}
	dir := copyPycorpus(t, "stdlib_import.py")

	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, filepath.Join(dir, "novenv"), python3)
	if err != nil {
		t.Fatalf("ScanPython: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("stdlib-only script should report no deps, got %+v", got)
	}
}

func TestScanPythonRequirementsPrecedenceNoVenv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("requests\nlocalpkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// AST files present too — requirements.txt must win outright, the AST
	// scanner never runs at all.
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("import shouldnotbescanned\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, filepath.Join(dir, "novenv"), "python3")
	if err != nil {
		t.Fatalf("ScanPython: %v", err)
	}
	byName := depNames(got)
	for _, want := range []string{"requests", "localpkg"} {
		d, ok := byName[want]
		if !ok {
			t.Errorf("missing %q, got %+v", want, got)
			continue
		}
		if d.PipName != want || d.Display != want {
			t.Errorf("%q Dep = %+v, want PipName/Display verbatim (no mapping for requirements.txt names)", want, d)
		}
	}
	if _, ok := byName["shouldnotbescanned"]; ok {
		t.Error("requirements.txt present -> the AST scanner must not run at all")
	}
}

func TestScanPythonRequirementsPrecedenceEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("# only comments\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, filepath.Join(dir, "novenv"), "python3")
	if err != nil {
		t.Fatalf("ScanPython: %v", err)
	}
	if got != nil {
		t.Errorf("empty requirements.txt should yield no deps, got %+v", got)
	}
}

// installedPipNames is exercised via a stub venv python answering
// `-m pip list --format=json` with canned JSON — no real pip involved. Also
// covers the underscore<->hyphen, case-insensitive normalization rule.
func TestScanPythonRequirementsWithVenvNormalization(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("requests\nflask\nsome_package\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	venvDir := filepath.Join(dir, "venv")
	writeStubVenvPython(t, venvDir, `[{"name":"requests"},{"name":"Some-Package"}]`)

	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, venvDir, "python3")
	if err != nil {
		t.Fatalf("ScanPython: %v", err)
	}
	byName := depNames(got)
	if _, ok := byName["requests"]; ok {
		t.Errorf("requests is in the stub pip list -> should not be missing, got %+v", got)
	}
	if _, ok := byName["some_package"]; ok {
		t.Errorf("some_package should normalize (underscore<->hyphen, case-insensitive) against installed 'Some-Package' -> not missing, got %+v", got)
	}
	if _, ok := byName["flask"]; !ok {
		t.Errorf("flask is not in the stub pip list -> should be missing, got %+v", got)
	}
	if len(got) != 1 {
		t.Errorf("got %+v, want exactly [flask]", got)
	}
}

// writeStubVenvPython builds a fake "venv" whose bin/python is a shell
// script: `-m pip list --format=json` prints listJSON, anything else exits
// nonzero (never exercised here) — no real pip, no real venv.
func writeStubVenvPython(t *testing.T, venvDir, listJSON string) {
	t.Helper()
	binDir := filepath.Join(venvDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		`if [ "$1" = "-m" ] && [ "$2" = "pip" ] && [ "$3" = "list" ]; then` + "\n" +
		"  echo '" + listJSON + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "python"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestScanPythonAbsentInterpreterYieldsEmptyNoError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("import requests\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := &Scanner{}
	got, err := scanner.ScanPython(dir, filepath.Join(dir, "novenv"), "/nonexistent-python-binary")
	if err != nil {
		t.Fatalf("ScanPython should not error on an absent interpreter, got %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil (absent interpreter -> empty, no error)", got)
	}
}
