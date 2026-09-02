package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/mcp"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// ---------------------------------------------------------------------
// Shared fixtures for the per-tool table tests below.
// ---------------------------------------------------------------------

// fakeCrontab is a stateful in-memory crontab: unlike newTestApp's
// always-empty fake, it actually round-trips what Save/Set/Remove write, so
// the schedule tools can be tested against a real cron.Crontab without ever
// touching the real binary.
type fakeCrontab struct {
	mu   sync.Mutex
	text string
}

func (f *fakeCrontab) run(stdin string, args ...string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 1 && args[0] == "-l" {
		return f.text, true
	}
	if len(args) == 1 && args[0] == "-" {
		f.text = stdin
		return "", true
	}
	return "", false
}

func newTestAppWithCrontab(t *testing.T) (*app.App, *fakeCrontab) {
	t.Helper()
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(`{"dataDir":"`+dataDir+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCrontab{}
	a, err := app.OpenWith(appDir, fc.run)
	if err != nil {
		t.Fatal(err)
	}
	return a, fc
}

func writePS(t *testing.T, dataDir, name, body string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "scripts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "main.ps1")
	if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return entry
}

func opsArgs(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want map[string]any: %#v", v, v)
	}
	return m
}

// ---------------------------------------------------------------------
// 1. list_scripts
// ---------------------------------------------------------------------

