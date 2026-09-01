package config

import "path/filepath"

// Paths holds every filesystem location the app derives from dataDir.
type Paths struct {
	AppDir           string
	DataDir          string
	ScriptsDir       string
	ModulesDir       string
	LogsDir          string
	LocksDir         string
	VenvsDir         string
	ToolsDir         string
	HistoryFile      string
	WebhookQueueFile string
}

// newPaths derives every path from the app dir and the resolved (already
// tilde-expanded) data dir.
func newPaths(appDir, dataDir string) Paths {
	return Paths{
		AppDir:           appDir,
		DataDir:          dataDir,
		ScriptsDir:       filepath.Join(dataDir, "scripts"),
		ModulesDir:       filepath.Join(dataDir, "modules"),
		LogsDir:          filepath.Join(dataDir, "logs"),
		LocksDir:         filepath.Join(dataDir, "locks"),
		VenvsDir:         filepath.Join(dataDir, "venvs"),
		ToolsDir:         filepath.Join(dataDir, "tools"),
		HistoryFile:      filepath.Join(dataDir, "history.jsonl"),
		WebhookQueueFile: filepath.Join(dataDir, "webhook-queue.jsonl"),
	}
}
