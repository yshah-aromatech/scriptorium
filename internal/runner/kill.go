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

// reaped reports whether the run's root process has already been waited for
// (done closes on the reap, in supervise). Once it has, the kernel is free to
// hand its pid — and with it this process group id — to something else, so no
// group signal may be sent any more. It is PS's `if (-not $proc -or
// $proc.HasExited) { return }` at the top of Stop-StoRun.
func reaped(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// killTree takes down a whole run: SIGTERM the process group, wait up to the
// grace period for it to go, then SIGKILL the group AND every pid of the last
// tree snapshot that is still alive.
//
// Every group signal is gated on the root still being unreaped, checked afresh
// before each one — the pid is only ours to signal until it has been waited
// for. A killTree that arrives after the reap therefore sends nothing to the
// group at all, and does the snapshot pass alone.
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
	if !reaped(done) {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(killGrace):
		}
		if !reaped(done) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}

	if !procstat.HasProc() {
		return
	}
	if len(snapshot) == 0 {
		// the sampler had not ticked yet (a run killed inside its first
		// monitor interval) — walk the tree now, the way PS always does
		snapshot = procstat.TreePIDs(pgid)
	}
	// the snapshot pass runs either way: it is the only thing that reaches an
	// escapee that left the process group (setsid) or reparented to init.
	// Its staleness window is narrower but real — the snapshot is up to one
	// monitor interval old (default 1s), so the /proc check below proves the
	// pid still exists, not that it is still the same process. That is the
	// price of reaching escapees at all, and PS takes the same risk with the
	// tree walk it does at kill time.
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