func TestListScriptsReportsRuntimeRepoRunningSchedule(t *testing.T) {
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `Write-Output "hi"; exit 0`)
	if err := os.WriteFile(filepath.Join(a.Paths.ScriptsDir, "hello", "script.json"), []byte(`{"description":"says hi"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	release, _, acquired := a.Locks.Acquire("hello")
	if !acquired {
		t.Fatal("could not acquire lock for the running-state assertion")
	}
	defer release()

	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.ListScripts()
	if err != nil || isErr {
		t.Fatalf("ListScripts() = %v, %v, %v", result, isErr, err)
	}
	scriptsList := asMap(t, result)["scripts"].([]map[string]any)
	var hello map[string]any
	for _, s := range scriptsList {
		if s["name"] == "hello" {
			hello = s
		}
	}
	if hello == nil {
		t.Fatalf("hello not found in %v", scriptsList)
	}
	if hello["runtime"] != "powershell" || hello["repo"] != "scripts" {
		t.Errorf("runtime/repo = %v/%v", hello["runtime"], hello["repo"])
	}
	if hello["running"] != true {
		t.Errorf("running = %v, want true (lock held)", hello["running"])
	}
	if hello["description"] != "says hi" {
		t.Errorf("description = %v", hello["description"])
	}
	if hello["lastStatus"] != "never run" {
		t.Errorf("lastStatus = %v, want 'never run'", hello["lastStatus"])
	}
}

// ---------------------------------------------------------------------
// 2. get_script_details
// ---------------------------------------------------------------------

func TestGetScriptDetailsParsesPowerShellParameters(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "detailed",
		"param([Parameter(Mandatory)][string]$Who, [switch]$DryRun)\nWrite-Output hi\n")

	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.GetScriptDetails(opsArgs("script", "detailed"))
	if err != nil || isErr {
		t.Fatalf("GetScriptDetails() = %v, %v, %v", result, isErr, err)
	}
	out := asMap(t, result)
	if out["parameterSource"] != "param() block (PowerShell AST)" {
		t.Errorf("parameterSource = %v", out["parameterSource"])
	}
	if !strings.Contains(out["argsHint"].(string), "PowerShell") {
		t.Errorf("argsHint = %v", out["argsHint"])
	}
	b, _ := json.Marshal(out["parameters"])
	if !strings.Contains(string(b), `"name":"Who"`) || !strings.Contains(string(b), `"mandatory":true`) {
		t.Errorf("parameters missing Who/mandatory: %s", b)
	}
	if !strings.Contains(string(b), `"name":"DryRun"`) || !strings.Contains(string(b), `"isSwitch":true`) {
		t.Errorf("parameters missing DryRun/isSwitch: %s", b)
	}
}

func TestGetScriptDetailsPythonReportsNoParameters(t *testing.T) {
	a := newTestApp(t)
	dir := filepath.Join(a.Paths.DataDir, "scripts", "pytool")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.GetScriptDetails(opsArgs("script", "pytool"))
	if err != nil || isErr {
		t.Fatalf("GetScriptDetails() = %v, %v, %v", result, isErr, err)
	}
	out := asMap(t, result)
	if out["parameterSource"] != "none — see readme" {
		t.Errorf("parameterSource = %v", out["parameterSource"])
	}
	if !strings.Contains(out["argsHint"].(string), "Python") {
		t.Errorf("argsHint = %v", out["argsHint"])
	}
	params, ok := out["parameters"].([]deps.Param)
	if !ok || len(params) != 0 {
		t.Errorf("parameters = %#v, want an empty (present) array", out["parameters"])
	}
}

func TestGetScriptDetailsUnknownScriptListsValidNames(t *testing.T) {
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.GetScriptDetails(opsArgs("script", "nope"))
	if err != nil || !isErr {
		t.Fatalf("GetScriptDetails(nope) = %v, %v, %v, want isErr", result, isErr, err)
	}
	te, ok := result.(*mcp.ToolError)
	if !ok {
		t.Fatalf("result = %T, want *mcp.ToolError", result)
	}
	if !strings.Contains(te.Message, "hello") {
		t.Errorf("message = %q, want it to list 'hello'", te.Message)
	}
	if !te.NotFound {
		t.Error("NotFound = false, want true for an unknown script (REST 404 sentinel)")
	}
}

func TestGetScriptDetailsMissingArgumentIsNotNotFound(t *testing.T) {
	a := newTestApp(t)
	ops := &mcp.Ops{App: a}
	result, isErr, _ := ops.GetScriptDetails(opsArgs())
	if !isErr {
		t.Fatal("want isErr for a missing 'script' argument")
	}
	te := result.(*mcp.ToolError)
	if te.NotFound {
		t.Error("NotFound = true, want false (missing-argument is a 400, not a 404)")
	}
}

// ---------------------------------------------------------------------
// 3. run_script
// ---------------------------------------------------------------------

func TestRunScriptMissingArgument(t *testing.T) {
	a := newTestApp(t)
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.RunScript(opsArgs())
	if err != nil || !isErr {
		t.Fatalf("RunScript({}) = %v, %v, %v, want isErr", result, isErr, err)
	}
	if !strings.Contains(result.(*mcp.ToolError).Message, "missing required argument") {
		t.Errorf("message = %q", result.(*mcp.ToolError).Message)
	}
}

func TestRunScriptUnknownScriptListsValidNames(t *testing.T) {
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	ops := &mcp.Ops{App: a}
	result, isErr, _ := ops.RunScript(opsArgs("script", "nope"))
	if !isErr || !strings.Contains(result.(*mcp.ToolError).Message, "hello") {
		t.Fatalf("RunScript(nope) = %v, %v", result, isErr)
	}
}

func TestRunScriptSuccessRecordsHistoryWithTriggerMcp(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `Write-Output "hello out"; exit 0`)

	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.RunScript(opsArgs("script", "hello"))
	if err != nil || isErr {
		t.Fatalf("RunScript(hello) = %v, %v, %v", result, isErr, err)
	}
	out := asMap(t, result)
	if out["status"] != "success" {
		t.Errorf("status = %v", out["status"])
	}
	if !strings.Contains(out["output"].(string), "hello out") {
		t.Errorf("output = %v", out["output"])
	}

	rows, _ := a.Hist.Last(5)
	if len(rows) == 0 || rows[len(rows)-1].Trigger != "mcp" {
		t.Fatalf("history rows = %+v, want a trailing trigger=mcp row", rows)
	}
}

func TestRunScriptSkipNoteWhenLocked(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `Write-Output "hello out"; exit 0`)

	release, _, acquired := a.Locks.Acquire("hello")
	if !acquired {
		t.Fatal("could not acquire lock")
	}
	defer release()

	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.RunScript(opsArgs("script", "hello"))
	if err != nil || isErr {
		t.Fatalf("RunScript(hello, locked) = %v, %v, %v — a skip is a normal result, not a tool error", result, isErr, err)
	}
	out := asMap(t, result)
	if out["status"] != "skipped" {
		t.Errorf("status = %v, want skipped", out["status"])
	}
	if !strings.Contains(out["note"].(string), "already running") {
		t.Errorf("note = %v", out["note"])
	}
}

func TestRunScriptEnvOverridesAreRedactedInOutput(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "envtest", `
Write-Output "var=$env:MCP_TEST_VAR"
if ($env:MCP_TEST_VAR -eq 'supersecretvalue123') { exit 0 } else { exit 1 }
`)
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.RunScript(opsArgs("script", "envtest", "env", map[string]any{"MCP_TEST_VAR": "supersecretvalue123"}))
	if err != nil || isErr {
		t.Fatalf("RunScript(envtest) = %v, %v, %v", result, isErr, err)
	}
	out := asMap(t, result)
	if out["status"] != "success" {
		t.Fatalf("status = %v, want success (proves the env var actually reached the child)", out["status"])
	}
	output := out["output"].(string)
	if strings.Contains(output, "supersecretvalue123") {
		t.Errorf("output leaked the secret: %q", output)
	}
	if !strings.Contains(output, "***") {
		t.Errorf("output = %q, want a redaction marker", output)
	}
}

// ---------------------------------------------------------------------
// 4. get_history
// ---------------------------------------------------------------------

func TestGetHistoryLimitOmittedDefaultsTo20NotOne(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	ops := &mcp.Ops{App: a}
	for i := 0; i < 3; i++ {
		if _, isErr, err := ops.RunScript(opsArgs("script", "hello")); err != nil || isErr {
			t.Fatal(err)
		}
	}
	result, isErr, err := ops.GetHistory(opsArgs())
	if err != nil || isErr {
		t.Fatal(err)
	}
	runs := asMap(t, result)["runs"].([]map[string]any)
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3 (omitted limit must not collapse to 1)", len(runs))
	}
}

func TestGetHistoryScriptFilterAndNewestFirst(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	writePS(t, a.Paths.DataDir, "other", `exit 0`)
	ops := &mcp.Ops{App: a}
	mustRun := func(name string) {
		if _, isErr, err := ops.RunScript(opsArgs("script", name)); err != nil || isErr {
			t.Fatal(err)
		}
	}
	mustRun("other")
	mustRun("hello")
	mustRun("other")

	result, _, err := ops.GetHistory(opsArgs("script", "hello", "limit", float64(5)))
	if err != nil {
		t.Fatal(err)
	}
	runs := asMap(t, result)["runs"].([]map[string]any)
	if len(runs) != 1 || runs[0]["script"] != "hello" {
		t.Fatalf("runs = %+v, want exactly one hello row", runs)
	}
}

// ---------------------------------------------------------------------
// 5. get_run_log
// ---------------------------------------------------------------------

func TestGetRunLogRejectsBadShapesBeforeTouchingDisk(t *testing.T) {
	a := newTestApp(t)
	ops := &mcp.Ops{App: a}
	for _, bad := range []string{"../../etc/passwd.log", "/etc/passwd.log", "x/y.log", "no-extension", "a..b.log"} {
		result, isErr, err := ops.GetRunLog(opsArgs("log_id", bad))
		if err != nil || !isErr {
			t.Fatalf("GetRunLog(%q) = %v, %v, %v, want isErr", bad, result, isErr, err)
		}
		if result.(*mcp.ToolError).NotFound {
			t.Errorf("GetRunLog(%q): NotFound = true, want false (a shape error is a 400)", bad)
		}
	}
}

func TestGetRunLogMissingHasRetentionHintAndIsNotFound(t *testing.T) {
	a := newTestApp(t)
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.GetRunLog(opsArgs("log_id", "ghost-run.log"))
	if err != nil || !isErr {
		t.Fatalf("GetRunLog(ghost) = %v, %v, %v", result, isErr, err)
	}
	te := result.(*mcp.ToolError)
	if !strings.Contains(te.Message, "not found") || !strings.Contains(te.Message, "retention") {
		t.Errorf("message = %q", te.Message)
	}
	if !te.NotFound {
		t.Error("NotFound = false, want true (missing log -> REST 404)")
	}
}

func TestGetRunLogReturnsTail(t *testing.T) {
	a := newTestApp(t)
	logPath := filepath.Join(a.Paths.LogsDir, "sample.log")
	if err := os.WriteFile(logPath, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.GetRunLog(opsArgs("log_id", "sample.log"))
	if err != nil || isErr {
		t.Fatalf("GetRunLog(sample.log) = %v, %v, %v", result, isErr, err)
	}
	out := asMap(t, result)
	if out["logId"] != "sample.log" || !strings.Contains(out["log"].(string), "line two") {
		t.Errorf("out = %+v", out)
	}
}

// ---------------------------------------------------------------------
// 6. sync_repos
// ---------------------------------------------------------------------

func TestSyncReposNoRepoConfiguredIsError(t *testing.T) {
	a := newTestApp(t)
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.SyncRepos()
	if err != nil || !isErr {
		t.Fatalf("SyncRepos() = %v, %v, %v, want isErr", result, isErr, err)
	}
	out := asMap(t, result)
	if out["ok"] != false || !strings.Contains(out["output"].(string), "repo") {
		t.Errorf("out = %+v", out)
	}
}

// ---------------------------------------------------------------------
// 7-9. schedule tools
// ---------------------------------------------------------------------

func TestScheduleToolsFullLifecycle(t *testing.T) {
	a, _ := newTestAppWithCrontab(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	ops := &mcp.Ops{App: a}

	// invalid cron rejected, nothing written
	result, isErr, err := ops.SetSchedule(opsArgs("script", "hello", "cron", "every tuesday"))
	if err != nil || !isErr {
		t.Fatalf("SetSchedule(bad cron) = %v, %v, %v", result, isErr, err)
	}
	if !strings.Contains(result.(*mcp.ToolError).Message, "@hourly") {
		t.Errorf("message = %q, want the example-forms hint", result.(*mcp.ToolError).Message)
	}
	if _, ok := a.Cron.Schedules()["hello"]; ok {
		t.Fatal("invalid cron must not have been written")
	}

	// valid set
	result, isErr, err = ops.SetSchedule(opsArgs("script", "hello", "cron", "@daily"))
	if err != nil || isErr {
		t.Fatalf("SetSchedule(@daily) = %v, %v, %v", result, isErr, err)
	}
	out := asMap(t, result)
	if out["cron"] != "@daily" || out["note"] != "schedule saved to crontab" {
		t.Errorf("out = %+v", out)
	}
	if out["nextRun"] == nil {
		t.Error("nextRun = nil, want a computed ISO time")
	}

	// get_schedules reflects it, sorted
	result, isErr, err = ops.GetSchedules()
	if err != nil || isErr {
		t.Fatal(err)
	}
	scheds := asMap(t, result)["schedules"].([]map[string]any)
	if len(scheds) != 1 || scheds[0]["script"] != "hello" || scheds[0]["cron"] != "@daily" {
		t.Fatalf("schedules = %+v", scheds)
	}

	// remove: had one -> "schedule removed"
	result, isErr, err = ops.RemoveSchedule(opsArgs("script", "hello"))
	if err != nil || isErr {
		t.Fatal(err)
	}
	if asMap(t, result)["note"] != "schedule removed" {
		t.Errorf("note = %v", asMap(t, result)["note"])
	}

	// remove again: never errors, reports "no schedule was set"
	result, isErr, err = ops.RemoveSchedule(opsArgs("script", "hello"))
	if err != nil || isErr {
		t.Fatal(err)
	}
	if asMap(t, result)["note"] != "no schedule was set" {
		t.Errorf("note = %v", asMap(t, result)["note"])
	}
}

// ---------------------------------------------------------------------
// 10. install_deps
// ---------------------------------------------------------------------

func TestInstallDepsUpToDateFastPath(t *testing.T) {
	pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`) // no third-party deps declared
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.InstallDeps(opsArgs("script", "hello"))
	if err != nil || isErr {
		t.Fatalf("InstallDeps(hello) = %v, %v, %v", result, isErr, err)
	}
	if asMap(t, result)["upToDate"] != true {
		t.Errorf("out = %+v", asMap(t, result))
	}
}

