package runner

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// canary starts a process in a process group of its own and reports when it
// exits. Nothing else is in that group, so a group signal reaching it can only
// have come from killTree.
func canary(t *testing.T) (pgid int, exited <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "5")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd.Process.Pid, done
}

// The snapshot escalation is not what these tests are about, so they pass a
// pid that cannot exist: non-empty (so killTree does not re-walk the tree) and
// absent from /proc (so the escalation loop skips it).
var noSnapshot = []int{1 << 30}

// A killTree that arrives after the root was reaped must send NOTHING to the
// process group: the reaped pid — and with it the group id — is the kernel's
// to hand out again, so both group signals would be aimed at a stranger.
func TestKillTreeSendsNoGroupSignalAfterTheReap(t *testing.T) {
	pgid, exited := canary(t)
	alreadyReaped := make(chan struct{})
	close(alreadyReaped)

	start := time.Now()
	killTree(pgid, noSnapshot, alreadyReaped)
	if elapsed := time.Since(start); elapsed > killGrace {
		t.Errorf("killTree took %v — it waited out the grace period instead of returning on the reap", elapsed)
	}

	select {
	case <-exited:
		t.Fatal("the canary group was signalled after the root had been reaped")
	case <-time.After(300 * time.Millisecond):
	}
}

// The control: with the root still unreaped, the group signal does go out —
// otherwise the test above would pass on a killTree that signals nobody.
func TestKillTreeSignalsTheGroupWhileTheRootLives(t *testing.T) {
	pgid, exited := canary(t)
	done := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond) // stands in for the reap, so the grace is short
		close(done)
	}()

	killTree(pgid, noSnapshot, done)

	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("the group SIGTERM never reached the canary")
	}
}
