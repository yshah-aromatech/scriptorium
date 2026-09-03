// Package installtest hermetically exercises install.sh end to end: a stub
// curl serves a real tarball this file builds (with a real sha256
// checksums.txt alongside it), stub apt-get/dpkg/sudo fail loudly so any
// accidental system mutation surfaces as a test failure, and HOME/PATH are
// fully sandboxed per test. Checkout-mode tests use real git against fully
// local, disposable bare repos (never the real scriptorium remote). No test
// in this package touches the network, the real crontab, or systemd.
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
// FAKE_RELEASE_DIR, FAKE_UNAME_M, SCRIPTORIUM_TEST_REPO_URL, ...). Nothing
// from the test process's own ambient environment leaks in except PATH's
// tail (so real git/go/tar/grep/sha256sum are found) and Go's build cache.
func runInstall(t *testing.T, scriptPath, home, stubBin string, extra map[string]string) runResult {
	t.Helper()
	env := []string{
		"HOME=" + home,
		"PATH=" + stubBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		// SCRIPTORIUM_APP_DIR/PSSCRIPTS_APP_DIR default to unset (bash's
		// ${VAR:-default} treats "" identically to unset), so a test that
		// wants the real chain default just omits them from extra.
		"SCRIPTORIUM_APP_DIR=",
		"PSSCRIPTS_APP_DIR=",
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
// 5. legacy launcher + psscripts cleanup
// ---------------------------------------------------------------------

func TestLegacyLauncherAndPsscriptsCleanup(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(localBin, "psscripts"), []byte("#!/usr/bin/env bash\nexec scriptorium \"$@\"\n"), 0o755); err != nil {
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
	if !strings.Contains(res.combined(), "removed legacy 'psscripts' launcher") {
		t.Errorf("output missing the psscripts cleanup announcement:\n%s", res.combined())
	}
	assertFileContent(t, oldLauncher, "fake-binary-v1")
	if _, err := os.Stat(filepath.Join(localBin, "psscripts")); err == nil {
		t.Error("psscripts launcher was not removed")
	}
}

// ---------------------------------------------------------------------
// 6. PATH warning
// ---------------------------------------------------------------------

func TestPathWarning(t *testing.T) {
	home := t.TempDir()
	stubBin := newStubBin(t)
	pipeDir := t.TempDir()
	script := copyInstallSh(t, pipeDir)
	release := t.TempDir()
	buildRelease(t, release, "amd64", "fake-binary")

	res := runInstall(t, script, home, stubBin, map[string]string{
		"FAKE_RELEASE_DIR": release,
		"FAKE_UNAME_M":     "x86_64",
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, output:\n%s", res.exitCode, res.combined())
	}
	want := `NOTE: ~/.local/bin is not on your PATH — add: export PATH="$HOME/.local/bin:$PATH"`
	if !strings.Contains(res.combined(), want) {
		t.Errorf("output missing the PATH warning; want it to contain:\n%s\ngot:\n%s", want, res.combined())
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
		"SCRIPTORIUM_APP_DIR=", "PSSCRIPTS_APP_DIR=",
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
// 7. checkout mode: correct origin, local history diverged -> NOTE only,
//    tree untouched
// ---------------------------------------------------------------------

func TestCheckoutModeCorrectOriginDivergenceLeavesTreeUntouched(t *testing.T) {
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

	res := runInstall(t, script, home, stubBin, map[string]string{
		"SCRIPTORIUM_TEST_REPO_URL": canonical,
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, output:\n%s", res.exitCode, res.combined())
	}

	if !strings.Contains(res.combined(), "NOTE: could not fast-forward") {
		t.Errorf("output missing the ff-failure NOTE:\n%s", res.combined())
	}
	for _, unwanted := range []string{"repointing origin", "resetting to scriptorium main"} {
		if strings.Contains(res.combined(), unwanted) {
			t.Errorf("output contains %q — the correct-origin path must never reset\n%s", unwanted, res.combined())
		}
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
// 8. checkout mode: repointed (foreign) origin -> hard reset onto canonical
// ---------------------------------------------------------------------

func TestCheckoutModeRepointedOriginResets(t *testing.T) {
	root := t.TempDir()
	canonical := newBareRepoWithCommit(t, root, "canonical", "canonical")
	foreign := newBareRepoWithCommit(t, root, "foreign", "foreign")

	appDir := filepath.Join(root, "appdir")
	runGit(t, root, nil, "clone", foreign, appDir)
	if got := strings.TrimSpace(runGit(t, appDir, nil, "remote", "get-url", "origin")); got != foreign {
		t.Fatalf("test setup: origin = %q, want %q", got, foreign)
	}

	home := t.TempDir()
	stubBin := newStubBin(t)
	script := copyInstallSh(t, appDir)

	res := runInstall(t, script, home, stubBin, map[string]string{
		"SCRIPTORIUM_TEST_REPO_URL": canonical,
	})
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, output:\n%s", res.exitCode, res.combined())
	}

	for _, want := range []string{"repointing origin -> " + canonical, "old install history diverged — resetting to scriptorium main"} {
		if !strings.Contains(res.combined(), want) {
			t.Errorf("output missing %q\ngot:\n%s", want, res.combined())
		}
	}
	if strings.Contains(res.combined(), "NOTE: could not fast-forward") {
		t.Errorf("output contains the ff-failure NOTE — the repointed path must reset, not note\n%s", res.combined())
	}

	if got := strings.TrimSpace(runGit(t, appDir, nil, "remote", "get-url", "origin")); got != canonical {
		t.Errorf("origin = %q, want repointed to %q", got, canonical)
	}
	if _, err := os.Stat(filepath.Join(appDir, "MARKER.txt")); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(appDir, "MARKER.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(marker)) != "canonical" {
		t.Errorf("MARKER.txt = %q after reset, want the canonical repo's content", marker)
	}
	assertExecutable(t, filepath.Join(home, ".local", "bin", "scriptorium"))
}
