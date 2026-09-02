package mcp

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/cron"
	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/missed"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
)

// Ops is the shared tool-implementation layer both the JSON-RPC dispatcher
// (tools.go/rpc.go) and the REST API (api.go) sit on top of — one method per
// tool, so neither frontend ever forks tool logic (ruling 1). Every method
// returns (result, isErr, err): err is reserved for a dispatch-level
// exception (-32603 / REST 500, message redacted); a tool-reported failure
// (unknown script, bad cron, a run that itself failed) rides isErr with a
// result body instead.
type Ops struct {
	App *app.App
}

// ToolError is a validation-style tool failure with no structured body of
// its own (an unknown script, a missing argument, a bad cron expression, a
// malformed or missing log_id) — MCP renders Message as the tool's bare text
// content; the REST API additionally reads NotFound to choose 404 (the
// referenced resource doesn't exist) vs 400 (a bad request) — the smallest
// sentinel both layers map (ruling 1).
type ToolError struct {
	Message  string
	NotFound bool
}

func (e *ToolError) Error() string { return e.Message }

func newToolError(msg string) *ToolError     { return &ToolError{Message: msg} }
func newNotFoundError(msg string) *ToolError { return &ToolError{Message: msg, NotFound: true} }

// stringify mirrors PowerShell's "$($x)" string interpolation for the JSON
// scalar shapes a tool argument can actually hold.
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if !math.IsInf(x, 0) && x == math.Trunc(x) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// toFloat extracts a JSON-decoded numeric argument (float64 from the
// standard decoder, or a numeric string), else 0.
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

// formatNum renders a float the way PowerShell's string interpolation does:
// fixed-point, no trailing ".0" for whole numbers.
func formatNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// localWireTime mirrors missed.wireTime: cron.Next's naive-local result
// (see resolveScript's callers below) has its fields reinterpreted as the
// real local zone and converted to the true UTC instant — what
// Get-StoCronNext's ".ToUniversalTime()" callers do.
func localWireTime(naive time.Time) time.Time {
	return time.Date(naive.Year(), naive.Month(), naive.Day(), naive.Hour(), naive.Minute(), naive.Second(), 0, time.Local).UTC()
}

// nextRunISO computes one cron expression's next fire time as an ISO UTC
// string, or nil when the expression doesn't parse (e.g. @reboot).
func nextRunISO(expr string) any {
	n, ok := cron.Next(expr, missed.NaiveNow(time.Now()))
	if !ok {
		return nil
	}
	return localWireTime(n).Format("2006-01-02T15:04:05Z")
}

// resolveScript is the shared script-by-name lookup (Resolve-StoMcpScript):
// a missing argument or an unrecognized name yields a *ToolError describing
// which, with valid script names listed for the latter.
func (o *Ops) resolveScript(args map[string]any) (scripts.Script, *ToolError) {
	name := stringify(args["script"])
	if name == "" {
		return scripts.Script{}, newToolError("missing required argument 'script'")
	}
	all := scripts.Discover(scripts.Repos(o.App.Cfg, o.App.Paths), o.App.Paths)
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
	}
	names := make([]string, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return scripts.Script{}, newNotFoundError(fmt.Sprintf("unknown script '%s' — valid scripts: %s", name, strings.Join(names, ", ")))
}

// scanMissing dispatches to the right scanner for a script's runtime. A
// degraded PowerShell scan (no usable pwsh) reports no missing deps — there
// is no way to install anything without a real pwsh anyway (ruling 2's
// Go-only degrade). A scan error is swallowed the same way --run's own
// installMissingDeps swallows one: a transient scan hiccup should not sink
// the whole tool call.
func (o *Ops) scanMissing(s scripts.Script) []deps.Dep {
	if s.Runtime == "python" {
		missing, err := o.App.Scanner.ScanPython(s.Dir, s.VenvDir, o.App.Cfg.PythonBin)
		if err != nil {
			return nil
		}
		return missing
	}
	res, err := o.App.Scanner.ScanPS(s.Entry, s.Dir, s.ModuleDir, s.Loose)
	if err != nil || res.Degraded {
		return nil
	}
	return res.Missing
}

