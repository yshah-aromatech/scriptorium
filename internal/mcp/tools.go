package mcp

import "encoding/json"

// --- tool schema registry (§11.9) -------------------------------------

// toolSchemaProp is one JSON Schema property of a tool's inputSchema.
type toolSchemaProp struct {
	Type                 string          `json:"type"`
	Description          string          `json:"description,omitempty"`
	AdditionalProperties *toolSchemaProp `json:"additionalProperties,omitempty"`
}

type toolInputSchema struct {
	Type       string                    `json:"type"`
	Required   []string                  `json:"required,omitempty"`
	Properties map[string]toolSchemaProp `json:"properties"`
}

type toolAnnotations struct {
	ReadOnlyHint   bool `json:"readOnlyHint,omitempty"`
	IdempotentHint bool `json:"idempotentHint,omitempty"`
}

type toolDef struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema toolInputSchema  `json:"inputSchema"`
	Annotations *toolAnnotations `json:"annotations,omitempty"`
}

var (
	readOnly       = &toolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	idempotentOnly = &toolAnnotations{IdempotentHint: true}

	scriptArg = toolSchemaProp{Type: "string", Description: "Script name exactly as returned by list_scripts"}
)

// toolDefs is the exact 12-tool registry — names, descriptions, schemas and
// annotations byte-copied from src/Mcp.psm1's Get-StoMcpTools (em-dashes are
// U+2014). Order is the wire contract: tools/list and the -32602
// unknown-tool listing both walk this slice in order. Only 5 tools carry
// readOnlyHint (list_scripts, get_script_details, get_history, get_run_log,
// get_schedules); run_script/set_schedule/remove_schedule carry no
// annotations at all.
var toolDefs = []toolDef{
	{
		Name:        "list_scripts",
		Description: "List every script this server can run, with runtime (powershell/python), repo, description, last run status/duration, whether it is currently running, and its cron schedule. Call get_script_details before running an unfamiliar script.",
		InputSchema: toolInputSchema{Type: "object", Properties: map[string]toolSchemaProp{}},
		Annotations: readOnly,
	},
	{
		Name:        "get_script_details",
		Description: "Everything needed to call a script correctly: its README, documented environment variables (.env.example), default args, and — for PowerShell — the full parameter list (names, types, mandatory, defaults, allowed values, per-parameter help) parsed from the script's param() block. Call this before run_script when unsure about arguments.",
		InputSchema: toolInputSchema{Type: "object", Required: []string{"script"}, Properties: map[string]toolSchemaProp{
			"script": scriptArg,
		}},
		Annotations: readOnly,
	},
	{
		Name:        "run_script",
		Description: `Run a script to completion and return its status, exit code and output. Blocks until the script finishes — these scripts normally run in under a couple of minutes. A script that is already running elsewhere returns status "skipped". Use get_script_details first to learn the accepted arguments.`,
		InputSchema: toolInputSchema{Type: "object", Required: []string{"script"}, Properties: map[string]toolSchemaProp{
			"script":          scriptArg,
			"args":            {Type: "string", Description: "Extra command-line arguments, quote-aware. PowerShell scripts: -ParamName value / bare -Switch (e.g. -DryRun -Role read); python: --flag value"},
			"env":             {Type: "object", Description: "Extra environment variables for this run only; override the script's .env values", AdditionalProperties: &toolSchemaProp{Type: "string"}},
			"timeout_minutes": {Type: "number", Description: "Override the run timeout for this run (minutes)"},
		}},
	},
	{
		Name:        "get_history",
		Description: "Recent run history (newest first), optionally filtered to one script. Each row has a logId usable with get_run_log.",
		InputSchema: toolInputSchema{Type: "object", Properties: map[string]toolSchemaProp{
			"script": {Type: "string", Description: "Only runs of this script"},
			"limit":  {Type: "number", Description: "Max entries to return (default 20, max 200)"},
		}},
		Annotations: readOnly,
	},
	{
		Name:        "get_run_log",
		Description: "Fetch the (secret-redacted) log of a past run by its logId from get_history — use it to diagnose failures beyond the short output tail.",
		InputSchema: toolInputSchema{Type: "object", Required: []string{"log_id"}, Properties: map[string]toolSchemaProp{
			"log_id":  {Type: "string", Description: "logId value from a get_history row"},
			"tail_kb": {Type: "number", Description: "How much of the end of the log to return in KB (default 64, max 256)"},
		}},
		Annotations: readOnly,
	},
	{
		Name:        "sync_repos",
		Description: "Sync (git pull/hard-reset) all configured scripts repos so the latest scripts are available. Run this before list_scripts if the repos may have changed.",
		InputSchema: toolInputSchema{Type: "object", Properties: map[string]toolSchemaProp{}},
		Annotations: idempotentOnly,
	},
	{
		Name:        "get_schedules",
		Description: "All cron schedules currently configured, with each next fire time.",
		InputSchema: toolInputSchema{Type: "object", Properties: map[string]toolSchemaProp{}},
		Annotations: readOnly,
	},
	{
		Name:        "set_schedule",
		Description: "Create or replace a script's cron schedule. Accepts a 5-field cron expression (e.g. */30 * * * *) or @hourly/@daily/@weekly/@monthly/@reboot. The schedule is written to the server's crontab and runs through the full pipeline (deps, logs, webhook).",
		InputSchema: toolInputSchema{Type: "object", Required: []string{"script", "cron"}, Properties: map[string]toolSchemaProp{
			"script": scriptArg,
			"cron":   {Type: "string", Description: "5-field cron expression or @hourly/@daily/@weekly/@monthly/@reboot"},
		}},
	},
	{
		Name:        "remove_schedule",
		Description: "Remove a script's cron schedule.",
		InputSchema: toolInputSchema{Type: "object", Required: []string{"script"}, Properties: map[string]toolSchemaProp{
			"script": scriptArg,
		}},
	},
	{
		Name:        "install_deps",
		Description: "Scan a script's dependencies (PowerShell modules or python packages) and install whatever is missing into its isolated module dir / venv. Safe to call repeatedly.",
		InputSchema: toolInputSchema{Type: "object", Required: []string{"script"}, Properties: map[string]toolSchemaProp{
			"script": scriptArg,
		}},
		Annotations: idempotentOnly,
	},
	{
		Name:        "update_app",
		Description: "Update this app itself (git pull --ff-only). The MCP service must be restarted afterwards to apply.",
		InputSchema: toolInputSchema{Type: "object", Properties: map[string]toolSchemaProp{}},
		Annotations: idempotentOnly,
	},
	{
		Name:        "update_packages",
		Description: "Upgrade every PowerShell module dir and python venv to latest package versions (plus apt packages when passwordless sudo is available). Can take several minutes — raise the tool timeout before calling.",
		InputSchema: toolInputSchema{Type: "object", Properties: map[string]toolSchemaProp{}},
		Annotations: idempotentOnly,
	},
}

