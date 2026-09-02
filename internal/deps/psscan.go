package deps

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed scanner.ps1
var scannerPS1 []byte

// degradedWarning is the exact warning text ruling 2 requires.
const degradedWarning = "pwsh not found — dependency scan degraded (regex), install checks skipped"

// psCacheEntry is one Scanner cache slot, invalidated on (size, mtime) drift
// of the entry file — same key PS never bothers with (it rescans every
// time); Go adds this because --run's CLI wiring can call ScanPS more than
// once per process for the same script.
type psCacheEntry struct {
	size   int64
	mtime  int64
	result PSScanResult
}

// Scanner runs the embedded PowerShell scanner (or, when pwsh is
// unavailable, the degraded regex fallback) and caches results per entry
// path. Zero value is ready to use.
type Scanner struct {
	PwshBin string

	mu         sync.Mutex
	cache      map[string]psCacheEntry
	scriptPath string // lazily materialized copy of scannerPS1
	scriptErr  error
	scriptOnce sync.Once
}

// ScanPS scans one PowerShell script's declared module dependencies, the
// subset currently unsatisfied, and its param() block. entry/dir/moduleDir
// mirror the script's own fields; loose mirrors Script.Loose (a root-level
// loose file's Dir is the whole repo, exempting the sibling-folder
// exclusion). Cache hit on an unchanged (size, mtime) skips the pwsh
// invocation entirely.
func (s *Scanner) ScanPS(entry, dir, moduleDir string, loose bool) (PSScanResult, error) {
	info, statErr := os.Stat(entry)
	if statErr == nil {
		if cached, ok := s.cached(entry, info.Size(), info.ModTime().UnixNano()); ok {
			return cached, nil
		}
	}

	result, err := s.runScanner(entry, dir, moduleDir, loose)
	if err != nil {
		return result, err
	}

	if statErr == nil {
		s.store(entry, info.Size(), info.ModTime().UnixNano(), result)
	}
	return result, nil
}

func (s *Scanner) cached(entry string, size, mtime int64) (PSScanResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ce, ok := s.cache[entry]
	if !ok || ce.size != size || ce.mtime != mtime {
		return PSScanResult{}, false
	}
	return ce.result, true
}

func (s *Scanner) store(entry string, size, mtime int64, result PSScanResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]psCacheEntry{}
	}
	s.cache[entry] = psCacheEntry{size: size, mtime: mtime, result: result}
}

// Invalidate drops any cached ScanPS result for entry. The (size, mtime)
// cache key only observes the entry file itself, not moduleDir's contents or
// the system's installed modules — both of which an install command changes
// without touching entry at all. A caller that just ran an install for this
// entry MUST call Invalidate before relying on the next ScanPS to reflect
// it, or a long-lived Scanner (P9's MCP server, P10's TUI) would keep
// serving the pre-install Missing list indefinitely. A fresh --run process
// has nothing to invalidate (its Scanner is new), so this is a no-op there.
func (s *Scanner) Invalidate(entry string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, entry)
}

// materializedScript writes the embedded scanner.ps1 to a content-addressed
// path under os.TempDir() — the same path for every Scanner in every process
// running this binary — and skips the write if it's already there. A plain
// os.CreateTemp-per-Scanner (the original approach) never got cleaned up:
// one file leaked per --run, indefinitely on a system that doesn't sweep
// /tmp. Content-addressing means at most one file per binary version per
// machine, ever, with no cleanup required — the identical-name-implies-
// identical-content invariant is what makes skipping a rewrite safe, and
// what would let a future scanner.ps1 edit land under a new name instead of
// silently reusing stale content on disk.
func (s *Scanner) materializedScript() (string, error) {
	s.scriptOnce.Do(func() {
		sum := sha256.Sum256(scannerPS1)
		path := filepath.Join(os.TempDir(), "scriptorium-deps-scanner-"+hex.EncodeToString(sum[:])[:12]+".ps1")
		if _, err := os.Stat(path); err == nil {
			s.scriptPath = path
			return
		}
		// write-then-rename: a reader can never observe a partially-written
		// file at the final content-addressed path.
		tmp := path + fmt.Sprintf(".tmp-%d", os.Getpid())
		if err := os.WriteFile(tmp, scannerPS1, 0o644); err != nil {
			s.scriptErr = err
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			s.scriptErr = err
			return
		}
		s.scriptPath = path
	})
	return s.scriptPath, s.scriptErr
}

