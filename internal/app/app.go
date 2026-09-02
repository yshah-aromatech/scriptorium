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
	"github.com/yshah-aromatech/scriptorium/internal/cron"
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
	Cron     *cron.Crontab
}

// Open loads config.json and the app .env, wires the stores and the webhook
// client, and runs the best-effort startup prune. Warnings collects every
// non-fatal problem found along the way (config.json key/type warnings, the
// legacy data-dir migration note); the caller prints them, it never fails
// the open.
func Open(appDir string) (*App, error) { return OpenWith(appDir, nil) }

// OpenWith is Open with the crontab command injected — a nil runner means the
// real binary. Only the RUNNER is injectable, not a whole cron.Crontab: a
// caller handing in a Crontab whose Run happened to be nil would silently
// talk to the user's real crontab, and no test may ever do that.
func OpenWith(appDir string, crontabRun cron.CrontabRunner) (*App, error) {
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

	// The managed crontab block. BinPath is what gets written into every
	// scheduled line, so it must be this executable — a plain "scriptorium"
	// (resolved through the cron daemon's PATH) is the fallback when the
	// kernel won't tell us our own path.
	binPath := "scriptorium"
	if exe, eerr := os.Executable(); eerr == nil {
		binPath = exe
	}
	ct := &cron.Crontab{
		AppDir:  paths.AppDir,
		LogsDir: paths.LogsDir,
		BinPath: binPath,
		Run:     crontabRun,
	}

	// Best-effort startup prune, matching Initialize-Sto's Clear-StoOldData
	// call. Real schedules now, so the frequent-success rule is live: a
	// script cron-scheduled every <=10 minutes keeps its success rows for a
	// day instead of the full history window.
	_ = retention.Prune(retention.Options{
		DataDir:          paths.DataDir,
		LogsDir:          paths.LogsDir,
		HistoryFile:      paths.HistoryFile,
		LogRetentionDays: cfg.LogRetentionDays,
		HistoryDays:      cfg.HistoryDays,
		HistoryMaxLines:  cfg.HistoryMaxLines,
		Schedules:        ct.Schedules(),
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
		Cron:     ct,
	}, nil
}
