// Package config loads config.json with the exact key set, defaults,
// warning strings, and path layout of the PowerShell app (src/Core.psm1
// Initialize-Sto). Unknown keys and non-numeric values for numeric keys
// warn (byte-identical strings) instead of failing; invalid JSON is a
// hard error. Load also performs the one-time default-dataDir migration
// (~/.psscripts -> ~/.scriptorium) and creates the data directories.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/yshah-aromatech/scriptorium/internal/envfile"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

// RepoEntry is one raw entry of the multi-repo "repos" config array.
// Normalization to repo roots is a later phase's concern.
type RepoEntry struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

// decodeRepos parses the "repos" config value the way PS's `@($cfg.repos)`
// does: a bare JSON object wraps to a one-element array (mirroring
// PowerShell's array-subexpression operator on a non-array). Each element
// is then decoded independently; an element whose shape/types don't fit
// RepoEntry fails to decode and is silently dropped AT THIS LAYER — the
// per-entry warnings PS emits for a missing url or a bad name pattern are a
// later phase's concern (Get-StoRepos normalization), not this decode step.
func decodeRepos(raw json.RawMessage) []RepoEntry {
	// a literal JSON null is PS's `@($cfg.repos)` on $null: the
	// array-subexpression operator wraps $null into a ONE-element array
	// (verified against pwsh), not zero — so it must NOT collapse to the
	// same empty-slice shape as "repos": [] (which is genuinely zero
	// entries and, in scripts.Repos, falls back to the legacy single repo).
	// A single zero-value RepoEntry reproduces both effects: it's counted
	// as one entry (matching PS's Count==1, so Repos() must not treat it as
	// "no repos configured"), and its blank URL/Name warn/skip exactly like
	// stringifying null's .url/.name in PS.
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []RepoEntry{{}}
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		// not an array — check whether it's a bare object to wrap
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		elems = []json.RawMessage{raw}
	}
	var out []RepoEntry
	for _, e := range elems {
		var entry RepoEntry
		if json.Unmarshal(e, &entry) != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Config is the fully-defaulted, parsed config.json.
type Config struct {
	ScriptsRepo        string
	Branch             string
	Repos              []RepoEntry
	PythonBin          string
	DataDir            string
	N8nWebhookURL      string
	PwshBin            string
	MonitorIntervalMs  int
	LogTailKb          int
	RunTimeoutMinutes  float64
	MaxOutputLines     int
	OpenRouterModel    string
	SyncOnLaunch       bool
	LogRetentionDays   float64
	HistoryMaxLines    int
	HistoryDays        float64
	WebhookTimeoutSec  int
	MissedGraceMinutes float64
	ColorMode          string
	McpPort            int
	McpBind            string
}

const defaultDataDir = "~/.scriptorium"

// numericDefaults holds both the numeric-gated key set and the default
// value used for out-of-range warnings — keys not present here are
// treated as non-numeric.
var numericDefaults = map[string]float64{
	"monitorIntervalMs":  1000,
	"logTailKb":          64,
	"runTimeoutMinutes":  0,
	"maxOutputLines":     5000,
	"logRetentionDays":   30,
	"historyMaxLines":    50000,
	"historyDays":        30,
	"webhookTimeoutSec":  15,
	"missedGraceMinutes": 5,
	"mcpPort":            8765,
}

// knownNonNumericKeys is every valid config.json key that is not
// numeric-gated.
var knownNonNumericKeys = map[string]bool{
	"scriptsRepo":     true,
	"branch":          true,
	"repos":           true,
	"pythonBin":       true,
	"dataDir":         true,
	"n8nWebhookUrl":   true,
	"pwshBin":         true,
	"openRouterModel": true,
	"syncOnLaunch":    true,
	"colorMode":       true,
	"mcpBind":         true,
}

// numericKeyByLower and nonNumericKeyByLower map a lower-cased key name to
// its canonical spelling. PS's config is an [ordered] hashtable, whose key
// lookup (`$cfg.Contains`, `$cfg[...]`) is case-insensitive by default, so
// e.g. "DataDir" must bind to the canonical "dataDir" field. Duplicate keys
// differing only by case: applied in document order, so the last one wins.
var numericKeyByLower = lowerKeys(numericDefaults)
var nonNumericKeyByLower = lowerKeys(knownNonNumericKeys)

func lowerKeys[V any](m map[string]V) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[strings.ToLower(k)] = k
	}
	return out
}

