// Package procstat samples a process tree's CPU and RSS out of /proc, the
// port of Get-StoTreePids / Measure-StoResources / Get-StoDownsampledSeries
// (src/Runner.psm1). It is a leaf: no imports outside the standard library,
// no goroutines. /proc is absent on non-Linux hosts, where HasProc reports
// false and runs simply carry no resource numbers.
package procstat

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// userHZ is the unit of the utime/stime fields in /proc/<pid>/stat. It is
// part of the /proc ABI and fixed at 100 on every Linux the app runs on,
// regardless of the kernel's actual CONFIG_HZ — so no `getconf CLK_TCK`
// shell-out (the PS app's one, which falls back to 100 anyway).
const userHZ = 100

// cpuCount is the divisor that makes CPU% relative to the WHOLE machine
// (all cores = 100%), so a multi-threaded tree never reads above 100.
var cpuCount = math.Max(1, float64(runtime.NumCPU()))

// pageSize is the RSS field's unit.
var pageSize = uint64(os.Getpagesize())

// hasProc is computed once: the /proc mount does not come and go.
var hasProc = sync.OnceValue(func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
})

// HasProc reports whether this host exposes /proc, i.e. whether tree walking
// and sampling can work at all.
func HasProc() bool { return hasProc() }

// Sample is one resource reading of a process tree.
type Sample struct {
	CPU   float64 // percent of the whole machine, clamped to [0,100]
	MemMB float64 // resident set of the whole tree
}

// ParseStatAfterComm splits a /proc/<pid>/stat line into the fields that
// follow the comm field. comm is the only field that can contain spaces and
// parentheses, so the split must start after the LAST ')' — index 0 of the
// result is state, 1 is ppid, 11 utime, 12 stime, 21 rss (in pages).
// A line with no ')' (or nothing after it) has no fields.
func ParseStatAfterComm(line string) []string {
	i := strings.LastIndexByte(line, ')')
	if i < 0 || i+2 > len(line) {
		return nil
	}
	return strings.Split(line[i+2:], " ")
}

// statFields reads and splits one process's stat file.
func statFields(pid int) ([]string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil, false
	}
	f := ParseStatAfterComm(string(b))
	return f, len(f) >= 22
}

// TreePIDs returns root and every descendant, from one pass over /proc
// building a ppid -> children map. root is always included, even when it has
// already vanished from /proc.
func TreePIDs(root int) []int {
	children := map[int][]int{}
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, e := range entries {
			pid, cerr := strconv.Atoi(e.Name())
			if cerr != nil {
				continue
			}
			f, ok := statFields(pid)
			if !ok {
				continue
			}
			ppid, perr := strconv.Atoi(f[1])
			if perr != nil {
				continue
			}
			children[ppid] = append(children[ppid], pid)
		}
	}
	var out []int
	stack := []int{root}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, p)
		stack = append(stack, children[p]...)
	}
	return out
}

// Snapshot totals utime+stime jiffies and resident bytes across pids. A pid
// that vanished mid-walk (or whose stat is unreadable or malformed) is
// skipped silently — a tree is a moving target by nature.
func Snapshot(pids []int) (jiffies, rssBytes uint64) {
	for _, p := range pids {
		f, ok := statFields(p)
		if !ok {
			continue
		}
		utime, err1 := strconv.ParseUint(f[11], 10, 64)
		stime, err2 := strconv.ParseUint(f[12], 10, 64)
		rss, err3 := strconv.ParseUint(f[21], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		jiffies += utime + stime
		rssBytes += rss * pageSize
	}
	return jiffies, rssBytes
}

// Sampler turns successive tree snapshots into CPU%/memory samples. It is
// not safe for concurrent use: the runner drives one from its sampler
// goroutine only.
type Sampler struct {
	root     int
	have     bool
	lastTime time.Time
	lastJiff uint64
}

// NewSampler returns a sampler rooted at a process tree.
func NewSampler(root int) *Sampler { return &Sampler{root: root} }

// Tick walks the tree and returns a sample. ok is false for the first tick
// (which only records the baseline) and for any tick less than 0.2s after
// the last accepted one — too short an interval makes jiffy granularity
// dominate. pids is the snapshot just walked, which the killer reuses so it
// can reach processes that reparent away from the root.
func (s *Sampler) Tick(now time.Time) (Sample, []int, bool) {
	pids := TreePIDs(s.root)
	jiff, rss := Snapshot(pids)
	memMB := float64(rss) / (1024 * 1024)
	if !s.have {
		s.have, s.lastTime, s.lastJiff = true, now, jiff
		return Sample{}, pids, false
	}
	dt := now.Sub(s.lastTime).Seconds()
	if dt <= 0.2 {
		return Sample{}, pids, false
	}
	// signed: a tree whose members exited totals FEWER jiffies than last
	// time, and an unsigned subtraction would wrap that into a huge CPU%
	delta := int64(jiff) - int64(s.lastJiff)
	cpu := float64(delta) / userHZ / dt * 100.0 / cpuCount
	if cpu < 0 {
		cpu = 0
	}
	if cpu > 100 { // jiffy-granularity rounding can overshoot
		cpu = 100
	}
	s.lastTime, s.lastJiff = now, jiff
	return Sample{CPU: cpu, MemMB: memMB}, pids, true
}

// Round1 is the app's single rounding rule: half-to-even at one decimal,
// matching PowerShell's [Math]::Round(x, 1). Go's math.Round is half-away-
// from-zero, which would make payloads diverge from the PS app's.
func Round1(x float64) float64 { return math.RoundToEven(x*10) / 10 }

// Downsample reduces a series to at most maxPoints by taking the MAXIMUM of
// each bucket — peaks are what a resource sparkline is read for, so they
// must survive. Every returned value is Round1'd. Port of
// Get-StoDownsampledSeries.
func Downsample(series []float64, maxPoints int) []float64 {
	n := len(series)
	if n <= maxPoints {
		out := make([]float64, n)
		for i, v := range series {
			out[i] = Round1(v)
		}
		return out
	}
	out := make([]float64, 0, maxPoints)
	for b := 0; b < maxPoints; b++ {
		lo := b * n / maxPoints // integer division is the floor PS takes
		hi := (b+1)*n/maxPoints - 1
		if hi < lo {
			hi = lo
		}
		max := series[lo]
		for i := lo + 1; i <= hi; i++ {
			if series[i] > max {
				max = series[i]
			}
		}
		out = append(out, Round1(max))
	}
	return out
}