func (s *Scanner) pwshBin() string {
	if s.PwshBin != "" {
		return s.PwshBin
	}
	return "pwsh"
}

// runScanner invokes the embedded scanner.ps1 via pwsh and parses its last
// stdout line as JSON. A pwsh that can't even be started (not found, not
// executable) degrades to the regex fallback (ruling 2); any other failure
// (bad exit, unparseable output) is a real scan error the caller decides how
// to handle (the CLI --run flow swallows it, matching PS's own
// Get-StoMissingDeps-failure-yields-empty-missing behavior).
func (s *Scanner) runScanner(entry, dir, moduleDir string, loose bool) (PSScanResult, error) {
	looseArg := "false"
	if loose {
		looseArg = "true"
	}

	scriptPath, err := s.materializedScript()
	if err != nil {
		return PSScanResult{}, fmt.Errorf("materializing embedded scanner: %w", err)
	}

	cmd := exec.Command(s.pwshBin(), "-NoProfile", "-NonInteractive", "-File", scriptPath, entry, dir, moduleDir, looseArg)
	out, runErr := cmd.Output()
	if runErr != nil {
		// "missing/unrunnable" means the process never actually started: a
		// bare name not on $PATH (*exec.Error) or an absolute/relative path
		// that doesn't exist or isn't executable (a *fs.PathError from
		// os.StartProcess). An *exec.ExitError means pwsh DID run — that's a
		// real scan error (a script bug, not an absent interpreter), not a
		// degrade-worthy condition.
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return regexFallback(entry, dir)
		}
		return PSScanResult{}, fmt.Errorf("pwsh scan failed: %w", runErr)
	}

	line := lastNonEmptyLine(out)
	if line == "" {
		return PSScanResult{}, fmt.Errorf("pwsh scan produced no output")
	}
	var doc psScanDoc
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		return PSScanResult{}, fmt.Errorf("parsing pwsh scan output: %w", err)
	}
	return doc.toResult(), nil
}

func lastNonEmptyLine(out []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// psScanDoc is the JSON shape scanner.ps1 emits; field names match its
// ConvertTo-Json output verbatim.
type psScanDoc struct {
	Deps    []psDepDoc   `json:"deps"`
	Missing []psDepDoc   `json:"missing"`
	Params  []psParamDoc `json:"params"`
}

type psDepDoc struct {
	Name            string `json:"name"`
	RequiredVersion string `json:"requiredVersion"`
	MinimumVersion  string `json:"minimumVersion"`
	MaximumVersion  string `json:"maximumVersion"`
	Display         string `json:"display"`
}

type psParamDoc struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Mandatory   bool     `json:"mandatory"`
	Default     *string  `json:"default"`
	ValidateSet []string `json:"validateSet"`
	IsSwitch    bool     `json:"isSwitch"`
	Description string   `json:"description"`
}

func (d psDepDoc) toDep() Dep {
	return Dep{
		Name:            d.Name,
		RequiredVersion: d.RequiredVersion,
		MinimumVersion:  d.MinimumVersion,
		MaximumVersion:  d.MaximumVersion,
		Display:         d.Display,
	}
}

func (doc psScanDoc) toResult() PSScanResult {
	r := PSScanResult{
		Deps:    make([]Dep, len(doc.Deps)),
		Missing: make([]Dep, len(doc.Missing)),
		Params:  make([]Param, len(doc.Params)),
	}
	for i, d := range doc.Deps {
		r.Deps[i] = d.toDep()
	}
	for i, d := range doc.Missing {
		r.Missing[i] = d.toDep()
	}
	for i, p := range doc.Params {
		r.Params[i] = Param(p)
	}
	return r
}

// ---------------------------------------------------------------------------
// Degraded fallback (ruling 2): pwsh unavailable. Dep NAMES only, collected
// via regex over #Requires -Modules bare names, `using module X`, and
// `Import-Module`/`ipmo` first-argument simple literals — no AST, no
// [version] semantics, no installed-module check (Missing stays empty: with
// no pwsh there's no way to install anything anyway, so reporting
// "everything missing" would only invite a doomed install attempt).
// ---------------------------------------------------------------------------