// toolNames is toolDefs' names, in order — used by the -32602 unknown-tool
// listing (and Resolve-StoMcpScript-alike valid-name listings elsewhere).
var toolNames = func() []string {
	out := make([]string, len(toolDefs))
	for i, t := range toolDefs {
		out[i] = t.Name
	}
	return out
}()

var validToolName = func() map[string]bool {
	m := make(map[string]bool, len(toolDefs))
	for _, n := range toolNames {
		m[n] = true
	}
	return m
}()

// toolCallResult builds the MCP content/isError envelope from an Ops
// method's result. A *ToolError renders as its bare message (PS: `Text =
// $r.Error`, a plain string); every other result renders as its compact
// JSON encoding (PS: `Text = (... | ConvertTo-Json -Compress)`).
func toolCallResult(result any, isErr bool) map[string]any {
	var text string
	if te, ok := result.(*ToolError); ok {
		text = te.Message
	} else {
		b, err := json.Marshal(result)
		if err != nil {
			text = err.Error()
		} else {
			text = string(b)
		}
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// Call dispatches one tool call by name onto the shared Ops layer — the
// single place both the JSON-RPC tools/call handler and (indirectly,
// through the same Ops methods) the REST API route to. name is assumed
// already validated against toolDefs by the caller.
func (o *Ops) Call(name string, args map[string]any) (result any, isErr bool, err error) {
	switch name {
	case "list_scripts":
		return o.ListScripts()
	case "get_script_details":
		return o.GetScriptDetails(args)
	case "run_script":
		return o.RunScript(args)
	case "get_history":
		return o.GetHistory(args)
	case "get_run_log":
		return o.GetRunLog(args)
	case "sync_repos":
		return o.SyncRepos()
	case "get_schedules":
		return o.GetSchedules()
	case "set_schedule":
		return o.SetSchedule(args)
	case "remove_schedule":
		return o.RemoveSchedule(args)
	case "install_deps":
		return o.InstallDeps(args)
	case "update_app":
		return o.UpdateApp()
	case "update_packages":
		return o.UpdatePackages()
	default:
		return newToolError("unknown tool '" + name + "'"), true, nil
	}
}
