package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
)

func TestPreparationReservesSlotBeforeCommandsRun(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	first := m.run.start(m, m.scripts[0])
	if !m.run.active() {
		t.Error("dependency scan did not reserve active ownership")
	}
	second := m.scripts[0]
	second.Name = "second"
	m.scripts = append(m.scripts, second)
	m.run.start(m, second)
	m.run.start(m, m.scripts[0])
	if len(m.run.queue) != 2 {
		t.Fatalf("rapid requests launched independently; queue=%v", m.run.queue)
	}
	scan := first().(DepsScannedMsg)
	launch := m.run.onDepsScanned(m, scan)
	if !m.run.active() {
		t.Error("launch command lost ownership before execution")
	}
	m.run.start(m, second)
	if len(m.run.queue) != 3 {
		t.Error("request during launch did not queue")
	}
	m.run.clearQueue()
	drive(t, m, launch, 10*time.Second)
}

func TestQueuedRunChecksDependencies(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	write(t, ta.scan, `{"deps":[],"missing":[{"name":"Az","display":"Az"}],"params":[]}`)
	m := toolModel(t, ta)
	m.run.queue = []queued{{Name: m.scripts[0].Name}}
	pump(t, m, m.run.dequeue(m), 10*time.Second, nil)
	if m.ov == nil || m.ov.kind() != overlayDeps {
		t.Errorf("queued run bypassed dependency prompt; calls=%v", ta.recorded(t))
	}
	if got := strings.Join(ta.recorded(t), ","); got != "scan" {
		t.Errorf("queued preparation calls=%q", got)
	}
}

func TestCancelledPreparationDoesNotLaunch(t *testing.T) {
	for _, phase := range []string{"scan", "launch"} {
		t.Run(phase, func(t *testing.T) {
			ta := seedToolApp(t, "echo abandoned\n")
			m := toolModel(t, ta)
			cmd := m.run.start(m, m.scripts[0])
			if phase == "launch" {
				cmd = m.run.onDepsScanned(m, cmd().(DepsScannedMsg))
			}
			m.run.kill(m)
			if !m.run.active() {
				t.Error("cancel released ownership before pending command acknowledged")
			}
			drive(t, m, cmd, 10*time.Second)
			if got := strings.Join(ta.recorded(t), ","); strings.Contains(got, "run") {
				t.Errorf("cancelled %s still launched: %s", phase, got)
			}
			if m.run.active() {
				t.Error("cancelled preparation never released ownership")
			}
		})
	}
}

func TestQuitDoesNotCancelUntilConfirmed(t *testing.T) {
	m := runAt(t, 120, 40)
	cancelled := false
	m.run.task = &task{name: "test", cancel: func() { cancelled = true }}
	m.quitCmd()
	if cancelled {
		t.Fatal("opening quit confirmation cancelled the task before consent")
	}
	press(m, "n")
	if cancelled {
		t.Fatal("declining quit cancelled the task")
	}
}

func TestActiveRunShowsErrors(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.handle = &runner.Handle{Name: "busy"}
	m.Update(ErrMsg{Context: "saving", Err: errors.New("failed")})
	if !strings.Contains(m.statusBar(), "saving: failed") {
		t.Fatalf("active run hides error: %q", m.statusBar())
	}
	m.Update(StatusMsg{Kind: StatusWarn, Text: "persistence warning"})
	if !strings.Contains(m.statusBar(), "persistence warning") {
		t.Fatal("active run hides warning")
	}
}

func TestPreparationStatusNamesQueue(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	m.run.start(m, m.scripts[0])
	m.run.queue = []queued{{Name: "next-script"}}
	line, busy := m.run.statusLine(m, 120)
	if !busy || !strings.Contains(line, "preparing") || !strings.Contains(line, "1 queued") || !strings.Contains(line, "next-script") {
		t.Errorf("preparation status=%q busy=%v", line, busy)
	}
}

