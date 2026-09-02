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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/mcp"
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

// tuiStubMessage is an honest stand-in for the surface a later phase ships
// (the TUI front end) — content-equal with the PS app's own messaging
// pattern, not byte-gated.
const tuiStubMessage = "the TUI is not yet available in the Go rebuild — use the PowerShell app (arrives in a later phase)"

// mcpNoTokenMessage is byte-copied from scriptorium.ps1:102 (the $mcpOnly
// branch's Write-Error text) — the exact string --mcp prints to stderr when
// MCP_AUTH_TOKEN is unset, before ever touching a socket.
const mcpNoTokenMessage = "MCP_AUTH_TOKEN is not set — add it to .env next to this script (see .env.example). Refusing to start an unauthenticated server."

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
	case f.mcpInstall:
		return runInstallMcpService(a, stdout, stderr)
	case f.mcpOnly:
		return runMcp(a, f, stdout, stderr)
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

// runList is --list. The schedule column is the managed crontab block's
// expression for the script, in PS's own bracket shape.
func runList(a *app.App, stdout io.Writer) int {
	rows, _ := a.Hist.Last(2000)
	statuses := history.LastStatuses(rows)
	schedules := a.Cron.Schedules()
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
		sched := ""
		if expr, ok := schedules[s.Name]; ok {
			sched = "  [" + expr + "]"
		}
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

// mcpBindAddr resolves config.mcpBind + the resolved port into a
// net.Listen address: 'localhost' binds loopback-only; anything else
// (PS's 'all', and any other value) binds every interface — mirroring
// Start-StoMcpServer's prefix choice (http://localhost:PORT/ vs
// http://+:PORT/) without HttpListener's own namespace-reservation quirks.
func mcpBindAddr(cfgBind string, port int) string {
	host := ""
	if cfgBind == "localhost" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// resolveMcpPort is --port's precedence rule: the CLI override wins only
// when explicitly positive; a zero (or negative) override means "not
// given" (parseFlags' zero value for an absent --port), and config.mcpPort
// applies.
func resolveMcpPort(cfgPort, override int) int {
	if override > 0 {
		return override
	}
	return cfgPort
}

// runMcp is --mcp [--port n]: serve the MCP + REST API in the foreground
// until the listener stops. scriptorium.ps1:102 checks MCP_AUTH_TOKEN and
// errors before ever calling into the server — ported verbatim, byte-exact
// message, before any socket is touched.
func runMcp(a *app.App, f flags, stdout, stderr io.Writer) int {
	token := os.Getenv("MCP_AUTH_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, mcpNoTokenMessage)
		return 1
	}
	srv, err := mcp.New(&mcp.Ops{App: a}, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	port := resolveMcpPort(a.Cfg.McpPort, f.mcpPortOverride)
	l, err := net.Listen("tcp", mcpBindAddr(a.Cfg.McpBind, port))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "MCP server listening on %s (endpoint POST /mcp, health GET /healthz, API under /api/v1)\n", l.Addr())
	if err := srv.Serve(l); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// runInstallMcpService is --install-mcp-service: Linux-only (the guard
// lives here, not in mcp.Installer, so the installer's root/non-root logic
// stays testable on any host) — writes and activates the systemd unit
// whose ExecStart is this very binary (ruling 3, the P7-deferred swap).
func runInstallMcpService(a *app.App, stdout, stderr io.Writer) int {
	if runtime.GOOS != "linux" {
		fmt.Fprintln(stderr, "--install-mcp-service needs systemd (Linux only)")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	in := &mcp.Installer{Out: func(s string) { fmt.Fprintln(stdout, s) }}
	if err := in.Install(a.Paths.AppDir, exe, os.Getenv("MCP_AUTH_TOKEN")); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
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

// installMissingDeps is scriptorium.ps1:157-163's dependency auto-install
// step: scan the target's declared deps, and when any are missing, run the
// generated install command through cfg.pwshBin — streaming its output —
// before letting the caller proceed to the run regardless of the install's
// outcome (PS parity: scriptorium.ps1 never checks the install command's
// exit code). Streaming the child's stdout/stderr to the CLI's own
// stdout/stderr (not merged) is the brief's own wording, not a PS behavior
// to match: the real `& $cfg.pwshBin ... -Command $cmd` has no redirection
// at all, so its streams inherit the console's separately.
//
// A PowerShell scan that degrades (no usable pwsh) prints its warning to
// stderr and skips straight to the run — Missing is always empty on that
// path, so there is nothing to install anyway. A scan error is swallowed
// exactly like PS's own Get-StoMissingDeps-failure-yields-empty-missing
// behavior: the caller sees no missing deps and just proceeds to run.
func installMissingDeps(a *app.App, target scripts.Script, stdout, stderr io.Writer) {
	var missing []deps.Dep
	if target.Runtime == "python" {
		result, err := a.Scanner.ScanPython(target.Dir, target.VenvDir, a.Cfg.PythonBin)
		if err != nil {
			return
		}
		missing = result
	} else {
		result, err := a.Scanner.ScanPS(target.Entry, target.Dir, target.ModuleDir, target.Loose)
		if err != nil {
			return
		}
		if result.Degraded {
			fmt.Fprintln(stderr, "WARNING: "+result.Warning)
			return
		}
		missing = result.Missing
	}
	if len(missing) == 0 {
		return
	}

	displays := make([]string, len(missing))
	for i, d := range missing {
		displays[i] = d.Display
	}
	fmt.Fprintln(stdout, "installing missing modules: "+strings.Join(displays, ", "))

	cmd := deps.InstallCommand(deps.InstallTarget{
		Runtime:   target.Runtime,
		Dir:       target.Dir,
		ModuleDir: target.ModuleDir,
		VenvDir:   target.VenvDir,
	}, missing, a.Cfg.PythonBin)

	c := exec.Command(a.Cfg.PwshBin, "-NoProfile", "-NonInteractive", "-Command", cmd)
	c.Stdout = stdout
	c.Stderr = stderr
	_ = c.Run() // proceed to the run regardless of the install's exit code

	// The (size, mtime) scan cache only observes the entry file — it has no
	// way to notice that moduleDir/venvDir just changed. Drop this entry's
	// cached result so the next ScanPS/ScanPython (if any, on a long-lived
	// Scanner) reflects what was just installed rather than serving the
	// pre-install Missing list.
	a.Scanner.Invalidate(target.Entry)
}

// runScript is --run <name>: the headless full pipeline.
func runScript(a *app.App, f flags, stdout, stderr io.Writer) int {
	// missed-run sweep piggybacks on every headless boot — best-effort,
	// errors swallowed. Real schedules and the configured grace, both
	// resolved here so Check never has to guess a default.
	_, _ = missed.Check(missed.Options{
		DataDir:      a.Paths.DataDir,
		Schedules:    a.Cron.Schedules(),
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

	installMissingDeps(a, *target, stdout, stderr)

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
