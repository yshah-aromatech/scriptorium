// Package runner executes one script under supervision: it launches the
// process, streams its output through the redaction chokepoint into a per-run
// log file, samples the process tree's resources, enforces the timeout, kills
// the whole tree on demand, and finalizes the run into a history row and a
// webhook report. Port of Start-StoRun / Update-StoRun / Measure-StoResources
// / Stop-StoRun / Complete-StoRun / Invoke-StoRunToCompletion
// (src/Runner.psm1); the shape is architecture §2-§3.
//
// This is the only package under internal/ that starts goroutines. Start
// spawns four: two pipe readers, a sampler, and the supervisor that owns the
// log writer, the accumulators and the event channel.
//
// # Consumer contract
//
// A caller that takes a Handle MUST drain Handle.Events until it is closed
// (EvDone is the last event). The channel is buffered and back-pressured on
// purpose — dropping output when a consumer falls behind would silently
// truncate a chatty script's log. A consumer that walks away without draining
// pins the supervisor goroutine, and with it the run's lock and history row.
// The escape hatch is the context: once ctx is cancelled, event sends stop
// blocking (they drop instead), so the supervisor always reaches finalization.
// Pass a cancellable context whenever the consumer might abandon the run.
package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/procstat"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

const (
	// eventBuffer and lineBuffer are deep enough that a consumer ticking at
	// UI rates never back-pressures a normal script, and shallow enough that
	// an abandoned one is noticed instead of buffering without limit.
	eventBuffer  = 256
	lineBuffer   = 256
	sampleBuffer = 64
	// seriesPoints is how many points a per-run resource series keeps: small
	// enough for history, enough for a sparkline.
	seriesPoints = 60
)

// EventKind tags an Event's payload.
type EventKind int

const (
	// EvLine carries one redacted output line.
	EvLine EventKind = iota
	// EvSample carries one resource reading.
	EvSample
	// EvDone carries the finished run's history row and is always the last
	// event on the channel.
	EvDone
)

// Event is the single tagged union every frontend consumes.
type Event struct {
	Kind   EventKind
	Line   string
	Sample procstat.Sample
	Result *history.Row
}

// Spec is one run request. Timeout 0 means no timeout; the CALLER resolves
// the precedence (per-call override > script.json > config.runTimeoutMinutes).
type Spec struct {
	Script    scripts.Script
	Trigger   string // "manual" | "cron" | "mcp"
	ExtraArgs []string
	ExtraEnv  map[string]string
	Timeout   time.Duration
}

// Handle is a started run. Kill's reason is "killed" or "timeout"; it blocks
// for up to the kill grace period (3s) and is a no-op once the run finished
// or was already killed.
type Handle struct {
	Name      string
	StartedAt time.Time
	Events    <-chan Event
	Kill      func(reason string)
}

// Runner holds everything a run touches. All fields are required except Now,
// which defaults to time.Now.
type Runner struct {
	Cfg   *config.Config
	Paths config.Paths
	Locks *lockfile.Dir
	Hist  *history.Store
	Hook  *webhook.Client
	Sec   *secret.Registry
	Now   func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// payload is the webhook wire body: the history row plus the log tail. The
// embedded Row inlines its fields, so "log" lands last, exactly where the PS
// app's ordered payload puts it.
type payload struct {
	history.Row
	Log string `json:"log"`
}

// Start launches a run and returns immediately with its event channel. It
// does not fail: a held lock and a failed exec are both RUNS — fully
// classified, appended to history and reported — not errors. The error
// result exists for the callers' sake and is always nil today.
func (r *Runner) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &supervisor{
		r:         r,
		spec:      spec,
		ctx:       ctx,
		name:      spec.Script.Name,
		trigger:   spec.Trigger,
		runtime:   "powershell",
		startedAt: r.now().UTC(),
		events:    make(chan Event, eventBuffer),
		procDone:  make(chan struct{}),
	}
	if spec.Script.Runtime == "python" {
		s.runtime = "python"
	}
	if s.trigger == "" {
		s.trigger = "manual" // Start-StoRun's parameter default
	}
	h := &Handle{
		Name:      s.name,
		StartedAt: s.startedAt,
		Events:    s.events,
		Kill: func(reason string) {
			if s.setPreset(reason) {
				s.killTree()
			}
		},
	}

	release, holder, acquired := r.Locks.Acquire(s.name)
	if !acquired {
		who := ""
		if holder != 0 {
			who = fmt.Sprintf(" (pid %d)", holder)
		}
		s.setPreset("skipped")
		go s.finishEarly(fmt.Sprintf("skipped: %s is already running%s", s.name, who))
		return h, nil
	}
	s.release = release

	// the log path is computed before the launch and kept even when it
	// fails: PS puts it in the handle before Process.Start, so a failed
	// start still reports a (never-created) logFile
	s.logFile = filepath.Join(r.Paths.LogsDir, safeName(s.name)+"-"+stamp(s.startedAt)+".log")

	cmd := r.buildCmd(spec)
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		var stderr io.ReadCloser
		stderr, err = cmd.StderrPipe()
		if err == nil {
			if err = cmd.Start(); err == nil {
				s.cmd = cmd
				go s.supervise(stdout, stderr)
				return h, nil
			}
		}
	}
	s.setPreset("failure")
	go s.finishEarly("failed to start: " + err.Error())
	return h, nil
}

