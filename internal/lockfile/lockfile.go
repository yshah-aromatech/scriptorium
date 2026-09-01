// Package lockfile implements the per-script run lock of the PowerShell app
// (Lock-StoScript / Unlock-StoScript / Test-StoScriptLocked /
// Get-StoRunningScripts, src/Runner.psm1) byte-for-byte: "<name>.lock" in the
// locks dir holding the owning process PID as bare ASCII. A lock whose owner
// is dead is stale and reclaimed; a lock file younger than 10s is left alone
// even when stale, because another process may be mid-reclaim of the same one.
//
// The format is the interop contract with the PowerShell app: both sides must
// read and honour locks the other wrote while a migration runs them side by
// side.
package lockfile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// reclaimGuard is how young a stale lock file may be before a reclaim backs
// off — a fresh file is more likely another process mid-reclaim than a
// genuinely abandoned lock.
const reclaimGuard = 10 * time.Second

// Dir is a locks directory. The zero value is not usable; call NewDir.
type Dir struct{ dir string }

// NewDir returns the lock manager for a locks directory (config.Paths.LocksDir).
func NewDir(dir string) *Dir { return &Dir{dir: dir} }

// Live describes one lock currently held by a live process.
type Live struct {
	Name      string
	OwnerPID  int
	External  bool
	StartedAt time.Time
}

func (d *Dir) file(name string) string { return filepath.Join(d.dir, name+".lock") }

// owner reads the PID a lock file holds. Returns 0 when the file is missing,
// unreadable or does not hold a parseable PID — 0 is never a live owner.
func owner(file string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// alive reports whether a PID names a running process. EPERM means the
// process exists but belongs to another user — still alive. PIDs <= 0 are
// never alive: signal 0 to pid 0 would hit our own process group.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Acquire takes the run lock for a script. release is nil when not acquired;
// holderPID is the live owner blocking us, or 0 when unknown.
func (d *Dir) Acquire(name string) (release func(), holderPID int, acquired bool) {
	file := d.file(name)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(strconv.Itoa(os.Getpid()))
			cerr := f.Close()
			if werr != nil || cerr != nil {
				// a lock file we cannot vouch for is worse than none
				_ = os.Remove(file)
				return nil, 0, false
			}
			return func() { _ = os.Remove(file) }, 0, true
		}
		if !os.IsExist(err) {
			return nil, 0, false
		}
		pid := owner(file)
		if alive(pid) {
			return nil, pid, false
		}
		// a fresh lock file may be another process mid-reclaim of the same
		// stale lock — backing off beats both of us acquiring it
		if st, serr := os.Stat(file); serr == nil && time.Since(st.ModTime()) < reclaimGuard {
			return nil, pid, false
		}
		// stale lock (owner died without cleanup) — reclaim and retry
		if rerr := os.Remove(file); rerr != nil {
			return nil, pid, false
		}
	}
	return nil, 0, false
}

// Probe reports whether a script's lock is held by a live process. Read-only:
// it never acquires and never reclaims, so status and reporting paths can call
// it freely.
func (d *Dir) Probe(name string) bool { return alive(owner(d.file(name))) }

// ListLive returns every script whose lock is held by a live process — runs
// started by this process, by cron, by the MCP server or by another session
// alike. StartedAt is the lock file mtime, i.e. when the run started.
func (d *Dir) ListLive() []Live {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	var out []Live
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".lock" {
			continue
		}
		file := filepath.Join(d.dir, e.Name())
		pid := owner(file)
		if !alive(pid) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		out = append(out, Live{
			Name:      strings.TrimSuffix(e.Name(), ".lock"),
			OwnerPID:  pid,
			External:  pid != os.Getpid(),
			StartedAt: info.ModTime(),
		})
	}
	return out
}
