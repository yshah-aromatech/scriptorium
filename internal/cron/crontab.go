package cron

// Managed-block crontab access — the port of Get-StoCrontab /
// Get-StoSchedules / Save-StoSchedules / Set-StoSchedule / Remove-StoSchedule
// (src/Cron.psm1 lines 1-101).
//
// Everything the user owns in their crontab lives OUTSIDE the marker pair and
// is preserved verbatim; only the block between the markers belongs to this
// app. The single most dangerous operation in the whole program is writing
// this file, so the invariant that governs every write path is: a `crontab -l`
// that actually FAILED (spool permissions, missing binary) is not an empty
// crontab, and writing back on one would destroy every unmanaged entry.
// Read returns ok=false for that case and no caller writes when it does.

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Block markers. The current pair is what this app writes; the legacy
// (pre-rename) pair is still recognized on read, so an existing block keeps
// showing its schedules and is rewritten under the current markers on the
// next save. The separator is an em-dash (U+2014) — byte-identical with
// Cron.psm1 and testdata/psfixtures/crontab/*.txt.
const (
	BlockStart = "# >>> scriptorium managed block — do not edit by hand >>>"
	BlockEnd   = "# <<< scriptorium managed block <<<"

	// Exported so internal/migrate can tell an adopted block from one still
	// wearing the pre-rename markers.
	LegacyBlockStart = "# >>> psscripts managed block — do not edit by hand >>>"
	LegacyBlockEnd   = "# <<< psscripts managed block <<<"
)

func isBlockStart(line string) bool { return line == BlockStart || line == LegacyBlockStart }
func isBlockEnd(line string) bool   { return line == BlockEnd || line == LegacyBlockEnd }

// Reader regexes, copied verbatim from Get-StoSchedules. They deliberately
// match BOTH spellings of a managed line — the PowerShell app's
// `'<pwsh>' -NoProfile -File scriptorium.ps1 --run '<name>'` and this
// binary's `'<bin>' --run '<name>'` — so the two apps can read each other's
// block during the migration window.
var (
	runNameRe = regexp.MustCompile(`--run '([^']+)'`)
	exprRe    = regexp.MustCompile(`^(@\S+|(?:\S+\s+){4}\S+)\s+cd `)
	// A quote or space in the name would break the shell-quoted cron line AND
	// the reader regex that parses it back — refuse rather than corrupt.
	safeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// CrontabRunner executes the crontab binary. stdin is piped in (empty for a
// read); ok is false when the command could not run at all or exited
// nonzero. Injected so no test ever touches a real crontab.
type CrontabRunner func(stdin string, args ...string) (stdout string, ok bool)

// Crontab is the managed block in one user's crontab. AppDir/LogsDir/BinPath
// shape the command written for each schedule; Run defaults to the real
// binary when nil.
type Crontab struct {
	AppDir  string
	LogsDir string
	BinPath string
	Run     CrontabRunner

	mu sync.Mutex
}

// defaultRunner shells out to the crontab binary, resolved through PATH (so a
// PATH shim can stand in for it). stderr is discarded, matching PS's
// `2>$null`: "no crontab for <user>" must not read as output. A command that
// could not START surfaces its error as stdout, which puts Read in its
// failed-with-output arm — the same wipe-guard side PS's catch block takes.
func defaultRunner(stdin string, args ...string) (string, bool) {
	bin, err := exec.LookPath("crontab")
	if err != nil {
		return err.Error(), false
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return err.Error(), false
		}
		return string(out), false
	}
	return string(out), true
}

func (c *Crontab) runner() CrontabRunner {
	if c.Run != nil {
		return c.Run
	}
	return defaultRunner
}

// splitLines mirrors how PowerShell hands a native command's stdout back as
// an array: one element per line, with the single trailing newline dropped.
func splitLines(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// Read is Get-StoCrontab's truth table:
//
//	exit 0 with output      -> (lines, true)
//	exit 0 with no output   -> (nil, true)    empty crontab
//	failed with no stdout   -> (nil, true)    "no crontab for <user>"
//	failed with stdout      -> (nil, false)   a real read failure
//	could not execute       -> (nil, false)   (runner reports the error as stdout)
//
// ok=false is the wipe guard: it means "unknown contents", and no caller may
// write on it.
func (c *Crontab) Read() ([]string, bool) {
	out, ok := c.runner()("", "-l")
	if ok {
		return splitLines(out), true
	}
	if out == "" {
		return nil, true
	}
	return nil, false
}

// Lines returns the whole crontab (Get-StoCrontabLines): a failed read is
// indistinguishable from an empty one here, exactly as in PS — only the write
// paths care about the difference.
func (c *Crontab) Lines() []string {
	lines, _ := c.Read()
	return lines
}

// Schedules maps script name -> cron expression for every managed line, under
// either marker generation and in either spelling. A line whose expression
// does not parse is skipped.
func (c *Crontab) Schedules() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedules()
}

func (c *Crontab) schedules() map[string]string {
	lines, _ := c.Read()
	return schedulesFromLines(lines)
}

// SchedulesFromLines is schedulesFromLines, exported for --migrate's preview
// (internal/migrate.Preview): it derives the planned block from the SAME
// lines snapshot the wipe-guard read already produced, rather than reading
// the crontab a second time.
func SchedulesFromLines(lines []string) map[string]string { return schedulesFromLines(lines) }

// BlockLines returns the managed block's own lines (both markers inclusive,
// either marker generation), or nil if lines has no block at all. Read-only,
// no I/O — --migrate's preview uses this to show "the current managed
// block" from an already-read snapshot.
func BlockLines(lines []string) []string {
	var block []string
	inBlock := false
	for _, line := range lines {
		switch {
		case isBlockStart(line):
			inBlock = true
			block = append(block, line)
		case isBlockEnd(line):
			block = append(block, line)
			inBlock = false
		case inBlock:
			block = append(block, line)
		}
	}
	return block
}