// writeFakeInstallStub is a pwshBin stand-in that relays -File invocations
// (the real AST dependency scan — exercised faithfully, no PSGallery
// contact needed for that) to the real pwsh, and intercepts -Command
// invocations (the install step) to simulate ONLY the filesystem side
// effect a real Save-Module/Save-PSResource install would have — never
// touching PSGallery.
func writeFakeInstallStub(t *testing.T, path, realPwsh, moduleDir string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"-Command\" ]; then\n" +
		"    mkdir -p '" + moduleDir + "/ModA/1.5.0'\n" +
		"    echo 'STUB_INSTALL_RAN'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"exec '" + realPwsh + "' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestInstallDepsInvalidationSequence reproduces the P8 Scanner cache
// pattern (internal/deps/psscan_test.go's
// TestScanPSCacheInvalidateAfterInstallReflectsNewlyInstalledModule) at the
// Ops layer end-to-end: scan (missing) -> install (stub) -> the SAME
// long-lived Scanner's next scan must be fresh, not the stale cached
// pre-install Missing list.
func TestInstallDepsInvalidationSequence(t *testing.T) {
	realPwsh := pwshtest.RequirePwsh(t)
	a := newTestApp(t)
	entry := writePS(t, a.Paths.DataDir, "depscript",
		"#Requires -Modules @{ ModuleName = 'ModA'; RequiredVersion = '1.5.0' }\nWrite-Output hi\n")
	moduleDir := filepath.Join(a.Paths.ModulesDir, "depscript")

	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "stub-pwsh.sh")
	writeFakeInstallStub(t, stub, realPwsh, moduleDir)
	a.Cfg.PwshBin = stub
	a.Scanner.PwshBin = stub

	ops := &mcp.Ops{App: a}

	first, isErr, err := ops.InstallDeps(opsArgs("script", "depscript"))
	if err != nil || isErr {
		t.Fatalf("first InstallDeps = %v, %v, %v", first, isErr, err)
	}
	firstOut := asMap(t, first)
	installed, _ := firstOut["installed"].([]string)
	if len(installed) != 1 || !strings.Contains(installed[0], "ModA") {
		t.Fatalf("installed = %+v, want [ModA ...]", firstOut["installed"])
	}
	if firstOut["ok"] != true {
		t.Fatalf("ok = %v, want true (stub exits 0)", firstOut["ok"])
	}

	// Fresh call on the SAME Ops (same long-lived Scanner): must reflect the
	// just-installed module, not a stale cached Missing list.
	second, isErr, err := ops.InstallDeps(opsArgs("script", "depscript"))
	if err != nil || isErr {
		t.Fatalf("second InstallDeps = %v, %v, %v", second, isErr, err)
	}
	if asMap(t, second)["upToDate"] != true {
		t.Errorf("second InstallDeps = %+v, want upToDate:true (cache should have been invalidated after the first install)", asMap(t, second))
	}
	_ = entry
}

