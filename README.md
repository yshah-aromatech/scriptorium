# Scriptorium

A terminal UI — a single static Go binary, zero required runtime dependencies — for running **PowerShell and Python** scripts on an Ubuntu server. Scripts live in one or more private GitHub repos, each script gets its own isolated environment (a module directory for PowerShell, a venv for Python), and every run is reported to an n8n webhook with logs and resource usage.

Styled with the [Night Owl (dark)](https://terminalcolors.com/themes/night-owl/dark/) color scheme by default; Catppuccin, Gruvbox and Tokyo Night ship alongside it, plus a `terminal` palette that inherits your terminal's own colors and any of the 340+ schemes from [bubbletint](https://github.com/lrstanley/bubbletint) — see [Themes](#themes).

## Features

- **Fleet / Run / History / Schedules views** — a home screen showing every script's status at a glance, a two-pane run view (script list + live output), full-width run history, and a schedules agenda — switch with `1`-`4` or the command palette (`ctrl+p` / `:`)
- **Rounded panels + 60 fps animations** (v1.1.0) — every pane, card and modal sits in a rounded frame with its title inset in the top border and its own keys inset in the bottom one; braille sparklines carry twice the history per column; one coalesced 16 ms clock drives the spinner, the marquee, a sub-cell-smooth ETA bar, per-frame status fades and a breathing live-activity title — and disarms itself entirely when nothing moves, so an idle session costs zero timers. Narrow terminals (under 100 columns) keep the denser rule-based layout; screenshots of the new look are coming with the release notes
- **Two runtimes, one pipeline** — folders with a `.ps1` entry run under `pwsh` with a per-script module dir prepended to `PSModulePath`; folders with a `.py` entry run under a per-script venv (created automatically, cwd = script folder). Locks, logs, history, secret redaction, timeouts and the webhook are identical for both
- **Multiple script repos** — the `repos` config key syncs any number of repos side by side; the legacy single `scriptsRepo` key keeps working unchanged
- **Automatic dependency detection** — no manifest needed. PowerShell scripts are scanned with a real AST (via `pwsh`, which the installer sets up — see [PowerShell 7](#powershell-7) below) or a degraded regex fallback otherwise; missing modules install from the PowerShell Gallery. Python imports are scanned inside the script's venv; missing packages are pip-installed, with a `requirements.txt` taking precedence when present
- **Live output** — streamed into the TUI (word-wrapped, wide-character aware, mouse-scrollable), saved to a timestamped log file per run; `y` copies the buffer to your clipboard (OSC 52, tmux-aware)
- **Resource monitoring** — CPU % and RSS memory sampled across the whole process tree every second via `/proc`; average and peak reported, with a per-run sparkline in history
- **n8n webhook reporting** — success/failure, exit code, duration, avg/max CPU & memory, host, and a log tail POSTed after every run. Delivery is retried, and undelivered reports are queued on disk and re-sent after the next successful delivery
- **Cron scheduling** — `e`/`Enter` in the Schedules view sets a cron expression or plain English on any script; schedules live in a managed block in your user crontab that leaves every other entry alone
- **Overlap protection + missed-run detection** — a per-script lock prevents concurrent runs of the same script (the loser is reported `skipped`); a schedule that silently stops firing gets a red badge and a one-time `missed` webhook
- **MCP server + REST API** — a built-in [MCP](https://modelcontextprotocol.io) server and a co-hosted REST surface so an AI agent (e.g. an n8n AI Agent node) can list/run scripts and manage schedules — see [MCP server](#mcp-server-ai-agents--n8n) below
- **Self-update** — `U` in the TUI updates the app in place (binary self-update for a released build, `git pull` for a source checkout); a released build also gets a startup notice when a newer release exists

## Keybindings (Run view)

| Key | Action |
| --- | --- |
| `↑`/`↓`/`j`/`k`, `g`/`G` | navigate / jump to top / bottom |
| `Enter` / `r` | run the selected script (deps checked first; queued if something is already running) |
| `a` | run with extra arguments (quote-aware) |
| `e` | edit the script's `.env` file |
| `v` | view the script's last run log |
| `s` | sync scripts repos |
| `i` | scan + install missing dependencies |
| `l` | lint the script |
| `u` | update PowerShell + Python (apt) and every module dir / venv |
| `U` | self-update this app (see [Updating](#updating)) |
| `h` | this script's run history |
| `t` | send a test event to the n8n webhook |
| `x` / `X` | kill the running script / clear the queue |
| `y` / `c` | copy output / clear the output pane |
| `/` | filter the script list |
| `ctrl+f`, `n`/`N` | search the output pane, jump matches |
| `1`-`4` | Fleet / Run / History / Schedules |
| `ctrl+p` / `:` | command palette |
| `[` / `]` | cycle the theme (session-only; the status line says how to keep one) |
| `?` | help overlay |
| `q` / `ctrl+c` | quit |

Mouse: wheel scrolls the hovered pane; click focuses/selects; drag over the output pane selects and copies text.

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/yshah-aromatech/scriptorium/main/install.sh | bash
```

This downloads the latest release for your architecture (linux amd64/arm64), verifies its checksum, and installs it to `~/.local/bin/scriptorium`, creating `config.json` + `.env` from the examples in `~/scriptorium` (override with `SCRIPTORIUM_APP_DIR`). On apt systems it also installs the runtime prerequisites — PowerShell 7 via the Microsoft repo, and python3 + pip + venv — escalating via sudo where it has to and downgrading each to a warning with the exact manual command where it can't. It adds `~/.local/bin` to your shell rc (marker-guarded, never duplicated) when it isn't on your PATH. Re-running the same one-liner later is the updater: it verifies and replaces the binary and prints `updated scriptorium vOLD → vNEW`. Prefer to build from source instead:

```bash
git clone https://github.com/yshah-aromatech/scriptorium.git && cd scriptorium && ./install.sh
```

(needs a Go toolchain — `go build ./cmd/scriptorium` under the hood; this mode also keeps the checkout tracking the repo on every re-run).

Then:

1. If your *scripts* repo is private, create a fine-grained PAT for it (github.com → Settings → Developer settings → Fine-grained tokens) with **Contents: Read-only** on that repo.
2. Configure `config.json` (`scriptsRepo`, `n8nWebhookUrl`) and `.env` (`GITHUB_TOKEN` if the scripts repo is private).
3. Run: `scriptorium`

### PowerShell 7

The app binary itself has no PowerShell dependency — only *running PowerShell scripts* needs `pwsh` on the machine, same as needing Python for Python scripts. Without `pwsh`, PowerShell scripts still run, just with a degraded (regex-based) dependency scan instead of the real AST scan. Since v1.1.0, install.sh installs `pwsh` for you on apt systems (via the Microsoft repo, matched to your `/etc/os-release` release); with no usable sudo it prints the manual command instead:

```bash
# what install.sh runs for you — adjust the ubuntu/24.04 path for your release
curl -fsSL https://packages.microsoft.com/config/ubuntu/24.04/packages-microsoft-prod.deb -o /tmp/packages-microsoft-prod.deb
sudo dpkg -i /tmp/packages-microsoft-prod.deb && sudo apt-get update && sudo apt-get install -y powershell
```

## Updating

- **`U` in the TUI** updates the app in place: a released binary downloads and installs the latest GitHub release over itself; a source checkout runs `git pull --ff-only` instead. Either way, restart `scriptorium` to run the new code. A released build also shows a startup notice (`update available: vX.Y.Z — press U`) when one is available — a source checkout has no release version to compare against, so it skips the check entirely.
- **Re-running `install.sh`** is always safe (it never touches `config.json`/`.env`), prints `updated scriptorium vOLD → vNEW` (or "already current") after verifying the download, and is the only path that also installs new prerequisites — worth doing occasionally even if `U` looks up to date.
- `scriptorium --version` prints the running build.

## Scripts repo layout

One folder per script; PowerShell and Python folders can live in the same repo or in separate repos:

```
your-scripts-repo/
├── backup-db/
│   ├── main.ps1        # PowerShell entry point
│   └── script.json     # optional: {"entry": "...", "description": "...", "args": ["-Flag"], "timeoutMinutes": 30}
└── pull-metrics/
    ├── main.py         # Python entry point — runs in its own venv
    └── requirements.txt
```

The entry point resolves in this order: `script.json`'s `"entry"`; then `main.ps1`/`<folder>.ps1`/`run.ps1`; then `main.py`/`<folder>.py`/`run.py`/`__main__.py`; then the sole (or first alphabetical) `.ps1`, else `.py`, in the folder.

### Multiple repos

```bash
scriptorium --add-repo https://github.com/YOUR_ORG/python-scripts --name python
scriptorium --sync
```

`scriptorium --repos` lists what's configured, or edit `repos` in `config.json` directly:

```json
"repos": [
  { "name": "powershell", "url": "https://github.com/YOUR_ORG/powershell-scripts" },
  { "name": "python",     "url": "https://github.com/YOUR_ORG/python-scripts", "branch": "main" }
]
```

Script names must be unique across repos; a folder name appearing in more than one repo is qualified as `<repoName>-<folder>` everywhere.

## Headless mode

| Command | Effect |
| --- | --- |
| `scriptorium --run <script> [--args "..."] [--cron]` | run one script through the full pipeline (exit: 0 success, 1 failure, 3 skipped) |
| `scriptorium --list` / `--history [script]` | list scripts with status/schedule, or print recent runs |
| `scriptorium --sync` | sync all scripts repos and exit |
| `scriptorium --version` / `--help` | print the build / usage |

## Per-script .env files

Each script folder can have a `.env` file (`KEY=VALUE`, `#` comments) — press `e` in the Run view to edit it in place. Every value (8+ characters) is treated as a secret and redacted (`***`) in TUI output, log files, and webhook payloads. Keep `.env` gitignored in the scripts repo; a `.env.example` there pre-fills the editor when no `.env` exists yet.

## n8n webhook payload

POSTed as JSON after every run (`{"event":"script_run", ...}`; `{"event":"test"}` for `t`; `{"event":"missed"}` for a missed schedule):

```json
{
  "event": "script_run", "script": "backup-db", "runtime": "powershell", "trigger": "manual",
  "status": "success", "success": true, "exitCode": 0,
  "startedAt": "2026-06-10T12:00:00.000Z", "durationSec": 204.1, "host": "ubuntu-vm-01",
  "resources": { "cpuAvgPercent": 23.4, "cpuMaxPercent": 87.1, "memAvgMb": 145.2, "memMaxMb": 312.8 },
  "logFile": "/home/user/.scriptorium/logs/backup-db-2026-06-10T12-00-00-000Z.log",
  "log": "...last 64KB of output..."
}
```

`status` is one of `success`, `failure`, `killed`, `timeout`, `skipped`. Delivery is retried once (2 attempts total); a payload that still fails is queued on disk and re-sent after the next successful delivery.

## MCP server (AI agents / n8n)

`scriptorium --mcp` starts an MCP server (streamable-HTTP, `POST /mcp`, `GET /healthz`) plus a co-hosted REST API under `/api/v1/*` — same listener, same Bearer token, same 1MB body cap. Both surfaces call the identical tool implementations.

**Tools:** `list_scripts`, `get_script_details`, `run_script`, `get_history`, `get_run_log`, `sync_repos`, `get_schedules`/`set_schedule`/`remove_schedule`, `install_deps`, `update_app`, `update_packages`.

Setup:

1. `MCP_AUTH_TOKEN=$(openssl rand -hex 32)` in `.env` — the server refuses to start without one.
2. `scriptorium --install-mcp-service` installs it as a systemd service (root → system unit; non-root → user unit + lingering, so it survives logout/reboot). `scriptorium --mcp` runs it in the foreground instead.
3. In n8n: an **AI Agent** node with an **MCP Client Tool** sub-node, Endpoint `http://<server-ip>:8765/mcp`, Server Transport **HTTP Streamable**, Bearer credential holding the token.

MCP/API-triggered runs go through the same pipeline as manual/cron runs (lock, dep install, log, history, webhook with `"trigger": "mcp"`).

## Themes

Set `theme` in `config.json`:

- **Curated palettes** (hand-tuned, contrast-audited: body text ≥ 7:1, secondary text ≥ 4.5:1, borders ≥ 3:1): `night-owl` (the default), `catppuccin-mocha`, `gruvbox-dark`, `tokyo-night`.
- **`terminal`** — no palette at all: the UI maps its roles onto ANSI colors 0-15 and your terminal's default foreground/background, so it inherits whatever scheme your terminal already uses (light terminals included). Uniquely, this palette paints no background of its own.
- **Any of 340+ [bubbletint](https://github.com/lrstanley/bubbletint) scheme IDs** — `dracula`, `rose_pine`, `nord`, `solarized_dark_higher_contrast`, … Both `snake_case` and `kebab-case` spellings work. Borders and card tints are derived, and low-contrast secondary text is automatically lifted to the same floors the curated palettes meet; light schemes are supported.

Lookup is curated-first (`tokyo_night` gets the curated palette, not the raw tint). A misspelled name falls back to Night Owl with a startup warning naming the three closest matches.

Press `]` / `[` in any view to cycle the whole set live — curated palettes first, then every tint. Cycling is session-only; the status line shows each theme's name and the exact `"theme": "…"` line to add to `config.json` to keep it. Both commands are also in the palette (`:` then type `theme`).

## Configuration reference (config.json)

| Key | Description | Default |
| --- | --- | --- |
| `scriptsRepo` | HTTPS URL of the private scripts repo (or `SCRIPTS_REPO` in `.env`) | — |
| `repos` | array of `{name, url, branch}` scripts repos (overrides `scriptsRepo`) | `[]` |
| `dataDir` | where scripts/module dirs/logs/history live | `~/.scriptorium` |
| `n8nWebhookUrl` | n8n webhook endpoint (or `N8N_WEBHOOK_URL` in `.env`) | — |
| `pwshBin` / `pythonBin` | interpreters used to run scripts | `pwsh` / `python3` |
| `runTimeoutMinutes` | kill runs longer than this (0 = no limit; `script.json`'s `timeoutMinutes` overrides) | `0` |
| `openRouterModel` | model for plain-English → cron (`OPENROUTER_API_KEY` in `.env`) | `google/gemini-3.1-flash-lite` |
| `logRetentionDays` / `historyDays` / `historyMaxLines` | log/history retention | `30` / `30` / `50000` |
| `missedGraceMinutes` | how late a scheduled fire may be before it's reported missed | `5` |
| `colorMode` | `auto`, `truecolor`, or `256` | `auto` |
| `theme` | a curated palette, `terminal`, or any bubbletint ID — see [Themes](#themes) | `night-owl` |
| `mcpPort` / `mcpBind` | MCP/API server port and bind (`all` or `localhost`) | `8765` / `all` |

See `config.json.example` for the full set. Unknown keys and bad values for numeric keys are reported as warnings at startup, never silently ignored.

## Troubleshooting

- **clone/fetch fails** — check `GITHUB_TOKEN` in `.env` (needs Contents: Read on the scripts repo); rerun sync (`s`) after rotating it.
- **webhook not firing** — press `t` to send a test event; check the n8n workflow is active and the URL is the production one.
- **copy does nothing over SSH/tmux** — add `set -g allow-passthrough on` to `~/.tmux.conf` (OSC 52 copies are capped at 72KB).
- **colors look gray/washed-out under tmux** — tmux often advertises only 256 colors; the UI then uses a harmonized indexed palette. For the true theme, let tmux pass truecolor through: `set -ga terminal-overrides ",*:Tc"` (tmux ≥3.2: `set -as terminal-features ",*:RGB"`) in `~/.tmux.conf`.
- **a run is stuck "skipped"** — a stale lock under `~/.scriptorium/locks/` is reclaimed automatically once its owning PID is dead; delete the file manually to force it sooner.

## Development

```bash
go build ./... && go vet ./... && go test -race ./...
```

`hack/gen-fixtures.ps1` regenerates the PS-parity fixtures under `testdata/psfixtures/`.

## License

[AGPL-3.0](LICENSE)
