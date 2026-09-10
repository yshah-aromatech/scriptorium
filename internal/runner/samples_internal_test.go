package runner

import (
	"context"
	"github.com/yshah-aromatech/scriptorium/internal/procstat"
	"testing"
)

func TestResourceSeriesBoundedAcrossFullRun(t *testing.T) {
	s := &supervisor{ctx: context.Background(), events: make(chan Event, 1)}
	var cpuSum, memSum float64
	const n = 100000
	for i := range n {
		sm := procstat.Sample{CPU: float64(i % 17), MemMB: float64(i % 31)}
		if i == 0 {
			sm.CPU = 1000
		}
		if i == n/2 {
			sm.MemMB = 2000
		}
		if i == n-1 {
			sm.CPU = 500
		}
		cpuSum += sm.CPU
		memSum += sm.MemMB
		s.onSample(sm)
		<-s.events
		if len(s.cpuSeries) > 1024 || len(s.memSeries) > 1024 {
			t.Fatalf("unbounded retained samples at %d: %d/%d", i, len(s.cpuSeries), len(s.memSeries))
		}
	}
	if s.sampleCount != n || s.cpuSum != cpuSum || s.memSum != memSum || s.cpuMax != 1000 || s.memMax != 2000 {
		t.Fatal("compaction changed full-run aggregates")
	}
	cpu, mem := procstat.Downsample(s.cpuSeries, 60), procstat.Downsample(s.memSeries, 60)
	if cpu[0] != 1000 || cpu[len(cpu)-1] != 500 {
		t.Fatalf("lost early/late CPU peaks: %v", cpu)
	}
	found := -1
	for i, v := range mem {
		if v == 2000 {
			found = i
		}
	}
	if found < 28 || found > 32 {
		t.Fatalf("middle-run peak shifted to %d: %v", found, mem)
	}
}