// runInstall runs the generated install command for missing deps and
// invalidates the scanner cache afterward — a caller that installs through
// a shared, long-lived Scanner MUST do this (ruling 2), which is exactly
// what the MCP server (and, through it, install_deps/run_script) is. Output
// is redacted; ok reports whether the command exited zero.
func (o *Ops) runInstall(s scripts.Script, missing []deps.Dep) (output string, ok bool) {
	cmd := deps.InstallCommand(deps.InstallTarget{
		Runtime: s.Runtime, Dir: s.Dir, ModuleDir: s.ModuleDir, VenvDir: s.VenvDir,
	}, missing, o.App.Cfg.PythonBin)
	out, err := exec.Command(o.App.Cfg.PwshBin, "-NoProfile", "-NonInteractive", "-Command", cmd).CombinedOutput()
	o.App.Scanner.Invalidate(s.Entry)
	return o.App.Sec.Redact(string(out)), err == nil
}

// splitNonEmptyLines splits combined process output into non-blank lines.
func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---------------------------------------------------------------------
// 1. list_scripts
// ---------------------------------------------------------------------

func (o *Ops) ListScripts() (any, bool, error) {
	rows, _ := o.App.Hist.Last(2000)
	statuses := history.LastStatuses(rows)
	schedules := o.App.Cron.Schedules()
	running := map[string]bool{}
	for _, l := range o.App.Locks.ListLive() {
		running[l.Name] = true
	}
	all := scripts.Discover(scripts.Repos(o.App.Cfg, o.App.Paths), o.App.Paths)
	items := make([]map[string]any, 0, len(all))
	for _, s := range all {
		item := map[string]any{
			"name":            s.Name,
			"runtime":         s.Runtime,
			"repo":            s.Repo,
			"description":     s.Description,
			"entry":           filepath.Base(s.Entry),
			"running":         running[s.Name],
			"lastStatus":      "never run",
			"lastRunAt":       nil,
			"lastDurationSec": nil,
			"schedule":        nil,
			"timeoutMinutes":  s.TimeoutMinutes,
		}
		if st, ok := statuses[s.Name]; ok {
			item["lastStatus"] = st.Status
			if !st.At.IsZero() {
				item["lastRunAt"] = st.At.UTC().Format("2006-01-02T15:04:05Z")
			}
			item["lastDurationSec"] = st.DurationSec
		}
		if expr, ok := schedules[s.Name]; ok {
			item["schedule"] = expr
		}
		items = append(items, item)
	}
	return map[string]any{"scripts": items}, false, nil
}

// ---------------------------------------------------------------------
// 2. get_script_details
// ---------------------------------------------------------------------

func (o *Ops) GetScriptDetails(args map[string]any) (any, bool, error) {
	s, terr := o.resolveScript(args)
	if terr != nil {
		return terr, true, nil
	}
	detail := scripts.GetDetail(s, o.App.Sec)
	out := map[string]any{
		"name":           detail.Name,
		"description":    detail.Description,
		"runtime":        detail.Runtime,
		"repo":           detail.Repo,
		"entry":          detail.Entry,
		"timeoutMinutes": detail.TimeoutMinutes,
		"defaultArgs":    detail.DefaultArgs,
		"readme":         detail.Readme,
		"envExample":     detail.EnvExample,
		"envConfigured":  detail.EnvConfigured,
	}
	if s.Runtime == "python" {
		out["parameters"] = []deps.Param{}
		out["parameterSource"] = "none — see readme"
		out["argsHint"] = "Python: pass args as e.g. --flag value; see readme for supported options"
		return out, false, nil
	}
	// composed from P8's Scanner output — never a second AST scan (ruling 2)
	res, scanErr := o.App.Scanner.ScanPS(s.Entry, s.Dir, s.ModuleDir, s.Loose)
	params := res.Params
	if scanErr != nil || params == nil {
		params = []deps.Param{}
	}
	out["parameters"] = params
	out["parameterSource"] = "param() block (PowerShell AST)"
	out["argsHint"] = "PowerShell: -ParamName value, switches as bare -SwitchName; quote values with spaces"
	// §11.9 tool 2: help/parseWarnings are conditional-on-data, not optional
	// to implement (Scripts.psm1:374-377) — present only when the scan
	// actually found comment-based help or a parse error.
	if scanErr == nil {
		if res.Synopsis != "" || res.Help != "" {
			out["help"] = map[string]any{"synopsis": res.Synopsis, "description": res.Help}
		}
		if res.ParseWarnings > 0 {
			out["parseWarnings"] = res.ParseWarnings
		}
	}
	return out, false, nil
}

