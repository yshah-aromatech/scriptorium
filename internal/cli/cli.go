// Package cli is the headless entry point, a byte-for-byte port of
// scriptorium.ps1's flag loop and every branch's output. Main is the whole
// surface: parse the args exactly like the PS switch loop (unknown flags
// silently ignored), open the app, print its warnings, then dispatch in the
// PS file's own branch order.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// ResolveAppDir finds the app directory the way scriptorium.ps1 does via
// $PSScriptRoot, adapted for a compiled binary that has no script path of
// its own: $SCRIPTORIUM_APP_DIR wins when set; else the executable's own
// directory, when a config.json lives there (an installed binary shipped
// beside its config); else the current working directory (running from a
// dev checkout). Tests drive this via the env var.
func ResolveAppDir() string {
	if d := os.Getenv("SCRIPTORIUM_APP_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
			return dir
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

// mcpStubMessage and tuiStubMessage are honest stand-ins for surfaces later
// phases ship (P9 for the MCP server, the TUI phase for the interactive
// front end) — content-equal with the PS app's own messaging pattern, not
// byte-gated.
const mcpStubMessage = "the MCP server is not yet available in the Go rebuild — use the PowerShell app (arrives in a later phase)"
const tuiStubMessage = "the TUI is not yet available in the Go rebuild — use the PowerShell app (arrives in a later phase)"

// helpLines mirrors scriptorium.ps1's own header block (its lines 2-16,
// '#'-stripped) — content-equal with the PS --help output, not byte-gated.
var helpLines = []string{
	"scriptorium.ps1 — Scriptorium entry point",
	"",
	"  scriptorium                 launch the TUI",
	"  scriptorium --list          list discovered scripts",
	"  scriptorium --run <name>    run one script headless (full pipeline)",
	`  scriptorium --run <name> --args "<extra args>"   pass extra arguments`,
	"  scriptorium --run <name> --cron     same, marks the run as cron-triggered",
	"  scriptorium --sync          sync all scripts repos and exit",
	"  scriptorium --repos         list configured scripts repos",
	"  scriptorium --add-repo <url> [--name <n>] [--branch <b>]   add a scripts repo",
	"  scriptorium --history [name]        print recent runs (optionally one script)",
	"  scriptorium --mcp [--port <n>]      serve the MCP server (for n8n AI agents)",
	"  scriptorium --install-mcp-service   install + start the MCP server as a systemd service",
	"  scriptorium --help",
	"",
}

// flags is the parsed argument set — one field per scriptorium.ps1 loop
// variable.
type flags struct {
	runName         string
	extraArgsRaw    string
	isCron          bool
	listOnly        bool
	syncOnly        bool
	historyOnly     bool
	historyName     string
	mcpOnly         bool
	mcpInstall      bool
	mcpPortOverride int
	addRepoURL      string
	addRepoName     string
	addRepoBranch   string
	listRepos       bool
	showHelp        bool
}

// argAt returns args[i], or "" when i is out of range — matching PS's
// silent-null indexing past the end of $args.
func argAt(args []string, i int) string {
	if i >= 0 && i < len(args) {
		return args[i]
	}
	return ""
}

// parseFlags is a single pass over args exactly like scriptorium.ps1's
// switch loop: an option that takes a value always consumes the next
// argument (even if that slot is empty or missing), and an unrecognized
// flag is silently ignored.
func parseFlags(args []string) flags {
	f := flags{addRepoBranch: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--run":
			f.runName = argAt(args, i+1)
			i++
		case "--args":
			f.extraArgsRaw = argAt(args, i+1)
			i++
		case "--cron":
			f.isCron = true
		case "--list":
			f.listOnly = true
		case "--sync":
			f.syncOnly = true
		case "--mcp":
			f.mcpOnly = true
		case "--port":
			f.mcpPortOverride, _ = strconv.Atoi(argAt(args, i+1))
			i++
		case "--repos":
			f.listRepos = true
		case "--add-repo":
			f.addRepoURL = argAt(args, i+1)
			i++
		case "--name":
			f.addRepoName = argAt(args, i+1)
			i++
		case "--branch":
			f.addRepoBranch = argAt(args, i+1)
			i++
		case "--install-mcp-service":
			f.mcpInstall = true
		case "--history":
			f.historyOnly = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				f.historyName = args[i+1]
				i++
			}
		case "--help", "-h":
			f.showHelp = true
		}
	}
	return f
}