var (
	requiresRe     = regexp.MustCompile(`(?im)^\s*#Requires\s+-Modules\s+(.+?)\s*$`)
	usingModuleRe  = regexp.MustCompile(`(?im)^\s*using\s+module\s+(\S+)\s*$`)
	importModuleRe = regexp.MustCompile(`(?im)^\s*(?:Import-Module|ipmo)\s+(.*)$`)
)

func regexFallback(entry, dir string) (PSScanResult, error) {
	src, err := os.ReadFile(entry)
	if err != nil {
		return PSScanResult{}, fmt.Errorf("reading entry for degraded scan: %w", err)
	}
	text := string(src)

	names := map[string]bool{}
	var order []string
	add := func(raw string) {
		name := excludeAndMap(raw, dir)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if !names[key] {
			names[key] = true
			order = append(order, name)
		}
	}

	for _, m := range requiresRe.FindAllStringSubmatch(text, -1) {
		rest := m[1]
		if strings.HasPrefix(strings.TrimSpace(rest), "@") {
			continue // hashtable/array literal form — versioned, not a "bare name"
		}
		for _, part := range strings.Split(rest, ",") {
			add(unquote(strings.TrimSpace(part)))
		}
	}
	for _, m := range usingModuleRe.FindAllStringSubmatch(text, -1) {
		add(unquote(m[1]))
	}
	for _, m := range importModuleRe.FindAllStringSubmatch(text, -1) {
		add(firstBareToken(m[1]))
	}

	sort.Strings(order)
	deps := make([]Dep, len(order))
	for i, n := range order {
		deps[i] = Dep{Name: n, Display: n}
	}

	return PSScanResult{
		Deps:     deps,
		Missing:  nil,
		Params:   nil,
		Degraded: true,
		Warning:  degradedWarning,
	}, nil
}

// firstBareToken extracts an Import-Module/ipmo call's first bare (simple
// literal) module name from the text following the command name: `-Name X`
// skips to X; any other leading `-flag` means no simple bare name to grab
// (this is the degraded path's small, deliberately non-AST filter — see
// ruling 2); a bare literal name is returned as-is.
func firstBareToken(rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	i := 0
	if strings.EqualFold(fields[i], "-Name") {
		i++
	}
	if i >= len(fields) {
		return ""
	}
	tok := fields[i]
	if strings.HasPrefix(tok, "-") {
		return ""
	}
	return unquote(strings.TrimRight(tok, ","))
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// builtinModules and nameMap mirror src/Deps.psm1's $script:BuiltinModules
// and $script:ModuleNameMap verbatim — the degraded path's small acceptable
// Go-side copy (ruling 2); the primary pwsh path never uses these, it runs
// the real thing via scanner.ps1.
var builtinModules = map[string]bool{
	"microsoft.powershell.archive":       true,
	"microsoft.powershell.core":          true,
	"microsoft.powershell.diagnostics":   true,
	"microsoft.powershell.host":          true,
	"microsoft.powershell.management":    true,
	"microsoft.powershell.security":      true,
	"microsoft.powershell.utility":       true,
	"microsoft.powershell.psresourceget": true,
	"psreadline":                         true,
	"packagemanagement":                  true,
	"powershellget":                      true,
	"threadjob":                          true,
	"cimcmdlets":                         true,
	"psdiagnostics":                      true,
	"microsoft.wsman.management":         true,
}

var nameMap = map[string]string{
	"pester":        "Pester",
	"az":            "Az",
	"awstools":      "AWS.Tools.Common",
	"awspowershell": "AWSPowerShell.NetCore",
	"sqlps":         "SqlServer",
}

// excludeAndMap applies the builtin + local-path exclusions and the gallery
// name map to one raw regex-captured name, or returns "" to drop it.
func excludeAndMap(name, dir string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.ContainsAny(name, `/\`) {
		return ""
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".psm1") || strings.HasSuffix(lower, ".psd1") || strings.HasSuffix(lower, ".dll") {
		return ""
	}
	if builtinModules[lower] {
		return ""
	}
	if dir != "" {
		if _, err := os.Stat(filepath.Join(dir, name+".psm1")); err == nil {
			return ""
		}
	}
	if mapped, ok := nameMap[lower]; ok {
		return mapped
	}
	return name
}