// ---------------------------------------------------------------------
// 3. run_script
// ---------------------------------------------------------------------

func (o *Ops) RunScript(args map[string]any) (any, bool, error) {
	s, terr := o.resolveScript(args)
	if terr != nil {
		return terr, true, nil
	}

	extraArgs := runner.SplitArguments(stringify(args["args"]))
	extraEnv := map[string]string{}
	if envArg, ok := args["env"].(map[string]any); ok {
		for k, v := range envArg {
			sv := stringify(v)
			extraEnv[k] = sv
			// these are per-run overrides the caller chose to keep out of
			// .env — secrets by definition, forced past the name gate
			o.App.Sec.Add(k, sv, true)
		}
	}
	var timeoutOverride float64
	if v, ok := args["timeout_minutes"]; ok {
		timeoutOverride = toFloat(v)
	}

	// same auto-install-without-prompt behavior as `scriptorium --run`
	var installed []string
	var installFailed bool
	missing := o.scanMissing(s)
	if len(missing) > 0 {
		_, ok := o.runInstall(s, missing)
		installFailed = !ok
		for _, d := range missing {
			installed = append(installed, d.Display)
		}
	}

	// timeout order: tool timeout_minutes > script.json > config
	minutes := o.App.Cfg.RunTimeoutMinutes
	if s.TimeoutMinutes != nil && *s.TimeoutMinutes > 0 {
		minutes = *s.TimeoutMinutes
	}
	if timeoutOverride > 0 {
		minutes = timeoutOverride
	}
	timeout := time.Duration(minutes * float64(time.Minute))

	var lines []string
	row, err := o.App.Runner.RunToCompletion(context.Background(), runner.Spec{
		Script: s, Trigger: "mcp", ExtraArgs: extraArgs, ExtraEnv: extraEnv, Timeout: timeout,
	}, func(ev runner.Event) {
		if ev.Kind == runner.EvLine {
			lines = append(lines, ev.Line)
		}
	})
	if err != nil {
		return nil, false, err
	}

	// prefer the log tail (bounded, already redacted); skipped runs have no log
	output := strings.Join(lines, "\n")
	if row.LogFile != nil && *row.LogFile != "" {
		output = history.LogTail(*row.LogFile, o.App.Cfg.LogTailKb)
	}

	out := map[string]any{
		"script":      row.Script,
		"status":      row.Status,
		"exitCode":    row.ExitCode,
		"durationSec": row.DurationSec,
		"startedAt":   row.StartedAt,
		"finishedAt":  row.FinishedAt,
		"logFile":     row.LogFile,
		"output":      output,
		"resources": map[string]any{
			"cpuAvgPercent": 0.0, "cpuMaxPercent": 0.0, "memAvgMb": 0.0, "memMaxMb": 0.0,
		},
	}
	if row.Resources != nil {
		out["resources"] = map[string]any{
			"cpuAvgPercent": row.Resources.CPUAvgPercent,
			"cpuMaxPercent": row.Resources.CPUMaxPercent,
			"memAvgMb":      row.Resources.MemAvgMb,
			"memMaxMb":      row.Resources.MemMaxMb,
		}
	}
	if row.Status == "skipped" {
		out["note"] = "already running (locked); try again later"
	}
	if len(installed) > 0 {
		out["installedModules"] = installed
	}
	if installFailed {
		out["depInstallWarning"] = "dependency install exited non-zero — a failure may be caused by missing modules"
	}
	return out, false, nil
}

