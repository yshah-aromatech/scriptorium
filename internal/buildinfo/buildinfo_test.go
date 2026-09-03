package buildinfo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionDefault(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must default to a non-empty string")
	}
	if Version != "dev" {
		t.Fatalf("unlinked builds report %q, want \"dev\"", Version)
	}
}

func TestStringShape(t *testing.T) {
	restore := stub("v1.2.3", "abcdef1", "2026-09-02T00:00:00Z")
	defer restore()
	got := String()
	want := "scriptorium v1.2.3 (commit abcdef1, built 2026-09-02T00:00:00Z)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct{ goos, arch, want string }{
		{"linux", "amd64", "scriptorium_linux_amd64.tar.gz"},
		{"linux", "arm64", "scriptorium_linux_arm64.tar.gz"},
	}
	for _, c := range cases {
		if got := AssetName(c.goos, c.arch); got != c.want {
			t.Errorf("AssetName(%q, %q) = %q, want %q", c.goos, c.arch, got, c.want)
		}
	}
}

func stub(version, commit, date string) func() {
	oldV, oldC, oldD := Version, Commit, Date
	Version, Commit, Date = version, commit, date
	return func() { Version, Commit, Date = oldV, oldC, oldD }
}

// repoRoot walks up from the working directory to find go.mod — the same
// pattern internal/psfixtures.Dir uses, kept local since this is the only
// other package that needs it.
func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("go.mod not found above " + d)
		}
		d = parent
	}
}

// TestGoreleaserArchiveNameMatches cross-checks .goreleaser.yml's archive
// name_template against the AssetName convention: goreleaser's own `.Os`/
// `.Arch` template vars render exactly "linux"/"amd64"/"arm64" (verified
// against a real snapshot build), so the template text itself is pinned
// here rather than duplicating a YAML parser for one line.
func TestGoreleaserArchiveNameMatches(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".goreleaser.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)

	wantTemplate := `name_template: "scriptorium_{{ .Os }}_{{ .Arch }}"`
	if !strings.Contains(content, wantTemplate) {
		t.Errorf(".goreleaser.yml archive name_template drifted from the AssetName convention\nwant it to contain: %s", wantTemplate)
	}
	// AssetName's own literal shape must agree with that template rendered
	// for goreleaser's real .Os/.Arch values (linux/amd64, linux/arm64) —
	// if either side's naming ever changes, this fails alongside the
	// contains-check above rather than silently drifting apart.
	if got, want := AssetName("linux", "amd64"), "scriptorium_linux_amd64.tar.gz"; got != want {
		t.Errorf("AssetName(linux, amd64) = %q, want %q (matching the goreleaser template)", got, want)
	}

	wantChecksum := `name_template: "checksums.txt"`
	if !strings.Contains(content, wantChecksum) {
		t.Errorf(".goreleaser.yml checksum name_template drifted from buildinfo.ChecksumsFile\nwant it to contain: %s", wantChecksum)
	}
	if ChecksumsFile != "checksums.txt" {
		t.Errorf("ChecksumsFile = %q, want %q", ChecksumsFile, "checksums.txt")
	}
}

// TestLdflagsRoundTrip actually builds the binary with the same -X ldflags
// goreleaser stamps and runs `--version` against it, so the round trip from
// .goreleaser.yml's ldflags template to this package's exported var names to
// the CLI's --version output is exercised for real, not just asserted by
// inspection.
func TestLdflagsRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("go build round trip skipped in -short")
	}
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "scriptorium-ldflags-test")
	modPath := "github.com/yshah-aromatech/scriptorium/internal/buildinfo"
	ldflags := fmt.Sprintf("-X %s.Version=v9.9.9 -X %s.Commit=deadbee -X %s.Date=2026-09-02T00:00:00Z", modPath, modPath, modPath)

	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, "./cmd/scriptorium")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version: %v\n%s", err, out)
	}
	want := "scriptorium v9.9.9 (commit deadbee, built 2026-09-02T00:00:00Z)\n"
	if string(out) != want {
		t.Errorf("--version output = %q, want %q", out, want)
	}
}