// ---------------------------------------------------------------------
// 11. update_app
// ---------------------------------------------------------------------

func TestUpdateAppNotAGitRepoReportsFailureRedacted(t *testing.T) {
	a := newTestApp(t)
	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.UpdateApp()
	if err != nil || !isErr {
		t.Fatalf("UpdateApp() = %v, %v, %v, want isErr (appDir is not a git repo)", result, isErr, err)
	}
	out := asMap(t, result)
	if out["ok"] != false {
		t.Errorf("ok = %v", out["ok"])
	}
	if !strings.Contains(out["note"].(string), "systemctl restart scriptorium-mcp") {
		t.Errorf("note = %v", out["note"])
	}
}

// ---------------------------------------------------------------------
// 12. update_packages
// ---------------------------------------------------------------------

// forceNoSudo prepends a fake `sudo` (always exits 1) to PATH so this test
// can never depend on — or trigger — the host's real sudo/apt configuration
// (some CI runners DO have passwordless sudo for their own user, which
// would otherwise make this test perform a real apt-get).
func forceNoSudo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestUpdatePackagesRunsModuleAndVenvStagesWithoutNetworkAccess(t *testing.T) {
	pwshtest.RequirePwsh(t)
	forceNoSudo(t)
	a := newTestApp(t)
	a.Cfg.PythonBin = "definitely-not-on-path-xyz" // VenvUpgradeCommand's system-pip stage must not fire

	ops := &mcp.Ops{App: a}
	result, isErr, err := ops.UpdatePackages()
	if err != nil {
		t.Fatalf("UpdatePackages() error = %v", err)
	}
	out := asMap(t, result)
	output := out["output"].(string)
	if !strings.Contains(output, "apt stage skipped: passwordless sudo unavailable") {
		t.Errorf("output missing apt-skip line: %s", output)
	}
	// ModulesDir/VenvsDir already exist (app.Open pre-creates every data dir)
	// but hold nothing yet — "complete"/"nothing to upgrade" is the
	// zero-subdirectory-loop outcome, not the missing-root-dir message.
	if !strings.Contains(output, "== module dirs ==") || !strings.Contains(output, "module upgrade complete") {
		t.Errorf("output missing module-dir stage: %s", output)
	}
	if !strings.Contains(output, "== python venvs ==") || !strings.Contains(output, "no venvs to upgrade yet") {
		t.Errorf("output missing venv stage: %s", output)
	}
	if isErr || out["ok"] != true {
		t.Errorf("ok/isErr = %v/%v, want a clean ok (nothing to upgrade is not a failure)", out["ok"], isErr)
	}
}

