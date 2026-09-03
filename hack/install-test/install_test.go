// Package installtest hermetically exercises install.sh end to end: a stub
// curl serves a real tarball this file builds (with a real sha256
// checksums.txt alongside it), stub apt-get/dpkg/sudo fail loudly so any
// accidental system mutation surfaces as a test failure, snap is stubbed to
// merely record its invocation, and stub systemctl reports "inactive" by
// default (overridden per test) — HOME/PATH are fully sandboxed per test.
// Checkout-mode tests use real git against fully local, disposable bare
// repos (never the real scriptorium remote). No test in this package
// touches the network or a real apt/snap/systemctl.
package installtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/buildinfo"
)

// ---------------------------------------------------------------------
// repo-root + install.sh source
// ---------------------------------------------------------------------

// repoRoot walks up from the working directory to find go.mod — the same
// pattern internal/psfixtures.Dir and internal/buildinfo's test use.
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

func installShSource(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestInstallShAssetNamingMatchesConvention pins install.sh's hardcoded
// asset-filename pattern against buildinfo.AssetName's own convention — the
// controller resolution's "shared test constant" cross-check. Every other
// test in this file proves the same thing end-to-end (a drifted pattern
// makes the stub curl unable to find its fixture, failing loudly), but this
// one names the exact literal so a diff to either side is obvious.
func TestInstallShAssetNamingMatchesConvention(t *testing.T) {
	content := string(installShSource(t))
	want := `ASSET="scriptorium_linux_${ARCH}.tar.gz"`
	if !strings.Contains(content, want) {
		t.Errorf("install.sh drifted from the buildinfo.AssetName convention; want it to contain: %s", want)
	}
	if got := buildinfo.AssetName("linux", "amd64"); got != "scriptorium_linux_amd64.tar.gz" {
		t.Fatalf("buildinfo.AssetName drifted: %s", got)
	}
	if !strings.Contains(content, `checksums.txt`) {
		t.Errorf("install.sh no longer mentions checksums.txt (buildinfo.ChecksumsFile = %q)", buildinfo.ChecksumsFile)
	}
}

// ---------------------------------------------------------------------
// fixture release tarball + checksums
// ---------------------------------------------------------------------

const fixtureConfigExample = `{"scriptsRepo":"https://example.invalid/repo.git","dataDir":"~/.scriptorium"}` + "\n"
const fixtureEnvExample = "GITHUB_TOKEN=\n"

type fileEntry struct {
	mode int64
	body []byte
}

func writeTarGz(t *testing.T, path string, files map[string]fileEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, fe := range files {
		hdr := &tar.Header{Name: name, Mode: fe.mode, Size: int64(len(fe.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(fe.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// buildRelease writes <releaseDir>/scriptorium_linux_<arch>.tar.gz (a
// stand-in binary with the given content, plus the example files a real
// goreleaser archive ships) and a matching checksums.txt, entirely with
// buildinfo's own naming convention.
func buildRelease(t *testing.T, releaseDir, arch, binaryContent string) (assetName string) {
	t.Helper()
	assetName = buildinfo.AssetName("linux", arch)
	tarPath := filepath.Join(releaseDir, assetName)
	writeTarGz(t, tarPath, map[string]fileEntry{
		"scriptorium":         {0o755, []byte(binaryContent)},
		"config.json.example": {0o644, []byte(fixtureConfigExample)},
		".env.example":        {0o644, []byte(fixtureEnvExample)},
		"README.md":           {0o644, []byte("# scriptorium\n")},
	})
	sum := sha256File(t, tarPath)
	line := fmt.Sprintf("%s  %s\n", sum, assetName)
	checksumsPath := filepath.Join(releaseDir, buildinfo.ChecksumsFile)
	existing, _ := os.ReadFile(checksumsPath)
	if err := os.WriteFile(checksumsPath, append(existing, []byte(line)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return assetName
}

// ---------------------------------------------------------------------
// PATH stubs — apt-get/dpkg/sudo are FORBIDDEN (fail loudly, never no-op,
// so an accidental invocation is a test failure); curl/uname/python3 are
// faked just enough for install.sh's own exact call shapes.
// ---------------------------------------------------------------------

const forbiddenStub = "#!/bin/sh\necho \"FORBIDDEN: $0 must not run in hermetic install tests (args: $*)\" >&2\nexit 1\n"

// curl is only ever called as: curl -fsSL -o <outfile> <url> (install.sh's
// two download lines) — the stub serves fixtures from $FAKE_RELEASE_DIR by
// the URL's basename instead of hitting the network.
const curlStub = `#!/bin/sh
out="$3"
url="$4"
name=$(basename "$url")
if [ -f "$FAKE_RELEASE_DIR/$name" ]; then
  cp "$FAKE_RELEASE_DIR/$name" "$out"
  exit 0
fi
echo "fake curl: no fixture for $name in $FAKE_RELEASE_DIR" >&2
exit 22
`

const unameStub = `#!/bin/sh
if [ "$1" = "-m" ]; then
  echo "${FAKE_UNAME_M:-x86_64}"
  exit 0
fi
exit 1
`

const python3Stub = `#!/bin/sh
if [ "$1" = "-m" ] && [ "$2" = "venv" ] && [ "$3" = "--help" ]; then
  echo "usage: venv (fake)"
  exit 0
fi
if [ "$1" = "--version" ]; then
  echo "Python 3.11.0 (fake)"
  exit 0
fi
exit 1
`

// systemctlInactiveStub is the hermetic default: no scriptorium-mcp unit
// exists in test fixtures, so both system- and user-scope `is-active`
// checks report inactive (real systemctl's own exit code for that) and
// install.sh's restart hint never fires unless a test overrides this stub.
const systemctlInactiveStub = "#!/bin/sh\nexit 3\n"

// systemctlActiveStub answers `is-active --quiet` as active regardless of
// scope, for tests that assert the restart hint fires.
const systemctlActiveStub = "#!/bin/sh\nexit 0\n"

func writeStub(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newStubBin builds the standard forbidden+faked command directory shared
// by every test.
func newStubBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeStub(t, dir, "apt-get", forbiddenStub)
	writeStub(t, dir, "dpkg", forbiddenStub)
	writeStub(t, dir, "sudo", forbiddenStub)
	writeStub(t, dir, "curl", curlStub)
	writeStub(t, dir, "uname", unameStub)
	writeStub(t, dir, "python3", python3Stub)
	writeStub(t, dir, "systemctl", systemctlInactiveStub)
	return dir
}

// ---------------------------------------------------------------------
// running install.sh
// ---------------------------------------------------------------------

type runResult struct {
	stdout, stderr string
	exitCode       int
}

func (r runResult) combined() string { return r.stdout + r.stderr }

// hostGoEnv reuses the real machine's Go build cache for the checkout-mode
// tests' `go build` calls — correctness doesn't depend on it (a cold cache
// works fine, just slower), it only keeps the suite fast.
func hostGoEnv(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE", "GOPATH", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		return nil
	}
	return []string{"GOCACHE=" + lines[0], "GOPATH=" + lines[1], "GOMODCACHE=" + lines[2]}
}

// runInstall runs scriptPath (a copy of install.sh, placed wherever a test
// needs it for checkout-mode detection to see or not see) under a fully
// explicit, sandboxed environment: HOME, PATH (stubBin first), and extra
// carries everything else the script or its stubs read (SCRIPTORIUM_APP_DIR,
// FAKE_RELEASE_DIR, FAKE_UNAME_M, ...). Nothing from the test process's own
// ambient environment leaks in except PATH's tail (so real
// git/go/tar/grep/sha256sum are found) and Go's build cache.
func runInstall(t *testing.T, scriptPath, home, stubBin string, extra map[string]string) runResult {
	t.Helper()
	env := []string{
		"HOME=" + home,
		"PATH=" + stubBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		// SCRIPTORIUM_APP_DIR defaults to unset (bash's ${VAR:-default}
		// treats "" identically to unset), so a test that wants the real
		// default just omits it from extra.
		"SCRIPTORIUM_APP_DIR=",
		// pinned: bash re-derives SHELL from the login shell when unset, so
		// on a zsh workstation the rc selection would otherwise follow the
		// HOST, not the scenario (a test overrides via extra).
		"SHELL=/bin/bash",
	}
	env = append(env, hostGoEnv(t)...)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running install.sh: %v", err)
		}
	}
	return runResult{out.String(), errb.String(), code}
}

// copyInstallSh copies the real install.sh under test into dir (binary-mode
// tests: dir has no go.mod/cmd/scriptorium sibling, so CHECKOUT_MODE stays
// 0 — the curl-pipe simulation).
func copyInstallSh(t *testing.T, dir string) string {
	t.Helper()
	dst := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(dst, installShSource(t), 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// ---------------------------------------------------------------------
// 1. binary-mode happy path
// ---------------------------------------------------------------------

func TestBinaryModeHappyPath(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir() // no go.mod/cmd sibling here — curl-pipe shape
	script := copyInstallSh(t, pipeDir)

	release := t.TempDir()
	buildRelease(t, release, "amd64", "fake-binary-v1")

	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release,
		"FAKE_UNAME_M":     "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, output:\n%s", res.exitCode, res.combined())
	}

	appDir := filepath.Join(home, "scriptorium")
	launcher := filepath.Join(home, ".local", "bin", "scriptorium")

	assertFileContent(t, launcher, "fake-binary-v1")
	assertExecutable(t, launcher)
	assertFileContent(t, filepath.Join(appDir, "config.json"), fixtureConfigExample)
	assertFileContent(t, filepath.Join(appDir, ".env"), fixtureEnvExample)

	for _, want := range []string{
		"created config.json — set scriptsRepo and n8nWebhookUrl",
		"created .env — set GITHUB_TOKEN",
		"done. Edit " + appDir + "/config.json + .env, then run: scriptorium",
	} {
		if !strings.Contains(res.combined(), want) {
			t.Errorf("output missing %q\ngot:\n%s", want, res.combined())
		}
	}
}

// ---------------------------------------------------------------------
// 2. re-run: binary overwritten, config never touched — 3 runs
// ---------------------------------------------------------------------

func TestReinstallOverwritesBinaryNeverConfig(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir()
	script := copyInstallSh(t, pipeDir)
	appDir := filepath.Join(home, "scriptorium")
	launcher := filepath.Join(home, ".local", "bin", "scriptorium")

	versions := []string{"fake-binary-v1", "fake-binary-v2", "fake-binary-v3"}
	var userEditedConfig string
	for i, v := range versions {
		release := t.TempDir()
		buildRelease(t, release, "amd64", v)

		res := runInstall(t, script, home, stubBin, map[string]string{
			"FAKE_RELEASE_DIR": release,
			"FAKE_UNAME_M":     "x86_64",
		})
		if res.exitCode != 0 {
			t.Fatalf("run %d: exit = %d, output:\n%s", i+1, res.exitCode, res.combined())
		}
		assertFileContent(t, launcher, v)

		if i == 0 {
			// simulate the user editing config.json after the first install —
			// no later run may touch it.
			userEditedConfig = `{"scriptsRepo":"https://user-edited.invalid/repo.git"}` + "\n"
			if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(userEditedConfig), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		assertFileContent(t, filepath.Join(appDir, "config.json"), userEditedConfig)
		if strings.Contains(res.combined(), "created config.json") && i > 0 {
			t.Errorf("run %d: re-announced config.json creation — it must be silent once the file exists", i+1)
		}
	}
}

// ---------------------------------------------------------------------
// 3. checksum mismatch -> hard fail, nothing installed
// ---------------------------------------------------------------------

func TestChecksumMismatchInstallsNothing(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir()
	script := copyInstallSh(t, pipeDir)

	release := t.TempDir()
	buildRelease(t, release, "amd64", "fake-binary-v1")
	// corrupt the checksums file so the download no longer matches
	if err := os.WriteFile(filepath.Join(release, buildinfo.ChecksumsFile),
		[]byte("0000000000000000000000000000000000000000000000000000000000000000  "+buildinfo.AssetName("linux", "amd64")+"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release,
		"FAKE_UNAME_M":     "x86_64",
	})
	if res.exitCode == 0 {
		t.Fatalf("exit = 0, want non-zero on checksum mismatch; output:\n%s", res.combined())
	}
	if !strings.Contains(res.combined(), "checksum mismatch") {
		t.Errorf("output missing the checksum-mismatch message:\n%s", res.combined())
	}

	launcher := filepath.Join(home, ".local", "bin", "scriptorium")
	if _, err := os.Stat(launcher); err == nil {
		t.Error("the binary landed despite a checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(home, "scriptorium", "config.json")); err == nil {
		t.Error("config.json was bootstrapped despite a checksum mismatch")
	}
}

// ---------------------------------------------------------------------
// 4. arch detect table
// ---------------------------------------------------------------------

func TestArchDetectTable(t *testing.T) {
	cases := []struct {
		unameM   string
		wantArch string // "" means: expect a clear error, nothing installed
	}{
		{"x86_64", "amd64"},
		{"aarch64", "arm64"},
		{"arm64", "arm64"},
		{"riscv64", ""},
	}
	for _, c := range cases {
		t.Run(c.unameM, func(t *testing.T) {
			home := t.TempDir()
			stubBin := newStubBin(t)
			pipeDir := t.TempDir()
			script := copyInstallSh(t, pipeDir)
			release := t.TempDir()
			if c.wantArch != "" {
				buildRelease(t, release, c.wantArch, "fake-binary")
			}

			res := runInstall(t, script, home, stubBin, map[string]string{
				"FAKE_RELEASE_DIR": release,
				"FAKE_UNAME_M":     c.unameM,
			})

			launcher := filepath.Join(home, ".local", "bin", "scriptorium")
			if c.wantArch == "" {
				if res.exitCode == 0 {
					t.Fatalf("unsupported arch %q: exit = 0, want non-zero; output:\n%s", c.unameM, res.combined())
				}
				if !strings.Contains(res.combined(), "unsupported architecture") {
					t.Errorf("unsupported arch %q: missing a clear error message:\n%s", c.unameM, res.combined())
				}
				if _, err := os.Stat(launcher); err == nil {
					t.Errorf("unsupported arch %q: binary landed anyway", c.unameM)
				}
				return
			}
			if res.exitCode != 0 {
				t.Fatalf("uname -m=%s: exit = %d, output:\n%s", c.unameM, res.exitCode, res.combined())
			}
			assertFileContent(t, launcher, "fake-binary")
		})
	}
}

// ---------------------------------------------------------------------
// 5. old script-based launcher gets replaced by the binary
// ---------------------------------------------------------------------

func TestOldLauncherScriptReplaced(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir()
	script := copyInstallSh(t, pipeDir)

	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	oldLauncher := filepath.Join(localBin, "scriptorium")
	if err := os.WriteFile(oldLauncher, []byte("#!/usr/bin/env bash\nexec pwsh -File scriptorium.ps1 \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	release := t.TempDir()
	buildRelease(t, release, "amd64", "fake-binary-v1")
	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release,
		"FAKE_UNAME_M":     "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, output:\n%s", res.exitCode, res.combined())
	}

	if !strings.Contains(res.combined(), "replacing the old launcher script") {
		t.Errorf("output missing the old-launcher announcement:\n%s", res.combined())
	}
	assertFileContent(t, oldLauncher, "fake-binary-v1")
}

// ---------------------------------------------------------------------
// 6. PATH persistence (v1.1.0): marker-idempotent rc append, $SHELL-detected
// ---------------------------------------------------------------------

const pathMarker = "# added by scriptorium install.sh"
const pathExport = `export PATH="$HOME/.local/bin:$PATH"`

// Three runs, one line: the rc is created on the first run, and the marker
// keeps the second and third from appending again.
func TestPathAppendIsIdempotentAcrossThreeRuns(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir()
	script := copyInstallSh(t, pipeDir)

	for i := range 3 {
		release := t.TempDir()
		buildRelease(t, release, "amd64", "fake-binary")
		res := runInstall(t, script, home, stubBin, map[string]string{
			"FAKE_RELEASE_DIR": release,
			"FAKE_UNAME_M":     "x86_64",
		})
		if res.exitCode != 0 {
			t.Fatalf("run %d: exit = %d, output:\n%s", i+1, res.exitCode, res.combined())
		}
		want := "PATH: added ~/.local/bin to "
		if i > 0 {
			want = "PATH: ~/.local/bin already configured in "
		}
		if !strings.Contains(res.combined(), want) {
			t.Errorf("run %d: output missing %q:\n%s", i+1, want, res.combined())
		}
	}

	rc, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil {
		t.Fatalf("no ~/.bashrc was created: %v", err)
	}
	if got := strings.Count(string(rc), pathMarker); got != 1 {
		t.Errorf("marker appears %d times after three runs, want exactly 1:\n%s", got, rc)
	}
	if got := strings.Count(string(rc), pathExport); got != 1 {
		t.Errorf("export line appears %d times, want exactly 1:\n%s", got, rc)
	}
}

// $SHELL picks the rc: zsh appends to ~/.zshrc, everything else to ~/.bashrc.
func TestPathAppendFollowsTheShell(t *testing.T) {
	for shell, rcName := range map[string]string{
		"/usr/bin/zsh": ".zshrc",
		"/bin/bash":    ".bashrc",
		"":             ".bashrc",
	} {
		home := t.TempDir()
		stubBin := newStubBin(t)
		script := copyInstallSh(t, t.TempDir())
		release := t.TempDir()
		buildRelease(t, release, "amd64", "fake-binary")

		res := runInstall(t, script, home, stubBin, map[string]string{
			"FAKE_RELEASE_DIR": release,
			"FAKE_UNAME_M":     "x86_64",
			"SHELL":            shell,
		})
		if res.exitCode != 0 {
			t.Fatalf("SHELL=%q: exit = %d, output:\n%s", shell, res.exitCode, res.combined())
		}
		rc, err := os.ReadFile(filepath.Join(home, rcName))
		if err != nil {
			t.Fatalf("SHELL=%q: expected %s: %v", shell, rcName, err)
		}
		if !strings.Contains(string(rc), pathMarker) || !strings.Contains(string(rc), pathExport) {
			t.Errorf("SHELL=%q: %s does not carry the marker + export:\n%s", shell, rcName, rc)
		}
	}
}

// An unwritable rc degrades to the old plain warning — never a failed install.
func TestPathUnwritableRcFallsBackToWarning(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	script := copyInstallSh(t, t.TempDir())
	release := t.TempDir()
	buildRelease(t, release, "amd64", "fake-binary")

	rc := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(rc, []byte("# locked\n"), 0o444); err != nil {
		t.Fatal(err)
	}

	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release,
		"FAKE_UNAME_M":     "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d — an unwritable rc must not fail the install:\n%s", res.exitCode, res.combined())
	}
	want := `NOTE: ~/.local/bin is not on your PATH — add: export PATH="$HOME/.local/bin:$PATH"`
	if !strings.Contains(res.combined(), want) {
		t.Errorf("output missing the fallback PATH warning:\n%s", res.combined())
	}
	got, _ := os.ReadFile(rc)
	if strings.Contains(string(got), pathMarker) {
		t.Errorf("the unwritable rc was modified:\n%s", got)
	}
}

func TestNoPathWarningWhenAlreadyOnPath(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir()
	script := copyInstallSh(t, pipeDir)
	release := t.TempDir()
	buildRelease(t, release, "amd64", "fake-binary")

	// Put $HOME/.local/bin on PATH too, ahead of the stub bin, matching a
	// normal already-configured shell.
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + localBin + string(os.PathListSeparator) + stubBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SCRIPTORIUM_APP_DIR=",
		"FAKE_RELEASE_DIR=" + release, "FAKE_UNAME_M=x86_64",
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "is not on your PATH") {
		t.Errorf("PATH warning fired even though ~/.local/bin was already on PATH:\n%s", out.String())
	}
}

// ---------------------------------------------------------------------
// helpers shared by the checkout-mode tests
// ---------------------------------------------------------------------

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s content = %q, want %q", path, got, want)
	}
}

func assertExecutable(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable: mode %v", path, fi.Mode())
	}
}

var gitIdentityEnv = []string{
	"GIT_AUTHOR_NAME=install-test", "GIT_AUTHOR_EMAIL=install-test@example.invalid",
	"GIT_COMMITTER_NAME=install-test", "GIT_COMMITTER_EMAIL=install-test@example.invalid",
}

func runGit(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, os.Environ()...), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// minimalModuleFiles are what every fixture git commit needs so `go build
// ./cmd/scriptorium` succeeds regardless of which branch/commit ends up
// checked out.
func writeMinimalModule(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "scriptorium"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scriptoriumfixture\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "scriptorium", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json.example"), []byte(fixtureConfigExample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(fixtureEnvExample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MARKER.txt"), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newBareRepoWithCommit creates a bare repo at <root>/<name>.git seeded with
// one commit (a minimal Go module + a MARKER.txt identifying it) on branch
// main, pushed from a disposable working clone.
func newBareRepoWithCommit(t *testing.T, root, name, marker string) (bareURL string) {
	t.Helper()
	bare := filepath.Join(root, name+".git")
	runGit(t, root, nil, "init", "--bare", "-b", "main", bare)

	work := filepath.Join(root, name+"-seed")
	runGit(t, root, nil, "init", "-b", "main", work)
	writeMinimalModule(t, work, marker)
	runGit(t, work, nil, "add", "-A")
	runGit(t, work, gitIdentityEnv, "commit", "-m", "init "+marker)
	runGit(t, work, nil, "remote", "add", "origin", bare)
	runGit(t, work, nil, "push", "-u", "origin", "main")
	return bare
}

// ---------------------------------------------------------------------
// 7. checkout mode: a failed fast-forward is never destructive — NOTE
//    only, tree left exactly as it was. Checkout-mode git handling is
//    just `fetch` + `pull --ff-only`; there is no other branch to test.
// ---------------------------------------------------------------------

func TestCheckoutModeFastForwardFailureLeavesTreeUntouched(t *testing.T) {
	root := t.TempDir()
	canonical := newBareRepoWithCommit(t, root, "canonical", "canonical")

	appDir := filepath.Join(root, "appdir")
	runGit(t, root, nil, "clone", canonical, appDir)

	// advance the remote with an unrelated commit, via a second clone
	other := filepath.Join(root, "other-clone")
	runGit(t, root, nil, "clone", canonical, other)
	if err := os.WriteFile(filepath.Join(other, "REMOTE_ADVANCE.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, nil, "add", "-A")
	runGit(t, other, gitIdentityEnv, "commit", "-m", "remote advances")
	runGit(t, other, nil, "push", "origin", "main")

	// advance the local checkout with a divergent, never-pushed commit
	if err := os.WriteFile(filepath.Join(appDir, "LOCAL_DIVERGENT.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, appDir, nil, "add", "-A")
	runGit(t, appDir, gitIdentityEnv, "commit", "-m", "local diverges")
	localHead := strings.TrimSpace(runGit(t, appDir, nil, "rev-parse", "HEAD"))

	home := t.TempDir()
	stubBin := newStubBin(t)
	script := copyInstallSh(t, appDir)

	res := runInstall(t, script, home, stubBin, nil)
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, output:\n%s", res.exitCode, res.combined())
	}

	if !strings.Contains(res.combined(), "NOTE: could not fast-forward") {
		t.Errorf("output missing the ff-failure NOTE:\n%s", res.combined())
	}

	if got := strings.TrimSpace(runGit(t, appDir, nil, "rev-parse", "HEAD")); got != localHead {
		t.Errorf("HEAD moved: %s -> %s — the local divergent commit must survive untouched", localHead, got)
	}
	if _, err := os.Stat(filepath.Join(appDir, "LOCAL_DIVERGENT.txt")); err != nil {
		t.Error("the local divergent file is gone — the tree was touched")
	}
	if _, err := os.Stat(filepath.Join(appDir, "REMOTE_ADVANCE.txt")); err == nil {
		t.Error("the remote's file appeared — a merge/reset happened when it must not have")
	}
	assertExecutable(t, filepath.Join(home, ".local", "bin", "scriptorium"))
}

// ---------------------------------------------------------------------
// 8. self-update path (v1.1.0): re-running the one-liner replaces the binary
//    and reports vOLD → vNEW
// ---------------------------------------------------------------------

// versionBinary is a stand-in scriptorium binary: an executable WITHOUT a
// shebang (the '#!' head marks the old pwsh wrapper to install.sh), which the
// shell's ENOEXEC fallback runs as sh — enough to answer `--version` in the
// real CLI's format. The tag makes replacement observable even when the
// version does not change.
func versionBinary(version, tag string) string {
	return "echo \"scriptorium " + version + " (commit " + tag + ", built test)\"\n"
}

func TestUpdateReplacesBinaryAndPrintsOldToNew(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	script := copyInstallSh(t, t.TempDir())

	// first install: v1.0.0
	release := t.TempDir()
	buildRelease(t, release, "amd64", versionBinary("v1.0.0", "old"))
	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release, "FAKE_UNAME_M": "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("first install: exit = %d\n%s", res.exitCode, res.combined())
	}
	if !strings.Contains(res.combined(), "installed scriptorium v1.0.0") {
		t.Errorf("fresh install did not report its version:\n%s", res.combined())
	}

	// the release moves on: v1.1.0
	release2 := t.TempDir()
	buildRelease(t, release2, "amd64", versionBinary("v1.1.0", "new"))
	res = runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release2, "FAKE_UNAME_M": "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("update run: exit = %d\n%s", res.exitCode, res.combined())
	}
	if !strings.Contains(res.combined(), "updated scriptorium v1.0.0 → v1.1.0") {
		t.Errorf("update run did not print vOLD → vNEW:\n%s", res.combined())
	}
	launcher := filepath.Join(home, ".local", "bin", "scriptorium")
	assertFileContent(t, launcher, versionBinary("v1.1.0", "new"))
}

func TestSameVersionRerunSaysSoAndStillRefreshes(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	script := copyInstallSh(t, t.TempDir())
	launcher := filepath.Join(home, ".local", "bin", "scriptorium")

	release := t.TempDir()
	buildRelease(t, release, "amd64", versionBinary("v1.1.0", "first"))
	if res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release, "FAKE_UNAME_M": "x86_64",
	}); res.exitCode != 0 {
		t.Fatalf("first install: exit = %d\n%s", res.exitCode, res.combined())
	}

	// same version again, distinguishable bytes: the re-run must SAY it is
	// current and still refresh the binary on disk
	release2 := t.TempDir()
	buildRelease(t, release2, "amd64", versionBinary("v1.1.0", "second"))
	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release2, "FAKE_UNAME_M": "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("re-run: exit = %d\n%s", res.exitCode, res.combined())
	}
	if !strings.Contains(res.combined(), "scriptorium v1.1.0 is already current — binary refreshed") {
		t.Errorf("same-version re-run did not say so:\n%s", res.combined())
	}
	assertFileContent(t, launcher, versionBinary("v1.1.0", "second"))
}

