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
	"io/fs"
	"math"
	"os"
	"path/filepath"
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

	return cfg, paths, warnings, nil
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
		if def, ok := numericDefaults[key]; ok {
			num, ok := jsonNumber(raw)
			if !ok {
				warnings = append(warnings, fmt.Sprintf("config.json: '%s' must be a number, got '%s' — using default %s", key, rawDisplay(raw), formatDefault(def)))
				continue
			}
			assignNumeric(cfg, key, num)
			continue
		}
		if !knownNonNumericKeys[key] {
			warnings = append(warnings, fmt.Sprintf("config.json: unknown key '%s' — ignored (typo?)", key))
			continue
		}
		assignNonNumeric(cfg, key, raw)
	}
	return warnings, nil
}

// orderedObject decodes a JSON object's top-level keys and raw values in
// document order.
func orderedObject(data []byte) (keys []string, raws []json.RawMessage, err error) {
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
	return keys, raws, nil
}

// jsonNumber reports whether raw is a JSON number literal, or a JSON
// string whose contents parse as one (PS's `-as [double]` cast accepts
// both — an upgrading user's quoted "9443" must still bind), and its value.
func jsonNumber(raw json.RawMessage) (float64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return 0, false
	}
	switch v := tok.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
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

func assignNumeric(cfg *Config, key string, v float64) {
	switch key {
	case "monitorIntervalMs":
		cfg.MonitorIntervalMs = int(v)
	case "logTailKb":
		cfg.LogTailKb = int(v)
	case "runTimeoutMinutes":
		cfg.RunTimeoutMinutes = v
	case "maxOutputLines":
		cfg.MaxOutputLines = int(v)
	case "logRetentionDays":
		cfg.LogRetentionDays = v
	case "historyMaxLines":
		cfg.HistoryMaxLines = int(v)
	case "historyDays":
		cfg.HistoryDays = v
	case "webhookTimeoutSec":
		cfg.WebhookTimeoutSec = int(v)
	case "missedGraceMinutes":
		cfg.MissedGraceMinutes = v
	case "mcpPort":
		cfg.McpPort = int(v)
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
		var r []RepoEntry
		if json.Unmarshal(raw, &r) == nil {
			cfg.Repos = r
		}
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
