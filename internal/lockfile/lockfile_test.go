package lockfile_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
)

func dir(t *testing.T) *lockfile.Dir { t.Helper(); return lockfile.NewDir(t.TempDir()) }

// Ported from tests/Runner.Tests.ps1 'Lock-StoScript / Unlock-StoScript'.
func TestAcquireBlocksSecond(t *testing.T) {
	d := dir(t)
	rel, _, ok := d.Acquire("a")
	if !ok {
		t.Fatal("first acquire failed")
	}
	if _, pid, ok2 := d.Acquire("a"); ok2 || pid != os.Getpid() {
		t.Fatalf("second acquire: ok=%v pid=%d, want blocked by own pid", ok2, pid)
	}
	rel()
	if _, _, ok3 := d.Acquire("a"); !ok3 {
		t.Fatal("re-acquire after release failed")
	}
}

func TestStaleReclaimWithFreshnessGuard(t *testing.T) {
	base := t.TempDir()
	d := lockfile.NewDir(base)
	stale := filepath.Join(base, "c.lock")
	if err := os.WriteFile(stale, []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	// fresh stale lock: guarded, NOT reclaimed
	if _, _, ok := d.Acquire("c"); ok {
		t.Fatal("fresh stale lock must not be reclaimed (<10s guard)")
	}
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.Acquire("c"); !ok {
		t.Fatal("aged stale lock must be reclaimed")
	}
}

func TestProbeAndListLive(t *testing.T) {
	base := t.TempDir()
	d := lockfile.NewDir(base)
	rel, _, _ := d.Acquire("live")
	defer rel()
	if err := os.WriteFile(filepath.Join(base, "dead.lock"), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !d.Probe("live") || d.Probe("dead") || d.Probe("absent") {
		t.Fatalf("probe wrong: live=%v dead=%v", d.Probe("live"), d.Probe("dead"))
	}
	lv := d.ListLive()
	if len(lv) != 1 || lv[0].Name != "live" || lv[0].OwnerPID != os.Getpid() || lv[0].External {
		t.Fatalf("ListLive = %+v", lv)
	}
}

// Interop direction 1: a lock written by ANOTHER live process (PS format:
// bare ASCII pid) blocks Go's Acquire and reports that PID.
func TestForeignLiveLockBlocks(t *testing.T) {
	base := t.TempDir()
	d := lockfile.NewDir(base)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	if err := os.WriteFile(filepath.Join(base, "x.lock"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, pid, ok := d.Acquire("x")
	if ok || pid != cmd.Process.Pid {
		t.Fatalf("foreign live lock: ok=%v pid=%d want blocked by %d", ok, pid, cmd.Process.Pid)
	}
	if !d.Probe("x") {
		t.Fatal("Probe must see the foreign live lock")
	}
}

// Interop direction 2: PowerShell's Test-StoScriptLocked sees a Go-held lock.
// Skipped when pwsh is absent (present locally + on ubuntu-latest CI).
func TestPSSeesGoLock(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not on PATH")
	}
	base := t.TempDir()
	d := lockfile.NewDir(base)
	rel, _, ok := d.Acquire("go-held")
	if !ok {
		t.Fatal("acquire failed")
	}
	defer rel()
	repo := findRepoRoot(t)
	script := `Import-Module '` + repo + `/src/Core.psm1','` + repo + `/src/Runner.psm1' -Force -DisableNameChecking
$dir = Join-Path ([IO.Path]::GetTempPath()) ('sto-interop-' + [guid]::NewGuid())
New-Item -ItemType Directory -Path $dir | Out-Null
'{"dataDir":"' + ($dir -replace '\\','/') + '"}' | Set-Content (Join-Path $dir 'config.json')
Initialize-Sto -AppDir $dir
Copy-Item '` + filepath.Join(base, "go-held.lock") + `' (Join-Path (Get-StoPaths).LocksDir 'go-held.lock')
Write-Output (Test-StoScriptLocked -Name 'go-held')
Remove-Item $dir -Recurse -Force`
	out, err := exec.Command(pwsh, "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "True") {
		t.Fatalf("PS did not see the Go lock as live:\n%s", out)
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