// RunToCompletion drives a run to its end, handing every event to onEvent
// (which may be nil), and returns the finished row. Ranging to the channel's
// close is what makes it a safe consumer: it can never abandon the run.
//
// The row is non-nil on every ordinary path. It is missing only when the
// EvDone event itself was dropped, which needs a cancelled context AND a full
// event buffer at the same instant (see emit) — the run still finalized,
// history and webhook included, but this caller cannot report which row it
// produced, so that case is an error rather than a nil row.
func (r *Runner) RunToCompletion(ctx context.Context, spec Spec, onEvent func(Event)) (*history.Row, error) {
	h, err := r.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	var row *history.Row
	for ev := range h.Events {
		if onEvent != nil {
			onEvent(ev)
		}
		if ev.Kind == EvDone {
			row = ev.Result
		}
	}
	if row == nil {
		return nil, fmt.Errorf("run abandoned before completion (context cancelled with full event buffer)")
	}
	return row, nil
}

// ---------------------------------------------------------------------------

// supervisor owns one run. Everything below the mutex is touched by the
// supervisor goroutine alone; the mutex box is the small shared surface
// between it, the sampler (which publishes pid snapshots) and whoever calls
// Handle.Kill (which presets the status).
type supervisor struct {
	r    *Runner
	spec Spec
	ctx  context.Context

	name      string
	trigger   string
	runtime   string
	logFile   string
	startedAt time.Time

	events   chan Event
	samples  chan procstat.Sample
	procDone chan struct{}
	release  func()
	cmd      *exec.Cmd
	log      *os.File

	mu       sync.Mutex
	preset   string // "" until a kill or an early classification
	pids     []int  // the sampler's latest tree snapshot
	finished bool

	// accumulators — supervisor goroutine only
	sampleCount          int
	cpuSum, cpuMax       float64
	memSum, memMax       float64
	cpuSeries, memSeries []float64
}

// setPreset records a status that wins over the exit code. The first one
// wins, and a finished run accepts none: a late Handle.Kill must not signal
// a process group whose pid may already have been reused.
func (s *supervisor) setPreset(status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.preset != "" {
		return false
	}
	s.preset = status
	return true
}

func (s *supervisor) setPids(pids []int) {
	s.mu.Lock()
	s.pids = pids
	s.mu.Unlock()
}

func (s *supervisor) getPids() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pids
}

// killTree signals the run's process group and escalates to the sampler's
// snapshot. Callers set the status first (setPreset).
func (s *supervisor) killTree() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	killTree(s.cmd.Process.Pid, s.getPids(), s.procDone)
}