// Main is cli's whole testable surface: main.go is just
// os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr)).
func Main(args []string, stdout, stderr io.Writer) int {
	a, err := app.Open(ResolveAppDir())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	f := parseFlags(args)
	for _, w := range a.Warnings {
		fmt.Fprintln(stderr, "WARNING: "+w)
	}

	switch {
	case f.showHelp:
		for _, l := range helpLines {
			fmt.Fprintln(stdout, l)
		}
		return 0
	case f.addRepoURL != "":
		return runAddRepo(a, f, stdout)
	case f.listRepos:
		return runListRepos(a, stdout)
	case f.mcpInstall, f.mcpOnly:
		fmt.Fprintln(stderr, mcpStubMessage)
		return 1
	case f.listOnly:
		return runList(a, stdout)
	case f.syncOnly:
		return runSync(a, stdout)
	case f.historyOnly:
		return runHistory(a, f.historyName, stdout)
	case f.runName != "":
		return runScript(a, f, stdout, stderr)
	default:
		fmt.Fprintln(stderr, tuiStubMessage)
		return 1
	}
}

func runAddRepo(a *app.App, f flags, stdout io.Writer) int {
	ok, message, _ := scripts.AddRepoConfig(a.Paths.AppDir, f.addRepoURL, f.addRepoName, f.addRepoBranch)
	fmt.Fprintln(stdout, message)
	if !ok {
		return 1
	}
	fmt.Fprintln(stdout, "run 'scriptorium --sync' to clone it")
	return 0
}

func runListRepos(a *app.App, stdout io.Writer) int {
	for _, r := range scripts.Repos(a.Cfg, a.Paths) {
		tag := ""
		if r.Legacy {
			tag = " (legacy scriptsRepo)"
		}
		url := r.URL
		if url == "" {
			url = "<no url configured>"
		}
		fmt.Fprintf(stdout, "%-15s %-8s %s%s\n", r.Name, r.Branch, url, tag)
	}
	return 0
}

// runList is --list: P7 not ready — the crontab reader doesn't exist yet,
// so the schedule column always renders empty (the diff-oracle test runs
// against an empty crontab too, so this is a fair diff for now).
func runList(a *app.App, stdout io.Writer) int {
	rows, _ := a.Hist.Last(2000)
	statuses := history.LastStatuses(rows)
	all := scripts.Discover(scripts.Repos(a.Cfg, a.Paths), a.Paths)
	for _, s := range all {
		st := "never run"
		if last, ok := statuses[s.Name]; ok {
			st = last.Status
		}
		rt := "ps"
		if s.Runtime == "python" {
			rt = "py"
		}
		const sched = "" // P7: no crontab reader yet
		fmt.Fprintf(stdout, "%-30s %-3s %-10s%s\n", s.Name, rt, st, sched)
	}
	return 0
}

func runSync(a *app.App, stdout io.Writer) int {
	ok := scripts.Sync(a.Cfg, a.Paths, a.Sec, func(line string) { fmt.Fprintln(stdout, line) })
	if ok {
		return 0
	}
	return 1
}

