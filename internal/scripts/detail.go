package scripts

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yshah-aromatech/scriptorium/internal/envfile"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

// readmeCap is 16KB — Get-StoScriptDetail's README truncation threshold.
const readmeCap = 16 * 1024

// Detail is the MCP-facing script detail — everything an agent needs to
// call a script, minus PowerShell param-block parsing (P8/P9's concern).
type Detail struct {
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Runtime        string             `json:"runtime"`
	Repo           string             `json:"repo"`
	Entry          string             `json:"entry"`
	TimeoutMinutes *float64           `json:"timeoutMinutes"`
	DefaultArgs    []string           `json:"defaultArgs"`
	Readme         string             `json:"readme"`
	EnvExample     []envfile.DocEntry `json:"envExample"`
	EnvConfigured  []string           `json:"envConfigured"`
}

// GetDetail is the port of Get-StoScriptDetail's non-parameter fields:
// readme (case-insensitive readme.md in s.Dir, skipped for a loose script,
// capped at 16KB with a truncation marker, redacted), the documented
// .env.example keys, and the .env's configured key names.
func GetDetail(s Script, reg *secret.Registry) Detail {
	var readme string
	if !s.Loose {
		if f := findReadme(s.Dir); f != "" {
			if data, err := os.ReadFile(f); err == nil {
				readme = string(data)
				if len(readme) > readmeCap {
					readme = readme[:readmeCap] + "\n[truncated]"
				}
				readme = reg.Redact(readme)
			}
		}
	}

	envExample, _ := envfile.ReadDoc(s.EnvExample)
	if envExample == nil {
		envExample = []envfile.DocEntry{}
	}
	envConfigured, _ := envfile.Keys(s.EnvFile)
	if envConfigured == nil {
		envConfigured = []string{}
	}
	defaultArgs := s.Args
	if defaultArgs == nil {
		defaultArgs = []string{}
	}

	return Detail{
		Name:           s.Name,
		Description:    s.Description,
		Runtime:        s.Runtime,
		Repo:           s.Repo,
		Entry:          filepath.Base(s.Entry),
		TimeoutMinutes: s.TimeoutMinutes,
		DefaultArgs:    defaultArgs,
		Readme:         readme,
		EnvExample:     envExample,
		EnvConfigured:  envConfigured,
	}
}

// findReadme returns the full path of a case-insensitive "readme.md" in
// dir, or "" when none exists.
func findReadme(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), "readme.md") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}