// ---------------------------------------------------------------------
// 8b. ETXTBSY regression (v1.1.1 hotfix): updating over a binary that is
//     currently executing must succeed via rename, never a write-in-place
//     `cp`. sleepLoopBinarySource is a real compiled program (not a script —
//     a script's text pages belong to its interpreter, not the script file,
//     so it wouldn't exercise the same kernel path) that answers --version
//     immediately and otherwise loops forever, standing in for the
//     scriptorium-mcp systemd service still running the old binary.
// ---------------------------------------------------------------------

const sleepLoopBinarySource = `package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("scriptorium vTEST (commit test, built test)")
		return
	}
	for {
		time.Sleep(time.Second)
	}
}
`

func TestUpdateReplacesRunningBinaryAtomically(t *testing.T) {
	for _, tc := range []struct {
		name          string
		systemctlStub string
		wantHint      bool
	}{
		{"systemctl-active", systemctlActiveStub, true},
		{"systemctl-inactive", systemctlInactiveStub, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stubBin := newStubBin(t)
			writeStub(t, stubBin, "systemctl", tc.systemctlStub) // override the hermetic default
			script := copyInstallSh(t, t.TempDir())

			launcher := filepath.Join(home, ".local", "bin", "scriptorium")
			if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
				t.Fatal(err)
			}

			srcDir := t.TempDir()
			src := filepath.Join(srcDir, "main.go")
			if err := os.WriteFile(src, []byte(sleepLoopBinarySource), 0o644); err != nil {
				t.Fatal(err)
			}
			// no Env override: this runs directly from the test process
			// (unlike install.sh's own sandboxed go build), so it already
			// inherits the real GOCACHE/PATH/etc.
			build := exec.Command("go", "build", "-o", launcher, src)
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("building the fixture binary: %v\n%s", err, out)
			}

			// exec the "already installed" binary in the background — this
			// is the process an ETXTBSY-prone install would step on.
			running := exec.Command(launcher)
			if err := running.Start(); err != nil {
				t.Fatalf("starting the background fixture process: %v", err)
			}
			t.Cleanup(func() {
				_ = running.Process.Kill()
				_, _ = running.Process.Wait()
			})

			release := t.TempDir()
			buildRelease(t, release, "amd64", "fake-binary-v2")
			res := runInstall(t, script, home, stubBin, map[string]string{
				"FAKE_RELEASE_DIR": release,
				"FAKE_UNAME_M":     "x86_64",
			})
			if res.exitCode != 0 {
				t.Fatalf("update over a running binary: exit = %d\n%s", res.exitCode, res.combined())
			}
			if strings.Contains(res.combined(), "Text file busy") {
				t.Errorf("ETXTBSY regressed — install.sh is writing in place again:\n%s", res.combined())
			}
			assertFileContent(t, launcher, "fake-binary-v2")

			if err := running.Process.Signal(syscall.Signal(0)); err != nil {
				t.Errorf("the background process died across the replace (its old inode should have survived): %v", err)
			}

			want := "scriptorium-mcp is running the old binary — restart to apply: systemctl restart scriptorium-mcp"
			if got := strings.Contains(res.combined(), want); got != tc.wantHint {
				t.Errorf("restart hint present = %v, want %v\noutput:\n%s", got, tc.wantHint, res.combined())
			}
		})
	}
}