// runHistory is --history [name]. Rendering matches Get-StoDuration.
func runHistory(a *app.App, name string, stdout io.Writer) int {
	rows, _ := a.Hist.Last(200)
	if name != "" {
		filtered := make([]history.Row, 0, len(rows))
		for _, r := range rows {
			if r.Script == name {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no runs recorded")
		return 0
	}
	for _, r := range rows {
		// PS falls back to the raw startedAt string when the ISO cast fails;
		// Row has already lost that raw text by the time it reaches here, so
		// an unparseable startedAt renders empty instead. Not exercised by
		// the diff oracle's seed data (every row parses).
		when := ""
		if t := r.StartedAt.Time(); !t.IsZero() {
			when = t.Local().Format("2006-01-02 15:04:05")
		}
		var dur float64
		if r.DurationSec != nil {
			dur = *r.DurationSec
		}
		var cpu, mem any = "", ""
		if r.Resources != nil {
			cpu = psNumber(r.Resources.CPUMaxPercent)
			mem = psNumber(r.Resources.MemMaxMb)
		}
		fmt.Fprintf(stdout, "%s  %-9s %-25s %8s  cpu %5v%%  mem %7vMB  [%s]\n",
			when, r.Status, r.Script, format.Duration(dur), cpu, mem, r.Trigger)
	}
	return 0
}

// runScript is --run <name>: the headless full pipeline.
func runScript(a *app.App, f flags, stdout, stderr io.Writer) int {
	// missed-run sweep piggybacks on every headless boot — best-effort,
	// errors swallowed. nil Schedules (no crontab reader until P7) makes
	// this a no-op today; PS still covers the shared server meanwhile.
	_, _ = missed.Check(missed.Options{
		DataDir:      a.Paths.DataDir,
		Schedules:    nil,
		GraceMinutes: a.Cfg.MissedGraceMinutes,
		Locks:        a.Locks,
		Hist:         a.Hist,
		Hook:         a.Hook,
	})

	all := scripts.Discover(scripts.Repos(a.Cfg, a.Paths), a.Paths)
	var target *scripts.Script
	for i := range all {
		if all[i].Name == f.runName {
			target = &all[i]
			break
		}
	}
	if target == nil {
		fmt.Fprintf(stderr, "script '%s' not found — run 'scriptorium --list' (or sync first)\n", f.runName)
		return 2
	}

	// timeout: script.json's TimeoutMinutes when set and positive, else the
	// config default (caller-resolved per the runner's P5 contract).
	minutes := a.Cfg.RunTimeoutMinutes
	if target.TimeoutMinutes != nil && *target.TimeoutMinutes > 0 {
		minutes = *target.TimeoutMinutes
	}
	timeout := time.Duration(minutes * float64(time.Minute))

	trigger := "manual"
	if f.isCron {
		trigger = "cron"
	}

	row, err := a.Runner.RunToCompletion(context.Background(), runner.Spec{
		Script:    *target,
		Trigger:   trigger,
		ExtraArgs: runner.SplitArguments(f.extraArgsRaw),
		Timeout:   timeout,
	}, func(ev runner.Event) {
		if ev.Kind == runner.EvLine {
			fmt.Fprintln(stdout, ev.Line)
		}
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var duration float64
	var exitCode int
	if row.DurationSec != nil {
		duration = *row.DurationSec
	}
	if row.ExitCode != nil {
		exitCode = *row.ExitCode
	}
	var res history.Resources
	if row.Resources != nil {
		res = *row.Resources
	}
	fmt.Fprintf(stdout, "-- %s: %s (exit %d) in %ss | cpu avg %s%% peak %s%% | mem avg %sMB peak %sMB\n",
		row.Script, row.Status, exitCode, psNumber(duration),
		psNumber(res.CPUAvgPercent), psNumber(res.CPUMaxPercent), psNumber(res.MemAvgMb), psNumber(res.MemMaxMb))

	switch {
	case row.Success != nil && *row.Success:
		return 0
	case row.Status == "skipped":
		return 3
	default:
		return 1
	}
}

// psNumber renders a float the way PowerShell's string interpolation
// renders a plain double: fixed-point, shortest round-trip, no trailing
// ".0" (12.3 -> "12.3", 0 -> "0", 100 -> "100").
func psNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