// Pauses the real runner in venv provisioning, before it can return a handle.
func TestCancelDuringPythonLaunchAwaitsProvisioning(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	ready := filepath.Join(t.TempDir(), "ready")
	py := filepath.Join(t.TempDir(), "python")
	write(t, py, fmt.Sprintf("#!/bin/sh\necho $$ > %q\nexec sleep 30\n", ready))
	if err := os.Chmod(py, 0755); err != nil {
		t.Fatal(err)
	}
	ta.Cfg.PythonBin = py
	s := m.scripts[0]
	s.Runtime = "python"
	cmd := m.run.launch(m, s, nil)
	results := make(chan tea.Msg, 1)
	go func() { results <- cmd() }()
	waitFile(t, ready)
	m.run.kill(m)
	select {
	case msg := <-results:
		_, next := m.Update(msg)
		drive(t, m, next, 5*time.Second)
	case <-time.After(2 * time.Second):
		b, _ := os.ReadFile(ready)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		_ = syscall.Kill(pid, syscall.SIGKILL)
		msg := <-results
		_, next := m.Update(msg)
		drive(t, m, next, 5*time.Second)
		t.Fatal("cancelled launch left venv provisioning running")
	}
	if m.run.active() {
		t.Error("launch cancellation did not finalize")
	}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child did not signal readiness: %s", path)
}

func TestScanCompletionWaitsForEditor(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	write(t, ta.scan, `{"missing":[{"name":"Az","display":"Az"}]}`)
	cmd := m.run.start(m, m.scripts[0])
	ed := newEnvEditor(m, m.scripts[0])
	m.open(ed)
	m.run.onDepsScanned(m, cmd().(DepsScannedMsg))
	if m.ov != ed {
		t.Fatal("dependency completion replaced the user's editor")
	}
	m.closeOverlay()
	m.run.resumePreparation(m)
	if m.ov == nil || m.ov.kind() != overlayDeps {
		t.Fatal("deferred dependency prompt did not resume")
	}
}

func TestStaleLifecycleMessagesCannotReplaceOwnership(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	scanCmd := m.run.start(m, m.scripts[0])
	old := scanCmd().(DepsScannedMsg)
	m.run.kill(m)
	pump(t, m, m.run.onDepsScanned(m, old), 5*time.Second, nil)
	next := m.run.start(m, m.scripts[0])
	current := m.run.attempt
	if cmd := m.run.onDepsScanned(m, old); cmd != nil {
		t.Fatal("stale scan was accepted")
	}
	m.run.onRunStartFailed(m, RunStartFailedMsg{Attempt: old.Attempt, Err: errors.New("old")})
	oldHandle := fakeHandle("old")
	m.run.onRunEvents(m, RunEventsMsg{Handle: oldHandle, Batch: []runner.Event{{Kind: runner.EvLine, Line: "stale output"}}, Closed: true})
	m.run.onRunDone(m, RunDoneMsg{Handle: oldHandle})
	if m.run.attempt != current || strings.Contains(m.run.out.allText(), "stale output") {
		t.Fatal("stale completion changed the current attempt")
	}
	drive(t, m, next, 10*time.Second)
}

func TestQuitWaitsForPendingPreparation(t *testing.T) {
	for _, phase := range []string{"scan", "launch"} {
		t.Run(phase, func(t *testing.T) {
			ta := seedToolApp(t, "exit 0\n")
			m := toolModel(t, ta)
			pending := m.run.start(m, m.scripts[0])
			if phase == "launch" {
				pending = m.run.onDepsScanned(m, pending().(DepsScannedMsg))
			}
			m.run.start(m, m.scripts[0])
			m.quitCmd()
			yes := press(m, "y")
			if !m.run.quitting || len(m.run.queue) != 0 {
				t.Fatal("confirmed quit did not clear queued work")
			}
			if containsQuit(yes) {
				t.Fatal("quit returned before preparation acknowledged cancellation")
			}
			msg := pending()
			_, done := m.Update(msg)
			if !containsQuit(done) {
				t.Fatal("cancelled preparation acknowledgement did not quit")
			}
			if m.run.active() {
				t.Fatal("quit still owns abandoned preparation")
			}
			if got := strings.Join(ta.recorded(t), ","); strings.Contains(got, "run") {
				t.Fatalf("quit launched work: %s", got)
			}
		})
	}
}

// Only used with completion commands, never animation/polling chains.
func containsQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if containsQuit(c) {
				return true
			}
		}
	}
	return false
}

func TestCancelInFlightDependencyScan(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	ready := filepath.Join(t.TempDir(), "ready")
	scanner := filepath.Join(t.TempDir(), "scanner")
	write(t, scanner, fmt.Sprintf("#!/bin/sh\necho ready > %q\nexec sleep 30\n", ready))
	if err := os.Chmod(scanner, 0755); err != nil {
		t.Fatal(err)
	}
	ta.Scanner.PwshBin = scanner
	cmd := m.run.start(m, m.scripts[0])
	results := make(chan tea.Msg, 1)
	go func() { results <- cmd() }()
	waitFile(t, ready)
	m.run.kill(m)
	if !m.run.active() {
		t.Fatal("cancelled scan released ownership before acknowledgement")
	}
	select {
	case msg := <-results:
		_, next := m.Update(msg)
		drive(t, m, next, 5*time.Second)
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled scan did not stop")
	}
	if m.run.active() {
		t.Error("cancelled scan did not release ownership")
	}
	if len(ta.recorded(t)) != 0 {
		t.Error("cancelled scan launched a script")
	}
}