// ---------------------------------------------------------------------
// 9. prerequisite matrix (v1.1.0): FULL install on apt systems, through the
//    sudo ladder — root direct, sudo -n, no-sudo → WARN per package.
// ---------------------------------------------------------------------

// minimalRealBin symlinks just the real tools install.sh needs, so a matrix
// test's PATH is stubs + this dir and NOTHING else — `command -v pwsh` (or
// sudo) then answers for the scenario, not for whatever the host has.
func minimalRealBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{
		"bash", "sh", "mktemp", "grep", "tar", "cp", "mv", "chmod", "mkdir", "head",
		"awk", "rm", "dirname", "basename", "cat", "sha256sum", "shasum", "gzip",
	} {
		real, err := exec.LookPath(name)
		if err != nil {
			continue // e.g. sha256sum on macOS — install.sh falls back to shasum
		}
		if err := os.Symlink(real, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// recorderStub logs "<name> <args>" to $STUB_RECORD and succeeds.
const recorderStub = `#!/bin/sh
echo "$(basename "$0") $*" >> "$STUB_RECORD"
exit 0
`

// sudoRecorderStub logs the escalated command; `sudo -n true` (the probe)
// succeeds silently.
const sudoRecorderStub = `#!/bin/sh
if [ "$1" = "-n" ]; then exit 0; fi
echo "sudo $*" >> "$STUB_RECORD"
exit 0
`

const idRootStub = "#!/bin/sh\necho 0\n"
const idUserStub = "#!/bin/sh\necho 1000\n"

// brokenVenvPython3 answers --version but fails the venv probe — the Debian
// "python3 without python3-venv" shape the REAL check exists for.
const brokenVenvPython3 = `#!/bin/sh
if [ "$1" = "--version" ]; then echo "Python 3.11.0 (fake)"; exit 0; fi
exit 1
`

// prereqScenario runs install.sh with a controlled PATH (stubs + minimal real
// tools) and returns the run plus the recorded privileged commands.
func prereqScenario(t *testing.T, stubs map[string]string) (runResult, string) {
	t.Helper()
	home := t.TempDir()
	script := copyInstallSh(t, t.TempDir())
	release := t.TempDir()
	buildRelease(t, release, "amd64", versionBinary("v1.1.0", "x"))
	// the Microsoft repo package the pwsh recipe downloads, served by the curl
	// stub exactly like the release assets
	if err := os.WriteFile(filepath.Join(release, "packages-microsoft-prod.deb"), []byte("fake-deb"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubBin := t.TempDir()
	base := map[string]string{"curl": curlStub, "uname": unameStub, "python3": python3Stub}
	for name, body := range base {
		writeStub(t, stubBin, name, body)
	}
	for name, body := range stubs {
		if body == "" {
			os.Remove(filepath.Join(stubBin, name))
			continue
		}
		writeStub(t, stubBin, name, body)
	}

	record := filepath.Join(t.TempDir(), "record")
	cmd := exec.Command("bash", script)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + stubBin + string(os.PathListSeparator) + minimalRealBin(t),
		"SCRIPTORIUM_APP_DIR=",
		"SHELL=/bin/bash",
		"FAKE_RELEASE_DIR=" + release,
		"FAKE_UNAME_M=x86_64",
		"STUB_RECORD=" + record,
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running install.sh: %v", err)
		}
	}
	rec, _ := os.ReadFile(record)
	return runResult{out.String(), "", code}, string(rec)
}

// Root installs everything directly: the Microsoft-repo recipe for pwsh
// (deb → dpkg -i → apt-get update → apt-get install powershell), then the
// python trio — no sudo anywhere.
func TestPrereqRootInstallsEverythingDirectly(t *testing.T) {
	res, rec := prereqScenario(t, map[string]string{
		"id":      idRootStub,
		"apt-get": recorderStub,
		"dpkg":    recorderStub,
		"python3": brokenVenvPython3,
		// no pwsh stub: the controlled PATH has none
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d\n%s\nrecord:\n%s", res.exitCode, res.combined(), rec)
	}
	for _, want := range []string{
		"dpkg -i",
		"packages-microsoft-prod.deb",
		"apt-get update -y",
		"apt-get install -y powershell",
		"apt-get install -y python3 python3-venv python3-pip",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("record missing %q:\n%s", want, rec)
		}
	}
	if strings.Contains(rec, "sudo") {
		t.Errorf("root escalated through sudo:\n%s", rec)
	}
	// the recipe's order: dpkg -i, then apt-get update, then install powershell
	dpkgAt := strings.Index(rec, "dpkg -i")
	psAt := strings.Index(rec, "apt-get install -y powershell")
	if dpkgAt < 0 || psAt < 0 || dpkgAt > psAt {
		t.Errorf("the Microsoft-repo recipe ran out of order:\n%s", rec)
	}
	if !strings.Contains(res.combined(), "installed scriptorium v1.1.0") {
		t.Errorf("the install itself did not complete:\n%s", res.combined())
	}
}

// A non-root user with a cached sudo credential (`sudo -n` succeeds) gets the
// same installs, each through sudo.
func TestPrereqSudoNonInteractive(t *testing.T) {
	res, rec := prereqScenario(t, map[string]string{
		"id":      idUserStub,
		"sudo":    sudoRecorderStub,
		"apt-get": recorderStub, // reached only if sudo were bypassed
		"dpkg":    recorderStub,
		"python3": brokenVenvPython3,
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d\n%s\nrecord:\n%s", res.exitCode, res.combined(), rec)
	}
	for _, want := range []string{
		"sudo dpkg -i",
		"sudo apt-get update -y",
		"sudo apt-get install -y powershell",
		"sudo apt-get install -y python3 python3-venv python3-pip",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("record missing %q:\n%s", want, rec)
		}
	}
}

// No sudo at all: every missing prerequisite becomes a WARN carrying the
// exact manual command — and the install itself still succeeds.
func TestPrereqNoSudoWarnsPerPackageAndNeverFails(t *testing.T) {
	res, rec := prereqScenario(t, map[string]string{
		"id":      idUserStub,
		"apt-get": forbiddenStub, // present, so the apt path IS taken
		"dpkg":    forbiddenStub,
		"python3": brokenVenvPython3,
		// no sudo stub and none in the minimal PATH: command -v sudo fails
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d — a missing prerequisite must never fail the install\n%s", res.exitCode, res.combined())
	}
	out := res.combined()
	for _, want := range []string{
		"WARN: PowerShell 7 (pwsh) is missing and sudo is unavailable",
		"sudo apt-get install -y snapd && sudo snap install powershell --classic",
		"WARN: python3/venv/pip are missing and sudo is unavailable",
		"sudo apt-get install -y python3 python3-venv python3-pip",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if rec != "" {
		t.Errorf("privileged commands ran without sudo:\n%s", rec)
	}
	if !strings.Contains(out, "installed scriptorium v1.1.0") {
		t.Errorf("the install did not complete despite the warnings:\n%s", out)
	}
}

// The venv REAL-check: python3 on PATH but `python3 -m venv --help` failing
// (Debian's stub) still triggers the python install; a working venv does not.
func TestPrereqVenvRealCheck(t *testing.T) {
	// broken venv → install
	_, rec := prereqScenario(t, map[string]string{
		"id":      idRootStub,
		"apt-get": recorderStub,
		"dpkg":    recorderStub,
		"pwsh":    "#!/bin/sh\nexit 0\n",
		"python3": brokenVenvPython3,
	})
	if !strings.Contains(rec, "apt-get install -y python3 python3-venv python3-pip") {
		t.Errorf("a broken venv did not trigger the python install:\n%s", rec)
	}

	// working venv → no python install
	_, rec = prereqScenario(t, map[string]string{
		"id":      idRootStub,
		"apt-get": recorderStub,
		"dpkg":    recorderStub,
		"pwsh":    "#!/bin/sh\nexit 0\n",
	})
	if strings.Contains(rec, "python3") {
		t.Errorf("a working venv still triggered the python install:\n%s", rec)
	}
}

// ---------------------------------------------------------------------
// 10. pwsh snap fallback (v1.1.1 hotfix): the per-version Microsoft repo can
//     set up fine yet not carry a powershell package at all (e.g. non-LTS
//     Ubuntu) — apt-get reports "Unable to locate package" (exit 100), and
//     install.sh must fall back to snap before ever warning.
// ---------------------------------------------------------------------

// aptGetPwshMissingStub records every call and behaves like a real apt-get
// that has no powershell candidate: `install -y powershell` exits 100
// ("E: Unable to locate package"), every other call (update, python trio)
// succeeds — so the test isolates the pwsh/snap path.
const aptGetPwshMissingStub = `#!/bin/sh
echo "apt-get $*" >> "$STUB_RECORD"
case "$*" in
  "install -y powershell")
    echo "E: Unable to locate package powershell" >&2
    exit 100
    ;;
  *)
    exit 0
    ;;
esac
`

func TestPrereqPwshFallsBackToSnapWhenAptLacksPackage(t *testing.T) {
	res, rec := prereqScenario(t, map[string]string{
		"id":      idRootStub,
		"apt-get": aptGetPwshMissingStub,
		"dpkg":    recorderStub,
		"snap":    recorderStub,
		// no pwsh stub: the controlled PATH has none, so MISSING_PWSH=1
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d\n%s\nrecord:\n%s", res.exitCode, res.combined(), rec)
	}
	for _, want := range []string{
		"apt-get install -y powershell",
		"snap install powershell --classic",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("record missing %q:\n%s", want, rec)
		}
	}
	if strings.Contains(res.combined(), "WARN") {
		t.Errorf("the snap fallback succeeded but a WARN was still printed:\n%s", res.combined())
	}
	if !strings.Contains(res.combined(), "installed scriptorium v1.1.0") {
		t.Errorf("the install itself did not complete:\n%s", res.combined())
	}
}

func TestPrereqPwshWarnsBothRoutesWhenNoSnapEither(t *testing.T) {
	res, rec := prereqScenario(t, map[string]string{
		"id":      idRootStub,
		"apt-get": aptGetPwshMissingStub,
		"dpkg":    recorderStub,
		// no snap stub and none in the minimal PATH: command -v snap fails
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d — a missing pwsh must never fail the install\n%s\nrecord:\n%s", res.exitCode, res.combined(), rec)
	}
	if strings.Contains(rec, "snap") {
		t.Errorf("snap ran despite not being on PATH:\n%s", rec)
	}
	out := res.combined()
	// snap isn't even installed here, so the first runnable step must be
	// getting snapd, not a bare `snap install` that would itself fail.
	snapRoute := "sudo apt-get install -y snapd && sudo snap install powershell --classic"
	msRoute := "https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-linux"
	snapAt := strings.Index(out, snapRoute)
	msAt := strings.Index(out, msRoute)
	if snapAt < 0 || msAt < 0 {
		t.Fatalf("WARN missing a manual route (snap=%q ms=%q):\n%s", snapRoute, msRoute, out)
	}
	if snapAt > msAt {
		t.Errorf("WARN lists the Microsoft-repo route before snap, want snap first:\n%s", out)
	}
	if !strings.Contains(out, "installed scriptorium v1.1.0") {
		t.Errorf("the install did not complete despite the warning:\n%s", out)
	}
}