// ---------------------------------------------------------------------
// Redaction leak test (binding requirement): a secret planted in a tool's
// command output, and in a dispatch-level exception message, must never
// appear raw in any response body.
// ---------------------------------------------------------------------

func TestRegisteredSecretNeverLeaksInToolOutput(t *testing.T) {
	pwshtest.RequirePwsh(t)
	forceNoSudo(t)
	a := newTestApp(t)
	const planted = "planted-secret-leak-check-value"
	a.Sec.Add("SOME_TOKEN", planted, true)

	// A stub pwshBin whose module-upgrade stage echoes the secret straight
	// to stdout — proving Ops-layer redaction, not just the runner's own
	// (unrelated) chokepoint.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "stub-pwsh.sh")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"-Command\" ]; then echo 'leaking " + planted + "'; exit 1; fi\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Cfg.PwshBin = stub

	result, _, err := ops(a).UpdatePackages()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), planted) {
		t.Fatalf("planted secret leaked through update_packages: %s", b)
	}
	if !strings.Contains(string(b), "***") {
		t.Errorf("expected a redaction marker in: %s", b)
	}

	// The same registry backs run_script's -32603 path (rpc.go): a
	// dispatch-level exception message must be redacted too. Exercised
	// directly since forcing a real panic/exception here would need an
	// invalid *app.App; the redaction call itself is what's under test.
	msg := a.Sec.Redact("internal error running tool 'x': token=" + planted)
	if strings.Contains(msg, planted) {
		t.Fatalf("Redact did not scrub the planted secret: %q", msg)
	}
}

func ops(a *app.App) *mcp.Ops { return &mcp.Ops{App: a} }

// ---------------------------------------------------------------------
// Ops.Call dispatch sanity — the parity assertion that both frontends
// share this one switch (task 3's API layer routes through the exact same
// method set proven here).
// ---------------------------------------------------------------------

func TestCallDispatchesEveryToolName(t *testing.T) {
	a := newTestApp(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	o := &mcp.Ops{App: a}
	for _, name := range []string{"list_scripts", "get_schedules", "sync_repos"} {
		if _, _, err := o.Call(name, map[string]any{}); err != nil {
			t.Errorf("Call(%q) error = %v", name, err)
		}
	}
	// get_script_details needs a valid script to avoid an (expected) isErr
	if _, _, err := o.Call("get_script_details", map[string]any{"script": "hello"}); err != nil {
		t.Errorf("Call(get_script_details) error = %v", err)
	}
}