func defaultConfig() *Config {
	return &Config{
		Branch:             "main",
		PythonBin:          "python3",
		DataDir:            defaultDataDir,
		PwshBin:            "pwsh",
		MonitorIntervalMs:  int(numericDefaults["monitorIntervalMs"]),
		LogTailKb:          int(numericDefaults["logTailKb"]),
		RunTimeoutMinutes:  numericDefaults["runTimeoutMinutes"],
		MaxOutputLines:     int(numericDefaults["maxOutputLines"]),
		OpenRouterModel:    "google/gemini-3.1-flash-lite",
		LogRetentionDays:   numericDefaults["logRetentionDays"],
		HistoryMaxLines:    int(numericDefaults["historyMaxLines"]),
		HistoryDays:        numericDefaults["historyDays"],
		WebhookTimeoutSec:  int(numericDefaults["webhookTimeoutSec"]),
		MissedGraceMinutes: numericDefaults["missedGraceMinutes"],
		ColorMode:          "auto",
		McpPort:            int(numericDefaults["mcpPort"]),
		McpBind:            "all",
	}
}

// Load parses <appDir>/config.json (a missing file is not an error — the
// defaults apply), resolves paths, performs the one-time default-dataDir
// migration, and creates the data directories.
func Load(appDir string) (*Config, Paths, []string, error) {
	cfg := defaultConfig()
	var warnings []string

	data, err := os.ReadFile(filepath.Join(appDir, "config.json"))
	switch {
	case err == nil:
		warns, perr := applyConfigJSON(cfg, data)
		if perr != nil {
			return nil, Paths{}, nil, fmt.Errorf("config.json is not valid JSON: %v", perr)
		}
		warnings = append(warnings, warns...)
	case errors.Is(err, fs.ErrNotExist):
		// no config.json — defaults apply
	default:
		return nil, Paths{}, nil, err
	}

	home := os.Getenv("HOME")
	dataDir := cfg.DataDir
	if strings.HasPrefix(dataDir, "~") {
		dataDir = home + dataDir[1:]
	}

	// one-time migration from the pre-rename data dir (~/.psscripts). Only
	// when dataDir is the default — an explicit dataDir is never
	// second-guessed.
	if cfg.DataDir == defaultDataDir {
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			legacy := filepath.Join(home, ".psscripts")
			if _, err := os.Stat(legacy); err == nil {
				if err := os.Rename(legacy, dataDir); err != nil {
					warnings = append(warnings, fmt.Sprintf("could not migrate %s to %s: %v — using the new (empty) dir", legacy, dataDir, err))
				} else {
					warnings = append(warnings, fmt.Sprintf("migrated data dir: %s -> %s", legacy, dataDir))
				}
			}
		}
	}

	paths := newPaths(appDir, dataDir)
	for _, d := range []string{paths.DataDir, paths.ModulesDir, paths.LogsDir, paths.LocksDir, paths.VenvsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, Paths{}, warnings, err
		}
	}

	// repos entry sanity (PS order: key warnings -> migration -> repos).
	// Advisory only — normalization/skipping of bad entries is
	// scripts.Repos' job, run lazily so env overrides loaded above apply.
	warnings = append(warnings, reposEntryWarnings(cfg.Repos)...)

	return cfg, paths, warnings, nil
}

// repoNamePattern is the valid repos-entry name shape, shared by the
// config-load warning below and scripts.Repos/AddRepoConfig normalization.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// reposEntryWarnings ports Initialize-Sto's repos-sanity loop: each entry
// (in array order) can warn for a missing url, a malformed name, or both —
// url is checked before name, independently, matching PS's two separate
// `if` statements.
func reposEntryWarnings(repos []RepoEntry) []string {
	var warnings []string
	for _, r := range repos {
		if r.URL == "" {
			warnings = append(warnings, "config.json: repos entry missing 'url' — skipped")
		}
		if r.Name != "" && !repoNamePattern.MatchString(r.Name) {
			warnings = append(warnings, fmt.Sprintf("config.json: repos entry name '%s' must match [A-Za-z0-9_-]+ — skipped", r.Name))
		}
	}
	return warnings
}

// applyConfigJSON walks data's top-level object in document order (PS
// iterates the JSON object's properties in document order, and warning
// order must match) and applies each property onto cfg.
func applyConfigJSON(cfg *Config, data []byte) ([]string, error) {
	keys, raws, err := orderedObject(data)
	if err != nil {
		return nil, err
	}
	var warnings []string
	for i, key := range keys {
		raw := raws[i]
		lower := strings.ToLower(key)
		if canon, ok := numericKeyByLower[lower]; ok {
			def := numericDefaults[canon]
			num, ok := jsonNumber(raw)
			if !ok {
				warnings = append(warnings, fmt.Sprintf("config.json: '%s' must be a number, got '%s' — using default %s", key, rawDisplay(raw), formatDefault(def)))
				continue
			}
			assignNumeric(cfg, canon, num)
			continue
		}
		canon, ok := nonNumericKeyByLower[lower]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("config.json: unknown key '%s' — ignored (typo?)", key))
			continue
		}
		assignNonNumeric(cfg, canon, raw)
	}
	return warnings, nil
}

