package procstat_test

import (
	"encoding/csv"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/procstat"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

// Round1 is the single rounding rule of the whole app (architecture §3), so
// it is held to the frozen PowerShell [Math]::Round(x,1) table.
func TestRound1MatchesPSRoundingFixture(t *testing.T) {
	dir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "rounding.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("rounding.csv has %d rows", len(rows))
	}
	for _, r := range rows[1:] {
		in, err := strconv.ParseFloat(r[0], 64)
		if err != nil {
			t.Fatalf("bad input %q: %v", r[0], err)
		}
		want, err := strconv.ParseFloat(r[1], 64)
		if err != nil {
			t.Fatalf("bad expectation %q: %v", r[1], err)
		}
		if got := procstat.Round1(in); got != want {
			t.Errorf("Round1(%v) = %v, want %v", in, got, want)
		}
	}
}

// The comm field is the one /proc/<pid>/stat field that can contain spaces
// and parens, so every parse must start after the LAST ')'.
func TestParseStatAfterComm(t *testing.T) {
	// state ppid pgrp sid tty tpgid flags minflt cminflt majflt cmajflt
	// utime stime cutime cstime prio nice threads itreal start vsize rss
	const tail = "S 1 1234 1234 0 -1 4194304 100 0 0 0 11 22 0 0 20 0 1 0 12345 123456 33 18446744073709551615\n"

	cases := []struct {
		name string
		line string
	}{
		{"plain", "1234 (bash) " + tail},
		{"space in comm", "1234 (my proc) " + tail},
		{"parens in comm", "1234 (comm (with) parens) " + tail},
		{"only closing paren in comm", "1234 (weird)name) " + tail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := procstat.ParseStatAfterComm(c.line)
			if len(f) < 22 {
				t.Fatalf("got %d fields: %q", len(f), f)
			}
			for _, want := range []struct {
				idx int
				val string
			}{{0, "S"}, {1, "1"}, {11, "11"}, {12, "22"}, {21, "33"}} {
				if f[want.idx] != want.val {
					t.Errorf("field %d = %q, want %q", want.idx, f[want.idx], want.val)
				}
			}
		})
	}

	for _, bad := range []string{"", "1234 (bash", "no parens at all", "1234 (bash)"} {
		if f := procstat.ParseStatAfterComm(bad); len(f) != 0 {
			t.Errorf("ParseStatAfterComm(%q) = %q, want no fields", bad, f)
		}
	}
}

func TestDownsampleShortSeriesPassesThroughRounded(t *testing.T) {
	in := []float64{0.05, 1.15, 7.317, 99.95}
	want := []float64{0, 1.2, 7.3, 100}
	if got := procstat.Downsample(in, 60); !reflect.DeepEqual(got, want) {
		t.Fatalf("Downsample = %v, want %v", got, want)
	}
	if got := procstat.Downsample(nil, 60); len(got) != 0 {
		t.Fatalf("Downsample(nil) = %v, want empty", got)
	}
}

// The P0 Pester case: 1..100 downsampled to 10 max-of-bucket points.
func TestDownsampleMaxOfBucket(t *testing.T) {
	in := make([]float64, 100)
	for i := range in {
		in[i] = float64(i + 1)
	}
	got := procstat.Downsample(in, 10)
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	if got[0] != 10 {
		t.Errorf("first = %v, want 10", got[0])
	}
	if got[9] != 100 {
		t.Errorf("last = %v, want 100", got[9])
	}
	want := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Downsample = %v, want %v", got, want)
	}
}

func TestHasProcIsStable(t *testing.T) {
	first := procstat.HasProc()
	if first != procstat.HasProc() {
		t.Fatal("HasProc changed between calls")
	}
	if runtime.GOOS == "linux" && !first {
		t.Fatal("HasProc = false on linux")
	}
}