// emit delivers one event. A buffered slot is taken immediately; only a full
// buffer blocks, and only while the context is live — a cancelled context
// turns the send into a drop so an abandoned consumer cannot pin the
// supervisor (and with it the run's lock) forever.
func (s *supervisor) emit(ev Event) {
	select {
	case s.events <- ev:
		return
	default:
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

// finishEarly is the whole lifecycle of a run that never had a process: a
// skipped run, or one whose exec failed. One line, then full finalization.
func (s *supervisor) finishEarly(line string) {
	s.emitLine(line, false)
	s.finalize(-1)
}

// supervise is the run's event loop, in two phases — because PS's completion
// test is a CONJUNCTION (Test-StoRunFinished: `HasExited -and $null -eq
// $OutTask -and $null -eq $ErrTask`) and either half can come first.
//
// Phase one ends when both pipes have hit EOF, which is also the guarantee
// that no tail line was lost: the last reader to finish closes the shared
// lines channel, so the loop cannot exit before every line has been handed
// over. Phase two then waits for the process itself to be reaped — a child
// that closes fds 1 and 2 and keeps running holds no pipe open at all, so the
// timeout and the context have to stay enforceable right up to the reap.
func (s *supervisor) supervise(stdout, stderr io.ReadCloser) {
	// non-fatal: a full disk or an unwritable logs dir must not orphan the
	// already-started process (and its lock) — run without a log instead
	if f, err := os.OpenFile(s.logFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644); err == nil {
		s.log = f
	}

	lines := make(chan string, lineBuffer)
	var openPipes atomic.Int32
	openPipes.Store(2)
	read := func(rc io.ReadCloser) {
		br := bufio.NewReader(rc)
		for {
			// NOT bufio.Scanner: its ErrTooLong on a long line would
			// silently truncate the rest of the stream
			text, err := br.ReadString('\n')
			if text != "" {
				lines <- strings.TrimRight(text, "\r\n")
			}
			if err != nil {
				break
			}
		}
		if openPipes.Add(-1) == 0 {
			close(lines)
		}
	}
	go read(stdout)
	go read(stderr)

	s.samples = make(chan procstat.Sample, sampleBuffer)
	stopSampling := make(chan struct{})
	if procstat.HasProc() {
		go s.sample(stopSampling)
	}

	var timeoutC <-chan time.Time
	if s.spec.Timeout > 0 {
		t := time.NewTimer(s.spec.Timeout)
		defer t.Stop()
		timeoutC = t.C
	}
	ctxC := s.ctx.Done()

	// phase one: the pipes
	for lines != nil {
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			s.emitLine(line, true)
		case sm := <-s.samples:
			s.onSample(sm)
		case <-timeoutC:
			timeoutC = nil
			s.onTimeout()
		case <-ctxC:
			ctxC = nil
			s.onCancel()
		}
	}

	// phase two: the process. Wait closes the pipes, so it may only start now
	// that both readers are done with them.
	waitDone := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(waitDone)
	}()
	reaped := func() bool {
		select {
		case <-waitDone:
			return true
		default:
			return false
		}
	}
	// completion has priority at the boundary, both as the loop condition and
	// again inside the deadline branches: PS guards its whole timeout branch
	// with `-not $proc.HasExited`, so a process that exited while the timer
	// was going off is a finished run, not a timed-out one.
	for !reaped() {
		select {
		case <-waitDone:
		case sm := <-s.samples:
			s.onSample(sm)
		case <-timeoutC:
			timeoutC = nil
			if !reaped() {
				s.onTimeout()
			}
		case <-ctxC:
			ctxC = nil
			if !reaped() {
				s.onCancel()
			}
		}
	}

	close(stopSampling)
	close(s.procDone) // releases any killer still inside its grace period
	s.finalize(exitCodeOf(s.cmd))
}

// onTimeout and onCancel are the two deadline branches, shared by both
// phases. The preset is what makes them idempotent: the first one wins, and a
// finished run accepts neither.
func (s *supervisor) onTimeout() {
	if s.setPreset("timeout") {
		s.emitLine(fmt.Sprintf("run exceeded %smin timeout — killing",
			formatMinutes(s.spec.Timeout)), false)
		go s.killTree()
	}
}

func (s *supervisor) onCancel() {
	if s.setPreset("killed") {
		go s.killTree()
	}
}

