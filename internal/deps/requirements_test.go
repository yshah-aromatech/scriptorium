package deps

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Ported from tests/Deps.Tests.ps1 "parses requirements.txt names, stripping
// specifiers and comments".
func TestReadRequirements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requirements.txt")
	content := "# comment\n" +
		"requests>=2.31\n" +
		"python-dotenv==1.0.0\n" +
		"pyyaml\n" +
		"-r other.txt\n" +
		`msal[broker]>=1.20 ; python_version >= "3.8"` + "\n" +
		"\n" +
		"   \n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadRequirements(path)
	want := []string{"requests", "python-dotenv", "pyyaml", "msal"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadRequirements = %v, want %v", got, want)
	}
}

func TestReadRequirementsMissingFile(t *testing.T) {
	got := ReadRequirements(filepath.Join(t.TempDir(), "nope.txt"))
	if got != nil {
		t.Errorf("ReadRequirements(missing) = %v, want nil", got)
	}
}

func TestReadRequirementsUnderscoreName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(path, []byte("some_package==2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadRequirements(path)
	want := []string{"some_package"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadRequirements = %v, want %v", got, want)
	}
}

func TestPipName(t *testing.T) {
	cases := map[string]string{
		"cv2":      "opencv-python",
		"PIL":      "pillow",
		"dotenv":   "python-dotenv",
		"requests": "requests", // unmapped -> identity
	}
	for in, want := range cases {
		if got := PipName(in); got != want {
			t.Errorf("PipName(%q) = %q, want %q", in, got, want)
		}
	}
}

// PS hashtable literal lookups are case-insensitive; PipName must match.
func TestPipNameCaseInsensitive(t *testing.T) {
	if got := PipName("CV2"); got != "opencv-python" {
		t.Errorf("PipName(CV2) = %q, want opencv-python (case-insensitive lookup)", got)
	}
	if got := PipName("Dotenv"); got != "python-dotenv" {
		t.Errorf("PipName(Dotenv) = %q, want python-dotenv (case-insensitive lookup)", got)
	}
}

func TestVenvPythonAndHasVenv(t *testing.T) {
	dir := t.TempDir()
	if HasVenv(dir) {
		t.Fatal("HasVenv should be false before the venv exists")
	}
	py := VenvPython(dir)
	if py != filepath.Join(dir, "bin", "python") {
		t.Errorf("VenvPython = %q", py)
	}
	if err := os.MkdirAll(filepath.Dir(py), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasVenv(dir) {
		t.Error("HasVenv should be true once bin/python exists")
	}
}