// schedulesFromLines is Schedules'/schedules' parser, extracted so Set/Remove
// can derive the current map from a snapshot they already hold — reading the
// crontab a second time to do it is exactly the single-read fix below
// guards against.
func schedulesFromLines(lines []string) map[string]string {
	m := map[string]string{}
	inBlock := false
	for _, line := range lines {
		switch {
		case isBlockStart(line):
			inBlock = true
		case isBlockEnd(line):
			inBlock = false
		case inBlock:
			nm := runNameRe.FindStringSubmatch(line)
			if nm == nil {
				continue
			}
			em := exprRe.FindStringSubmatch(line)
			if em == nil {
				continue
			}
			if expr := strings.TrimSpace(em[1]); expr != "" {
				m[nm[1]] = expr
			}
		}
	}
	return m
}

// Save rewrites the managed block. Every line outside the block (either
// marker generation) is kept in its original order and written FIRST; the
// block is appended at the END with one line per schedule, names sorted.
// An empty map removes the block entirely.
func (c *Crontab) Save(schedules map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.save(schedules)
}

func (c *Crontab) save(schedules map[string]string) error {
	lines, ok := c.Read()
	if !ok {
		return errors.New("crontab read failed — refusing to write (unmanaged entries would be destroyed)")
	}
	return c.saveFromLines(lines, schedules)
}

// RenderBlock previews the block Save(schedules) would write — both
// markers plus one line per schedule, or nil for an empty map — without
// touching the crontab at all. --migrate's preview uses this (via
// internal/migrate.Preview) to show "the block it would write" from a
// schedule map it already derived from one read, so the rendering can never
// drift from what an actual Save/Set/Remove call produces: both go through
// this exact function.
func (c *Crontab) RenderBlock(schedules map[string]string) []string {
	return c.renderedBlock(schedules)
}

// renderedBlock is RenderBlock's implementation, shared with saveFromLines.
func (c *Crontab) renderedBlock(schedules map[string]string) []string {
	if len(schedules) == 0 {
		return nil
	}
	names := make([]string, 0, len(schedules))
	for name := range schedules {
		names = append(names, name)
	}
	// Ordinal sort. PS's Sort-Object is culture-aware; for the
	// [A-Za-z0-9._-] names Set-StoSchedule permits, the two orders differ
	// only in how case and punctuation weight against each other, which
	// changes line order inside the block and nothing else.
	sort.Strings(names)

	block := []string{BlockStart}
	for _, name := range names {
		log := filepath.Join(c.LogsDir, "cron-"+name+".log")
		cmd := fmt.Sprintf("cd '%s' && '%s' --run '%s' --cron >> '%s' 2>&1", c.AppDir, c.BinPath, name, log)
		// % is crontab's command terminator, so it is escaped in the
		// COMMAND portion only (Cron.psm1:74) — an expression keeps its
		// bytes, whatever they are.
		block = append(block, schedules[name]+" "+strings.ReplaceAll(cmd, "%", `\%`))
	}
	return append(block, BlockEnd)
}

// saveFromLines writes schedules against an ALREADY-READ lines snapshot —
// no read happens here. Set/Remove call this directly with the one snapshot
// they read at the top, so the whole read-mutate-write sequence touches the
// crontab exactly once (the single-read fix: see Set/Remove below for the
// wipe scenario a second, independent read used to expose).
func (c *Crontab) saveFromLines(lines []string, schedules map[string]string) error {
	var kept []string
	inBlock := false
	for _, line := range lines {
		switch {
		case isBlockStart(line):
			inBlock = true
		case isBlockEnd(line):
			inBlock = false
		case !inBlock:
			kept = append(kept, line)
		}
	}

	out := append(kept, c.renderedBlock(schedules)...)

	text := strings.Join(out, "\n")
	if text != "" {
		text += "\n"
	}
	if _, ok := c.runner()(text, "-"); !ok {
		return errors.New("crontab write failed")
	}
	return nil
}

// Set adds or replaces one schedule. An unsafe name is refused before
// anything is read or written.
//
// The crontab is read EXACTLY ONCE — into lines, below — and that same
// snapshot is both parsed for the current schedules and passed to
// saveFromLines. Reading twice (once to build the map, again inside a
// naive save) is the wipe hazard this guards against: if the FIRST read
// failed and was silently treated as empty while a SECOND, later read
// succeeded with sibling schedules present, the save would write back a
// block containing only this one schedule — destroying every sibling that
// existed on disk but was never folded into the map. A single failed read
// must abort the whole call with nothing written, full stop.
func (c *Crontab) Set(name, expr string) error {
	if !safeNameRe.MatchString(name) {
		return fmt.Errorf("refusing to schedule %q: name must match %s", name, safeNameRe)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lines, ok := c.Read()
	if !ok {
		return errors.New("crontab read failed — refusing to write (unmanaged entries would be destroyed)")
	}
	s := schedulesFromLines(lines)
	s[name] = expr
	return c.saveFromLines(lines, s)
}

// Remove drops one schedule. An absent name still rewrites the block, which
// is what Remove-StoSchedule does. Single-read, same rationale as Set.
func (c *Crontab) Remove(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	lines, ok := c.Read()
	if !ok {
		return errors.New("crontab read failed — refusing to write (unmanaged entries would be destroyed)")
	}
	s := schedulesFromLines(lines)
	delete(s, name)
	return c.saveFromLines(lines, s)
}