// orderedObject decodes a JSON object's top-level keys and raw values in
// document order. A leading UTF-8 BOM is stripped first, and anything after
// the closing '}' (besides trailing whitespace) is a hard error.
func orderedObject(data []byte) (keys []string, raws []json.RawMessage, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, nil, fmt.Errorf("expected a top-level JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected a string key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		raws = append(raws, raw)
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, nil, fmt.Errorf("unexpected trailing data after top-level JSON object")
		}
		return nil, nil, err
	}
	return keys, raws, nil
}

// jsonNumber reports whether raw is a JSON number literal, or a JSON
// string whose contents parse as one (PS's `-as [double]` cast accepts
// both — an upgrading user's quoted "9443" must still bind), and its value.
// NaN/±Inf (reachable only via a quoted string like "Infinity" — a bare
// JSON number literal can't spell one) and values outside int32 range are
// treated as invalid: Go's int() conversion of such a float is
// undefined/platform-dependent, unlike PS's [int] cast (which throws at
// use) — a config warning is safer than either. This is a deliberate
// divergence from PS, which would let the value through here and only fail
// later at the point of use.
func jsonNumber(raw json.RawMessage) (float64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return 0, false
	}
	var f float64
	switch v := tok.(type) {
	case json.Number:
		f, err = v.Float64()
	case string:
		f, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return 0, false
	}
	if err != nil {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f > math.MaxInt32 || f < math.MinInt32 {
		return 0, false
	}
	return f, true
}

// rawDisplay renders raw the way PS string-interpolation would: a JSON
// string's bare contents, or the raw JSON text for any other kind.
func rawDisplay(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// formatDefault renders a numeric default the way PS does: bare integers
// for whole numbers, decimal otherwise.
func formatDefault(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// toInt matches PS's [int] cast on a double, which rounds half-to-even
// (banker's rounding) rather than truncating.
func toInt(v float64) int {
	return int(math.RoundToEven(v))
}

func assignNumeric(cfg *Config, key string, v float64) {
	switch key {
	case "monitorIntervalMs":
		cfg.MonitorIntervalMs = toInt(v)
	case "logTailKb":
		cfg.LogTailKb = toInt(v)
	case "runTimeoutMinutes":
		cfg.RunTimeoutMinutes = v
	case "maxOutputLines":
		cfg.MaxOutputLines = toInt(v)
	case "logRetentionDays":
		cfg.LogRetentionDays = v
	case "historyMaxLines":
		cfg.HistoryMaxLines = toInt(v)
	case "historyDays":
		cfg.HistoryDays = v
	case "webhookTimeoutSec":
		cfg.WebhookTimeoutSec = toInt(v)
	case "missedGraceMinutes":
		cfg.MissedGraceMinutes = v
	case "mcpPort":
		cfg.McpPort = toInt(v)
	}
}

// assignNonNumeric applies a known non-numeric key. A type mismatch (e.g.
// a number where a string is expected) is left as the default — the PS
// source performs no such validation either, and the mismatch will surface
// wherever the value is actually consumed.
func assignNonNumeric(cfg *Config, key string, raw json.RawMessage) {
	switch key {
	case "scriptsRepo":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.ScriptsRepo = s
		}
	case "branch":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.Branch = s
		}
	case "repos":
		cfg.Repos = decodeRepos(raw)
	case "pythonBin":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.PythonBin = s
		}
	case "dataDir":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.DataDir = s
		}
	case "n8nWebhookUrl":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.N8nWebhookURL = s
		}
	case "pwshBin":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.PwshBin = s
		}
	case "openRouterModel":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.OpenRouterModel = s
		}
	case "syncOnLaunch":
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			cfg.SyncOnLaunch = b
		}
	case "colorMode":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.ColorMode = s
		}
	case "mcpBind":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cfg.McpBind = s
		}
	}
}

// forceEnvNames are secrets that may arrive directly via the process
// environment rather than the app .env file.
var forceEnvNames = []string{"GITHUB_TOKEN", "OPENROUTER_API_KEY", "N8N_WEBHOOK_URL", "MCP_AUTH_TOKEN"}

// LoadAppEnv reads <appDir>/.env into the process environment (an already
// set variable wins) and registers every value through reg's name gate;
// it then force-registers the four known secret names directly from the
// process environment when set.
func LoadAppEnv(appDir string, reg *secret.Registry) error {
	vals, err := envfile.Read(filepath.Join(appDir, ".env"))
	if err != nil {
		return err
	}
	for name, value := range vals {
		if _, exists := os.LookupEnv(name); !exists {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
		}
		reg.Add(name, value, false)
	}
	for _, name := range forceEnvNames {
		if v := os.Getenv(name); v != "" {
			reg.Add(name, v, true)
		}
	}
	return nil
}