// ---------------------------------------------------------------------
// 4. get_history
// ---------------------------------------------------------------------

func (o *Ops) GetHistory(args map[string]any) (any, bool, error) {
	limit := 20
	// ContainsKey semantics, not a falsy-check: an omitted limit must not
	// collapse the default 20 down to 1.
	if v, ok := args["limit"]; ok {
		n := int(toFloat(v))
		if n < 1 {
			n = 1
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	name := stringify(args["script"])

	rows, _ := o.App.Hist.Last(2000)
	if name != "" {
		filtered := make([]history.Row, 0, len(rows))
		for _, r := range rows {
			if r.Script == name {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}

	runs := make([]map[string]any, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // newest first
		r := rows[i]
		logFile := ""
		if r.LogFile != nil {
			logFile = *r.LogFile
		}
		var logID any
		if logFile != "" {
			logID = filepath.Base(logFile)
		}
		startedAt := ""
		if t := r.StartedAt.Time(); !t.IsZero() {
			startedAt = t.UTC().Format("2006-01-02T15:04:05Z")
		}
		runs = append(runs, map[string]any{
			"script":      r.Script,
			"trigger":     r.Trigger,
			"status":      r.Status,
			"exitCode":    r.ExitCode,
			"startedAt":   startedAt,
			"durationSec": r.DurationSec,
			"logFile":     logFile,
			"logId":       logID,
		})
	}
	return map[string]any{"runs": runs}, false, nil
}

// ---------------------------------------------------------------------
// 5. get_run_log
// ---------------------------------------------------------------------

// logIDPattern is the strict allow-list: a log basename only — no
// separators, no traversal.
var logIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.log$`)

func (o *Ops) GetRunLog(args map[string]any) (any, bool, error) {
	logID := stringify(args["log_id"])
	if !logIDPattern.MatchString(logID) || strings.Contains(logID, "..") {
		return newToolError(fmt.Sprintf("invalid log_id '%s' — use the logId field from get_history", logID)), true, nil
	}
	path := filepath.Join(o.App.Paths.LogsDir, logID)
	if _, err := os.Stat(path); err != nil {
		return newNotFoundError(fmt.Sprintf(
			"log '%s' not found (pruned by retention — success logs of frequently-scheduled scripts keep 1 day, everything else %s days)",
			logID, formatNum(o.App.Cfg.LogRetentionDays))), true, nil
	}
	tailKb := 64
	if v, ok := args["tail_kb"]; ok {
		n := int(toFloat(v))
		if n < 1 {
			n = 1
		}
		if n > 256 {
			n = 256
		}
		tailKb = n
	}
	return map[string]any{"logId": logID, "log": history.LogTail(path, tailKb)}, false, nil
}

// ---------------------------------------------------------------------
// 6. sync_repos
// ---------------------------------------------------------------------

func (o *Ops) SyncRepos() (any, bool, error) {
	var lines []string
	ok := scripts.Sync(context.Background(), o.App.Cfg, o.App.Paths, o.App.Sec, func(l string) { lines = append(lines, l) })
	return map[string]any{"ok": ok, "output": strings.Join(lines, "\n")}, !ok, nil
}

// ---------------------------------------------------------------------
// 7. get_schedules
// ---------------------------------------------------------------------

func (o *Ops) GetSchedules() (any, bool, error) {
	schedules := o.App.Cron.Schedules()
	names := make([]string, 0, len(schedules))
	for name := range schedules {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		expr := schedules[name]
		items = append(items, map[string]any{"script": name, "cron": expr, "nextRun": nextRunISO(expr)})
	}
	return map[string]any{"schedules": items}, false, nil
}

// ---------------------------------------------------------------------
// 8. set_schedule
// ---------------------------------------------------------------------

func (o *Ops) SetSchedule(args map[string]any) (any, bool, error) {
	s, terr := o.resolveScript(args)
	if terr != nil {
		return terr, true, nil
	}
	expr := strings.TrimSpace(stringify(args["cron"]))
	if !cron.Validate(expr) {
		return newToolError(fmt.Sprintf(
			"invalid cron expression '%s' — use 5 fields (min hour dom mon dow, e.g. */30 * * * *) or @hourly/@daily/@weekly/@monthly/@reboot", expr)), true, nil
	}
	if err := o.App.Cron.Set(s.Name, expr); err != nil {
		return nil, false, err
	}
	return map[string]any{"script": s.Name, "cron": expr, "nextRun": nextRunISO(expr), "note": "schedule saved to crontab"}, false, nil
}

// ---------------------------------------------------------------------
// 9. remove_schedule — never errors once the script itself resolves.
// ---------------------------------------------------------------------

func (o *Ops) RemoveSchedule(args map[string]any) (any, bool, error) {
	s, terr := o.resolveScript(args)
	if terr != nil {
		return terr, true, nil
	}
	_, had := o.App.Cron.Schedules()[s.Name]
	if err := o.App.Cron.Remove(s.Name); err != nil {
		return nil, false, err
	}
	note := "no schedule was set"
	if had {
		note = "schedule removed"
	}
	return map[string]any{"script": s.Name, "note": note}, false, nil
}

// ---------------------------------------------------------------------
// 10. install_deps
// ---------------------------------------------------------------------

func (o *Ops) InstallDeps(args map[string]any) (any, bool, error) {
	s, terr := o.resolveScript(args)
	if terr != nil {
		return terr, true, nil
	}
	missing := o.scanMissing(s)
	if len(missing) == 0 {
		return map[string]any{"script": s.Name, "upToDate": true}, false, nil
	}
	output, ok := o.runInstall(s, missing)
	installed := make([]string, len(missing))
	for i, d := range missing {
		installed[i] = d.Display
	}
	return map[string]any{"script": s.Name, "installed": installed, "ok": ok, "output": output}, !ok, nil
}

// ---------------------------------------------------------------------
// 11. update_app
// ---------------------------------------------------------------------

func (o *Ops) UpdateApp() (any, bool, error) {
	out, err := exec.Command("git", deps.GitPullFFOnlyArgs(o.App.Paths.AppDir)...).CombinedOutput()
	ok := err == nil
	return map[string]any{
		"ok":     ok,
		"output": o.App.Sec.Redact(string(out)),
		"note":   "restart the MCP service to apply: systemctl restart scriptorium-mcp",
	}, !ok, nil
}

// ---------------------------------------------------------------------
// 12. update_packages
// ---------------------------------------------------------------------

func (o *Ops) UpdatePackages() (any, bool, error) {
	var lines []string

	if exec.Command("sudo", "-n", "true").Run() == nil {
		lines = append(lines, "== apt upgrade (powershell + python) ==")
		out, _ := exec.Command("bash", "-c", deps.AptUpgradeScript).CombinedOutput()
		for _, l := range splitNonEmptyLines(string(out)) {
			lines = append(lines, o.App.Sec.Redact(l))
		}
	} else {
		lines = append(lines, deps.AptSkipNote)
	}

	lines = append(lines, "== module dirs ==")
	outMods, errMods := exec.Command(o.App.Cfg.PwshBin, "-NoProfile", "-NonInteractive", "-Command",
		deps.ModuleUpgradeCommand(o.App.Paths.ModulesDir)).CombinedOutput()
	for _, l := range splitNonEmptyLines(string(outMods)) {
		lines = append(lines, o.App.Sec.Redact(l))
	}
	okMods := errMods == nil

	lines = append(lines, "== python venvs ==")
	outVenvs, errVenvs := exec.Command(o.App.Cfg.PwshBin, "-NoProfile", "-NonInteractive", "-Command",
		deps.VenvUpgradeCommand(o.App.Paths.VenvsDir, o.App.Cfg.PythonBin)).CombinedOutput()
	for _, l := range splitNonEmptyLines(string(outVenvs)) {
		lines = append(lines, o.App.Sec.Redact(l))
	}
	okVenvs := errVenvs == nil

	ok := okMods && okVenvs
	return map[string]any{"ok": ok, "output": strings.Join(lines, "\n")}, !ok, nil
}
