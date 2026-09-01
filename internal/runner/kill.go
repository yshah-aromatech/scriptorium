package runner

import (
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/procstat"
)

// killGrace is how long a SIGTERM'd tree has to leave on its own before the
// SIGKILL.
const killGrace = 3 * time.Second

// killTree takes down a whole run: SIGTERM the process group, wait up to the
// grace period for it to go (done closes when the run's pipes have closed,
// i.e. nothing in the tree holds them any more), then SIGKILL the group AND
// every pid of the last tree snapshot that is still alive.
//
// The snapshot escalation is the part that matters. Children that ignored
// TERM are reparented to init the moment the root exits, so a re-walk from
// the root misses exactly the processes that need escalating; and a child
// that called setsid left the process group altogether, so -pgid never
// reaches it at all. Off Linux there is no /proc to check, so the group
// signals are all there is.
func killTree(pgid int, snapshot []int, done <-chan struct{}) {
	if pgid <= 0 {
		return // a signal to group 0 would hit our own process group
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(killGrace):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)

	if !procstat.HasProc() {
		return
	}
	if len(snapshot) == 0 {
		// the sampler had not ticked yet (a run killed inside its first
		// monitor interval) — walk the tree now, the way PS always does
		snapshot = procstat.TreePIDs(pgid)
	}
	for _, p := range snapshot {
		if p <= 0 {
			continue
		}
		if _, err := os.Stat("/proc/" + strconv.Itoa(p)); err != nil {
			continue
		}
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}