// sample ticks the process tree. It publishes every snapshot it walks — even
// the baseline tick that yields no sample — because that snapshot is what the
// killer escalates to.
func (s *supervisor) sample(stop <-chan struct{}) {
	interval := time.Duration(s.r.Cfg.MonitorIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	sm := procstat.NewSampler(s.cmd.Process.Pid)
	for {
		select {
		case <-stop:
			return
		case now := <-tk.C:
			sample, pids, ok := sm.Tick(now)
			if len(pids) > 0 {
				s.setPids(pids)
			}
			if !ok {
				continue
			}
			select {
			case s.samples <- sample:
			case <-stop:
				return
			}
		}
	}
}

// emitLine is the redaction chokepoint: exactly one place sees raw text, and
// everything downstream (the log file, the event stream, and the webhook tail
// read back out of that log) is already scrubbed. That includes the runner's
// OWN notices — a skip message carries a script name, a start failure carries
// the command line — which is why they route through here too.
//
// toLog is false for exactly those notices: PS adds them to Update-StoRun's
// returned lines but never hands them to the LogWriter, so the log file (and
// the webhook tail read back out of it) holds child output alone.
func (s *supervisor) emitLine(raw string, toLog bool) {
	line := s.r.Sec.Redact(raw)
	if toLog && s.log != nil {
		_, _ = s.log.WriteString(line + "\n")
	}
	s.emit(Event{Kind: EvLine, Line: line})
}

func (s *supervisor) onSample(sm procstat.Sample) {
	s.sampleCount++
	s.cpuSum += sm.CPU
	s.memSum += sm.MemMB
	if sm.CPU > s.cpuMax {
		s.cpuMax = sm.CPU
	}
	if sm.MemMB > s.memMax {
		s.memMax = sm.MemMB
	}
	s.cpuSeries = append(s.cpuSeries, sm.CPU)
	s.memSeries = append(s.memSeries, sm.MemMB)
	s.emit(Event{Kind: EvSample, Sample: sm})
}

// finalize classifies the run, writes it to history, releases the lock, and
// reports it. Order is load-bearing throughout — see the comments.
func (s *supervisor) finalize(exitCode int) {
	s.mu.Lock()
	s.finished = true
	status := s.preset
	s.mu.Unlock()

	finishedAt := s.r.now().UTC()
	if status == "skipped" {
		// a skipped run occupies no time at all
		finishedAt = s.startedAt
	}
	if status == "" {
		status = "failure"
		if exitCode == 0 {
			status = "success"
		}
	}
	// the log writer closes BEFORE the tail is read back out of the file
	if s.log != nil {
		_ = s.log.Close()
		s.log = nil
	}

	n := float64(s.sampleCount)
	if n < 1 {
		n = 1
	}
	res := history.Resources{
		CPUAvgPercent: procstat.Round1(s.cpuSum / n),
		CPUMaxPercent: procstat.Round1(s.cpuMax),
		MemAvgMb:      procstat.Round1(s.memSum / n),
		MemMaxMb:      procstat.Round1(s.memMax),
		Samples:       s.sampleCount,
	}
	if s.sampleCount > 0 {
		res.CPUSeries = procstat.Downsample(s.cpuSeries, seriesPoints)
		res.MemSeries = procstat.Downsample(s.memSeries, seriesPoints)
	}

	success := status == "success"
	duration := procstat.Round1(finishedAt.Sub(s.startedAt).Seconds())
	var logFile *string
	if s.logFile != "" {
		logFile = &s.logFile
	}
	row := history.Row{
		Event:       "script_run",
		RunID:       newRunID(),
		Script:      s.name,
		Runtime:     s.runtime,
		Repo:        s.spec.Script.Repo,
		Trigger:     s.trigger,
		Status:      status,
		Success:     &success,
		ExitCode:    &exitCode,
		StartedAt:   history.Stamp(s.startedAt),
		FinishedAt:  history.Stamp(finishedAt),
		DurationSec: &duration,
		Host:        webhook.Host(),
		Resources:   &res,
		LogFile:     logFile,
	}

	// history first: unlocking earlier would let a queued re-run of a
	// sub-second script append its row ahead of this one and lose
	// last-status-wins
	_ = s.r.Hist.Append(row)
	if s.release != nil {
		s.release()
		s.release = nil
	}
	s.r.Hook.Send(payload{Row: row, Log: history.LogTail(s.logFile, s.r.Cfg.LogTailKb)}, "script_run")

	s.emit(Event{Kind: EvDone, Result: &row})
	close(s.events)
}

// ---------------------------------------------------------------------------

// unsafeNameChars is everything a run name may not contribute to a log file
// name (the name also lands in shell-quoted cron lines and MCP log ids).
var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func safeName(name string) string { return unsafeNameChars.ReplaceAllString(name, "_") }

// stamp renders the log-file timestamp: PS's 'yyyy-MM-ddTHH-mm-ss-fffZ'.
// Go's layout language only recognizes fractional seconds behind a '.', so
// the millisecond separator is swapped afterwards.
func stamp(t time.Time) string {
	return strings.Replace(t.UTC().Format("2006-01-02T15-04-05.000Z"), ".", "-", 1)
}

// formatMinutes renders the timeout the way PS interpolates a [double].
// PowerShell formats double/float with .NET's "G15" — 15 significant digits,
// trailing zeros trimmed — NOT the shortest round-trip form .NET Core made
// the default in 3.0 (PS restored G15 to keep its pre-Core output). So 6s
// reads "0.1", 30min reads "30", and 2s reads "0.0333333333333333" rather
// than the round-trip "0.03333333333333333". Verified against live pwsh.
func formatMinutes(d time.Duration) string {
	return strconv.FormatFloat(d.Minutes(), 'g', 15, 64)
}

// exitCodeOf reports the child's exit status. A signalled process reports
// 128+signal, which is what .NET's Process.ExitCode gives the PS app on Unix
// — history rows from the two implementations sit in the same file.
func exitCodeOf(cmd *exec.Cmd) int {
	st := cmd.ProcessState
	if st == nil {
		return -1
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}

// newRunID is a RFC 4122 v4 UUID — the row's stable identity and the
// webhook's dedupe key. Sixteen random bytes with the version and variant
// bits set is the whole spec; a dependency would be silly.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