// requireProc gates the tests that need a real /proc. Linux-only by
// construction; everywhere else there is nothing to sample.
func requireProc(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("/proc sampling is linux-only")
	}
	if !procstat.HasProc() {
		t.Fatal("linux without /proc/self/stat")
	}
}

// spawnTree starts a shell that forks two sleeps, so the tree under the
// root has more than one member, and returns the root pid.
func spawnTree(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 5 & sleep 5")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	// give sh time to fork its children
	time.Sleep(300 * time.Millisecond)
	return cmd.Process.Pid
}

func TestTreePIDsAndSnapshot(t *testing.T) {
	requireProc(t)
	root := spawnTree(t)
	pids := procstat.TreePIDs(root)
	if len(pids) < 2 {
		t.Fatalf("TreePIDs(%d) = %v, want at least the shell and a child", root, pids)
	}
	if pids[0] != root {
		t.Errorf("TreePIDs[0] = %d, want the root %d", pids[0], root)
	}
	_, rss := procstat.Snapshot(pids)
	if rss == 0 {
		t.Errorf("Snapshot rss = 0, want > 0")
	}
	// a pid that cannot exist is skipped silently, not fatal
	if j, r := procstat.Snapshot([]int{1 << 30}); j != 0 || r != 0 {
		t.Errorf("Snapshot(vanished) = (%d, %d), want (0, 0)", j, r)
	}
	// TreePIDs of a pid with no /proc entry still includes the root
	if got := procstat.TreePIDs(1 << 30); len(got) != 1 || got[0] != 1<<30 {
		t.Errorf("TreePIDs(absent) = %v, want just the root", got)
	}
}

func TestSamplerBaselineThenSample(t *testing.T) {
	requireProc(t)
	root := spawnTree(t)
	s := procstat.NewSampler(root)
	now := time.Now()
	if _, pids, ok := s.Tick(now); ok || len(pids) == 0 {
		t.Fatalf("first tick: ok = %v (want false), pids = %v", ok, pids)
	}
	// a tick inside the 0.2s guard is not a sample
	if _, _, ok := s.Tick(now.Add(100 * time.Millisecond)); ok {
		t.Error("tick within 0.2s reported a sample")
	}
	time.Sleep(500 * time.Millisecond)
	sample, pids, ok := s.Tick(time.Now())
	if !ok {
		t.Fatal("second tick did not sample")
	}
	if len(pids) < 2 {
		t.Errorf("sampler pids = %v, want the whole tree", pids)
	}
	if sample.CPU < 0 || sample.CPU > 100 {
		t.Errorf("CPU = %v, want within [0,100]", sample.CPU)
	}
	if sample.MemMB <= 0 {
		t.Errorf("MemMB = %v, want > 0", sample.MemMB)
	}
	if math.IsNaN(sample.CPU) || math.IsNaN(sample.MemMB) {
		t.Error("sample carries NaN")
	}
}

// A tree whose total jiffies drop (a child exited) must clamp to 0, never
// wrap around an unsigned subtraction into 100%.
func TestSamplerNeverReportsNegativeCPU(t *testing.T) {
	requireProc(t)
	root := spawnTree(t)
	s := procstat.NewSampler(root)
	s.Tick(time.Now())
	time.Sleep(300 * time.Millisecond)
	// far-future clock: dt is huge, so the delta divided by it is ~0
	sample, _, ok := s.Tick(time.Now().Add(time.Hour))
	if !ok {
		t.Fatal("tick did not sample")
	}
	if sample.CPU < 0 {
		t.Fatalf("CPU = %v", sample.CPU)
	}
}

func TestParseStatAfterCommHandlesRealSelfStat(t *testing.T) {
	requireProc(t)
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	f := procstat.ParseStatAfterComm(string(b))
	if len(f) < 22 {
		t.Fatalf("/proc/self/stat parsed to %d fields", len(f))
	}
	if _, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); err != nil {
		t.Fatalf("ppid field %q: %v", f[1], err)
	}
}
