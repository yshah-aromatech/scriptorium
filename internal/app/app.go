// Package app is the process-wide facade every frontend (cli today, tui and
// mcp later) opens once at startup: it loads config and the .env secrets,
// wires the stores and clients every other package needs, and runs the
// best-effort startup prune. Port of Initialize-Sto (src/Core.psm1).
package app

import (
	"fmt"
	"os"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/lockfile"
	"github.com/yshah-aromatech/scriptorium/internal/retention"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

// App is everything a headless run or a UI frame needs, opened once.
type App struct {
	Cfg      *config.Config
	Paths    config.Paths
	Warnings []string
	Sec      *secret.Registry
	Locks    *lockfile.Dir
	Hist     *history.Store
	Hook     *webhook.Client
	Runner   *runner.Runner
}

// Open loads config.json and the app .env, wires the stores and the webhook
// client, and runs the best-effort startup prune. Warnings collects every
// non-fatal problem found along the way (config.json key/type warnings, the
// legacy data-dir migration note); the caller prints them, it never fails
// the open.
func Open(appDir string) (*App, error) {
	cfg, paths, warnings, err := config.Load(appDir)
	if err != nil {
		return nil, err
	}

	sec := secret.NewRegistry()
	if err := config.LoadAppEnv(appDir, sec); err != nil {
		return nil, fmt.Errorf("loading .env: %w", err)
	}

	locks := lockfile.NewDir(paths.LocksDir)
	hist := history.NewStore(paths.HistoryFile)

	// env wins over config (Send-StoWebhook, src/Runner.psm1) — LoadAppEnv
	// already applied the .env file to the process environment (an
	// already-set variable wins), so os.Getenv alone captures both sources.
	url := os.Getenv("N8N_WEBHOOK_URL")
	if url == "" {
		url = cfg.N8nWebhookURL
	}
	hook := webhook.NewClient(url, time.Duration(cfg.WebhookTimeoutSec)*time.Second, paths.WebhookQueueFile)

	r := &runner.Runner{
		Cfg:   cfg,
		Paths: paths,
		Locks: locks,
		Hist:  hist,
		Hook:  hook,
		Sec:   sec,
	}

	// Best-effort startup prune, matching Initialize-Sto's Clear-StoOldData
	// call. Schedules is nil: P6 has no crontab reader yet, so the
	// frequent-success retention rule (FrequentScripts) is inert until P7
	// wires one — every other rule (the history-days window, the log-age
	// sweep, the newest-row-survives guarantee) still applies in full.
	// Deviation ledgered: the still-running PS app prunes with real
	// schedules on the same shared data dir during the migration window, so
	// nothing is lost — only the Go binary's own frequent-success pruning is
	// deferred to P7.
	_ = retention.Prune(retention.Options{
		DataDir:          paths.DataDir,
		LogsDir:          paths.LogsDir,
		HistoryFile:      paths.HistoryFile,
		LogRetentionDays: cfg.LogRetentionDays,
		HistoryDays:      cfg.HistoryDays,
		HistoryMaxLines:  cfg.HistoryMaxLines,
		Schedules:        nil,
	}, false)

	return &App{
		Cfg:      cfg,
		Paths:    paths,
		Warnings: warnings,
		Sec:      sec,
		Locks:    locks,
		Hist:     hist,
		Hook:     hook,
		Runner:   r,
	}, nil
}