func TestQuitWaitsForTaskDrainAndSuppressesContinuation(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	called := false
	taskCmd := m.run.startTask(m, "install", func(ctx context.Context, emit func(string)) bool {
		<-ctx.Done()
		emit("cancel acknowledged")
		return true
	}, func(*Model, bool) tea.Cmd { called = true; return nil })
	m.quitCmd()
	yes := press(m, "y")
	if containsQuit(yes) {
		t.Fatal("quit did not await task drain")
	}
	// Ignore the animation command, drain the task's actual channel to closure.
	_ = taskCmd
	cmd := drainTask(m.run.task)
	for cmd != nil {
		msg := cmd().(TaskEventsMsg)
		next := m.run.onTaskEvents(m, msg)
		if msg.Closed {
			if !containsQuit(next) {
				t.Fatal("completed cancellation did not quit")
			}
			break
		}
		cmd = next
	}
	if called || m.run.active() {
		t.Fatal("quitting ran the install continuation or retained the task")
	}
}

func TestCancelledStartedMessageStillFinalizes(t *testing.T) {
	ta := seedToolApp(t, "echo started\nsleep 30\n")
	m := toolModel(t, ta)
	launch := m.run.onDepsScanned(m, m.run.start(m, m.scripts[0])().(DepsScannedMsg))
	started := launch().(RunStartedMsg) // hold delivery while the process is already running
	m.run.kill(m)
	drive(t, m, m.run.onRunStarted(m, started), 10*time.Second)
	if m.run.active() {
		t.Fatal("cancelled late handle was abandoned")
	}
	rows, err := ta.Hist.Last(10)
	if err != nil || len(rows) != 1 || rows[0].Status != "killed" {
		t.Fatalf("cancelled launch did not finalize: rows=%v err=%v", rows, err)
	}
	if len(ta.Locks.ListLive()) != 0 {
		t.Error("cancelled launch left a lock")
	}
}

func TestTUITimeoutFinalizesAndContinuesQueue(t *testing.T) {
	ta := seedToolApp(t, "sleep 30\n")
	m := toolModel(t, ta)
	minutes := 0.0005
	m.scripts[0].TimeoutMinutes = &minutes
	m.run.queue = []queued{{Name: m.scripts[0].Name}}
	drive(t, m, m.run.start(m, m.scripts[0]), 10*time.Second)
	rows, err := ta.Hist.Last(10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("queue finalization rows=%v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.Status != "timeout" {
			t.Errorf("status=%s", row.Status)
		}
	}
	if m.run.active() || len(m.run.queue) != 0 {
		t.Fatal("timeout did not release queue ownership")
	}
}

func TestDecliningQuitRestoresDependencyPrompt(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	write(t, ta.scan, `{"missing":[{"name":"Az","display":"Az"}]}`)
	pump(t, m, m.run.start(m, m.scripts[0]), 5*time.Second, nil)
	prompt := m.ov
	m.onKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	press(m, "n")
	if m.ov != prompt {
		t.Fatalf("declining quit abandoned dependency prompt: %T", m.ov)
	}
	drive(t, m, press(m, "n"), 10*time.Second)
	if m.run.active() {
		t.Fatal("restored dependency prompt did not finish")
	}
}

func TestStaleStartedHandleIsDrained(t *testing.T) {
	ta := seedToolApp(t, "exit 0\n")
	m := toolModel(t, ta)
	old := m.run.reserve("old")
	m.run.releaseAttempt()
	current := m.run.reserve("current")
	ch := make(chan runner.Event, 1)
	ch <- runner.Event{Kind: runner.EvLine, Line: "stale"}
	close(ch)
	killed := false
	h := &runner.Handle{Name: "old", Events: ch, Kill: func(string) { killed = true }}
	m.run.onRunStarted(m, RunStartedMsg{Attempt: old, Handle: h})()
	if !killed || len(ch) != 0 || m.run.attempt != current || m.run.handle != nil {
		t.Fatal("stale handle was abandoned or replaced the current owner")
	}
	m.run.releaseAttempt()
}
