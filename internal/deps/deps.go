// Package deps is the dependency-scanning and install-command-building half
// of scriptorium: it finds a script's declared PowerShell module / Python
// package dependencies, checks which are missing, and builds the pwsh
// -Command strings that install them. Port of src/Deps.psm1.
//
// PowerShell scanning ([version] comparisons, AST walking) stays pwsh-native
// — reimplementing PowerShell's own version/AST semantics in Go would be a
// second, divergence-prone copy of logic .NET already gets right. Instead the
// whole PS pipeline (scan, filter, name-map, installed-modules union,
// satisfaction check, param block) is transcribed into one embedded
// standalone script (scanner.ps1); Go shells out to it and parses its JSON
// result. Python scanning (pyscan.go) is a thinner Go layer since its own
// embedded scanner.py only finds imports — Go does the installed/missing
// classification via `pip list`.
package deps

// Dep is one dependency: a name plus optional version constraints. Only
// #Requires -Modules hashtable syntax carries version constraints in the
// PowerShell path; Python deps are always unversioned (name + PipName).
// Display byte-rules mirror New-StoDep: "Name", "Name (=RV)",
// "Name (>=Min,<=Max)", "Name (>=Min)", "Name (<=Max)".
type Dep struct {
	Name            string
	RequiredVersion string
	MinimumVersion  string
	MaximumVersion  string
	Display         string
	PipName         string // Python only; empty for PowerShell deps
}

// Param is one PowerShell script parameter, shaped from Get-StoScriptParameters
// (src/Scripts.psm1). Default is nil when the parameter has no default value
// expression (distinct from a default that is itself the empty string). JSON
// tags are the MCP-facing spelling (get_script_details serializes these
// directly — mirrors envfile.DocEntry's same rationale).
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Mandatory   bool     `json:"mandatory"`
	Default     *string  `json:"default"`
	ValidateSet []string `json:"validateSet"`
	IsSwitch    bool     `json:"isSwitch"`
	Description string   `json:"description"`
}

// PSScanResult is what ScanPS returns: the script's declared PowerShell
// module deps, the subset currently unsatisfied, its param() block, and the
// same comment-based-help fields Get-StoScriptParameters returns alongside
// Parameters (Synopsis/Help/ParseWarnings) — get_script_details (P9) composes
// its optional help{synopsis,description} and parseWarnings fields from
// these, never a second AST scan. Synopsis/Help are "" and ParseWarnings is
// 0 when the scanner degraded (no AST parse happened at all) or the script
// simply has neither comment-based help nor a parse error.
// Degraded is true only on the regex-fallback path (pwsh unavailable), in
// which case Missing is always empty and Warning explains why (install
// checks require a real pwsh to inspect installed modules and [version]s).
type PSScanResult struct {
	Deps          []Dep
	Missing       []Dep
	Params        []Param
	Synopsis      string
	Help          string
	ParseWarnings int
	Degraded      bool
	Warning       string
}
