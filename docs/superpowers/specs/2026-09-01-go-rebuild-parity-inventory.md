# Go Rebuild Parity Inventory (generated 2026-09-01)

Source reviewed in full: `README.md`, `scriptorium.ps1`, `install.sh`, `config.json.example`, `.env.example`, all of `src/{Core,Scripts,Deps,Runner,Cron,Mcp,Tui}.psm1`, all of `tests/*.Tests.ps1`, `.github/workflows/ci.yml`.

---

## 1. TUI SURFACE

### 1.1 Modes
`S.Mode` ∈ `list | deps | input | confirm | env | history | help`. Within `list`, `S.FocusPane` ∈ `list | output`.

### 1.2 Keybindings — global (every mode)
- `Ctrl+C` — quit; if a script is running, opens a confirm ("a script is running — kill it and quit?") instead of quitting immediately.

### 1.3 Keybindings — list mode (FocusPane=list)
- `↑`/`↓`, `k`/`j` — move selection ±1 (case-sensitive k/j); triggers 180ms "pulse" anim on change.
- `g` / `G` — jump to top / bottom of visible list.
- `Tab` — toggle FocusPane list↔output; shows status "focus: <pane> pane…".
- `Enter` / `r` — run selected script (`Start-TuiRunFlow`: dep-check → deps modal if missing, else run/queue).
- `a` — prompt for extra args (quote-aware `Split-StoArguments`), then run.
- `e` — open cron schedule input (`Open-TuiCronInput`).
- `v` — open `.env` editor for selected script.
- `s` — sync scripts repos (background task).
- `i` — scan deps for selected script, opens deps modal if anything missing.
- `l` — lint selected script.
- `u` — system update: apt (PowerShell+Python) + module dirs + venvs.
- `U` — self-update (`git pull --ff-only`), needs restart.
- `h` — open run-history view (scoped to selected script).
- `t` — send n8n webhook test event.
- `x` — kill running script (SIGTERM→SIGKILL); "nothing is running" if idle.
- `X` — clear run queue.
- `n` / `N` — jump to next/previous output search match.
- `y` — copy whole output buffer to clipboard.
- `c` — clear output panel.
- `?` — open help overlay.
- `/` — open live filter input (filters as you type).
- `Ctrl+F` — open output search input.
- `q` — quit (confirm if a run is active).
- `PgUp`/`PgDn`/`Home`/`End` — scroll output (Home disengages follow, End re-engages).

### 1.4 Keybindings — output-focus (FocusPane=output, still list Mode)
- `↑`/`DownArrow`, `k`/`j` — scroll output ±1 line.
- `g` — scroll to top (Follow=false).
- `G` — re-engage follow (Follow=true, jumps to bottom).
- (Other list-mode keys, e.g. run/sync/etc., remain active — only nav keys are redirected.)

### 1.5 Keybindings — deps modal
- `y` — install missing deps then (if not InstallOnly) run.
- `n` — skip install (InstallOnly) or run anyway without installing.
- `Escape` — cancel, "cancelled".

### 1.6 Keybindings — confirm modal
- `y` / `Enter` — invoke OnYes(Data).
- `n` / `Escape` — cancel, "cancelled".

### 1.7 Keybindings — input modal (also filter/search/args/cron prompts)
- `Enter` — submit (`OnSubmit(Text)`), close.
- `Escape` — cancel; if Kind='filter', restores prior filter text live.
- `Backspace`/`Delete` — edit at cursor.
- `←`/`→` — move cursor.
- `Home`/`End` — cursor to start/end.
- Any printable char — insert at cursor; filter kind re-applies filter on every keystroke.

### 1.8 Keybindings — env editor modal
- `Escape` — if dirty & not armed: warns "unsaved changes — esc again to discard, ctrl+s to save" (arms); second `Escape` discards + closes.
- `Ctrl+S` — write file (`Set-Content -NoNewline`, trailing `\n`), re-register secrets, close, status "saved <file> for <script>".
- `↑`/`↓`/`←`/`→` — cursor movement (vertical clamps X to line length).
- `Home`/`End` — line start/end.
- `Enter` — split line at cursor.
- `Backspace` — delete char before cursor, or join with previous line at col 0.
- `Delete` — delete char at cursor, or join next line.
- Any printable char — insert; marks Dirty.

### 1.9 Keybindings — history view
- `Escape`/`Q`/`q`/`h` — close, back to list.
- `Enter` — open selected run's log into the output panel.
- `r` (case-sensitive) — re-run that entry's script through normal flow (dep-check/queue); errors "script '<name>' not found — removed from the repo?" if gone.
- `f` (case-sensitive) — toggle scope: all scripts ↔ just the selected run's script; resets selection to 0.
- `↑`/`↓`, `k`/`j` — move ±1.
- `PgUp`/`PgDn` — move ± page (`Get-TuiBodyHeight`).
- `Home`/`End` — first/last.

### 1.10 Keybindings — help overlay
- Any key — close, back to list.

### 1.11 Mouse (active only in Mode ∈ {list, history}; ignored in deps/input/confirm/env/help)
- **Wheel** (SGR button bit 0x40): delta ±3 rows.
  - History mode: scrolls selection.
  - List mode, pointer over list pane (X ≤ list width): moves selection by sign(delta) (±1).
  - Pointer over output pane: scrolls output by delta.
- **Drag** (motion bit 0x20, button 0 held, an anchor recorded): extends output-pane text selection live, sets Follow=false.
- **Release** (button 0 up): if a selection exists → `Copy-TuiSelection` (clipboard + "copied N lines/chars" toast); else if it was a plain click (no drag) → `Copy-TuiCodeAt` (device-code click-to-copy check).
- **Press, history mode**: click on a row (row≥2, within list-width offset) selects that history item by `Top + (row-2)`.
- **Press, list mode**:
  - Clears any existing text selection.
  - X within list pane and row within list height → focuses list pane, selects clicked row (`ListTop + row`).
  - X within output pane and row within output height → focuses output pane, computes click position, arms a potential drag/click-to-copy anchor.
- **Click-to-copy device code**: clicked word matching `^[A-Z0-9]{8,10}$` (e.g. MS device-login codes) is copied; status "code XXXX <copy-method>".
- Wrapped-line rejoin on copy: selection spanning a wrap-fold reinserts the consumed space; a real newline (different source line) is kept as `\n`.

### 1.12 Visual elements
- **Header bar**: blue-bg black-bold " ▸ scriptorium " chip; right side up to 3 muted chips on CardBg pill: `repo[+repo2...] · synced Xs/m/h/d ago`, hostname, app version (git short hash); collapses to single muted string if too narrow.
- **Panel top border**: box-drawing with inset titles " ≡ scripts [/filter]" and " ❯ [spinner] outTitle"; focused pane's title/fill highlighted (BrCyan/Blue vs Blue/Border).
- **Script list rows**: leading badge glyph — `✓` success(Green) / `✗` failure(Red) / `⊘` killed(BrYellow) / `◷` timeout(BrYellow) / `◇` skipped(BrYellow) / `·` never-run(Muted); spinner overrides when running; `»`(Cyan) when queued. Schedule glyph column: `⚠`(Red, missed) / `↻`(Cyan, scheduled) / space. Name (marquee-scrolled if selected & overflowing, else ellipsis-truncated; filter substring highlighted BrCyan). 2-char runtime tag `ps`(Blue)/`py`(Yellow). Right-aligned age (≤3 chars: Ns/Nm/Nh/Nd). Scrollbar cell (`█` Blue thumb / `│` Muted track).
- **Selection**: SelBg background, Bold White text, Blue `▎` leading accent bar.
- **Zebra striping**: odd rows get CardBg.
- **Completion flash**: 700ms green/red wash on the just-finished script's row, cubic ease-out blend toward Bg.
- **Selection pulse**: 180ms Blue→SelBg blend + BrCyan accent bar on selection move.
- **Details card** (bottom-left, 8 rows, hidden when body height <14): `● name · runtime[ · repo]`, `▸ entry:`, `⚙ env:` (var count + mods/venv presence), `↻ cron:` (expr + next-run hint, or red missed note), `✦ last:` (status/duration/age), optional cpu/mem lines, `at:` timestamp. Placeholder "no script selected" when list empty. `.env` var-count cached on file mtime (2s TTL for the rest).
- **Activity card** (top-right, 2 rows): live-locked scripts (own + cron/MCP/external via lock scan) with spinner/name/elapsed/pid/source; "+N more" overflow; queue-depth line.
- **Recent-runs card** (5 rows): icon/name/runtime-tag/status-word (killed & timeout both shown as "stopped")/6-wide heat-colored cpu sparkline/relative age.
- **Output panel**: word/wide-char-aware wrap, tab-expanded, colored by content pattern (banner icons ✓/✗/⊘◷◇⚠/── ; `WARNING:` muted; word-boundary error/exception/failed/failure/fatal → red; `: success ` → green), scrollbar, search-match reverse-highlight, drag-selection reverse-video (`\e[7m`).
- **Bottom border**: `▼ N more — End follows` (BrYellow) when scrolled back with content below.
- **Status line** (mode-dependent): deps — elided missing-module list + y/n/esc hints; input — live text w/ cursor block; confirm — message + y/esc; default — running-script line with 10-cell eighth-block-precision ETA bar (`% · ~time left` or `+time over`) or transient status message (color by kind, fades over the 5.2–6.0s window (6.3s is the redraw horizon, not the fade end)) or selected script's description+schedule hint.
- **Key-hints footer**: mode-specific key/description pairs (Magenta key, Muted description), truncated to width.
- **Marquee**: selected long name scrolls after 1s pause, ~6 chars/s (165ms/step), loops with `" · "` separator; disabled while filtering.
- **Spinner**: 10-frame braille (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`) @100ms/frame, tinted by a precomputed 32-step BrCyan→Blue ramp on a continuous 1.6s triangle-wave phase.
- **Sparkline**: 8-level blocks `▁▂▃▄▅▆▇█` scaled to series' own max; heat-colored green→BrYellow (levels 0-3) → BrYellow→red (levels 4-7).
- **Scrollbars**: list and output panes both; thumb length=max(1, viewport²/total), position proportional to offset.
- **Env editor view**: SelBg/White header row (`editing <file> — <script>[ *]  (ctrl+s save · esc cancel)`), inverse-video cursor cell, muted `#` comment lines, independent horizontal/vertical scroll to keep cursor visible, `~` muted padding lines beyond content.
- **History view**: header row + column header (`when age status script duration cpu-peak trend mem-peak trigger`), selected row gets Blue `▎` bar, per-row sparkline.
- **Help overlay**: SelBg/White title, `#`-prefixed BrCyan section headers, Magenta-key/default-description entries.
- **Terminal-too-small guard**: W<40 or H<10 → "terminal too small".
- **Frame rendering**: damage-diff writer (only changed rows repainted; full `\e[2J` clear only on first frame / row-count change), synchronized-output wrap (`\e[?2026h…\e[?2026l`). Anim-only frames patch exactly 2 rows (top-border spinner, status line) over the prior full frame. Full rebuild triggers: Dirty, marquee step, any live animation, 1Hz second boundary, or every 100ms while something runs. 16ms(~60fps) budget while animating/running else render-on-Dirty only; hard floor 15ms between any two frame writes; main loop sleeps 8ms/iteration.

---

## 2. CLI SURFACE (`scriptorium.ps1`)

Manual arg-loop parser (unrecognized flags silently ignored). Config warnings printed via `Write-Warning` before any dispatch.

| Flag | Semantics | Exit code |
|---|---|---|
| *(none)* | Launch TUI | n/a (interactive) |
| `--run <name>` | Full headless pipeline: missed-run sweep first (errors swallowed) → resolve script by exact name → auto-install missing deps (no prompt) → `Start-StoRun` (trigger=manual) → stream to stdout → print `-- <script>: <status> (exit <code>) in <dur>s \| cpu avg X% peak Y% \| mem avg XMB peak YMB` | 0 success · 1 failure/other · 3 skipped |
| `--run <name> --args "<extra>"` | Adds extra CLI args, quote-aware split (`Split-StoArguments`) | same |
| `--run <name> --cron` | Marks trigger='cron' instead of 'manual' | same |
| `--list` | Print `Name(-30) rt(-3) status(-10) [schedule]` per script | 0 |
| `--sync` | `Sync-StoRepo`, streamed to stdout | 0 ok · 1 fail |
| `--repos` | Print `name(-15) branch(-8) url[ (legacy scriptsRepo)]` | 0 |
| `--add-repo <url> [--name <n>] [--branch <b, default main>]` | `Add-StoRepoConfig`; prints message + "run 'scriptorium --sync' to clone it" on success | 0 ok · 1 fail |
| `--history [name]` | Print last 200 rows (optional script filter): `yyyy-MM-dd HH:mm:ss  status(-9) script(-25) duration(8)  cpu NN%  mem NNNNNNMB  [trigger]`; "no runs recorded" if empty | 0 |
| `--mcp [--port <n>]` | Requires `$env:MCP_AUTH_TOKEN` set (else error, exit 1); port = `--port` override else `config.mcpPort`; bind = `config.mcpBind`; starts foreground listener | 0 after server stops · 1 if no token |
| `--install-mcp-service` | Installs+starts systemd unit (root=system, non-root=user+linger) | 0 ok · 1 fail (error printed) |
| `--help` / `-h` | Prints first 15 lines of the script's own header comment (skip line 1), strips leading `# ` | 0 |

---

## 3. FILE/DATA CONTRACTS (byte-level)

### 3.1 `history.jsonl` row schema (one compact-JSON object per line, written via single `[IO.File]::AppendAllText` + `"\n"`, UTF8-no-BOM — deliberately not `Add-Content`, whose ~1KB write buffer can split a long row across two writes under concurrent appenders)
```
event: "script_run"
runId: <GUID string> — stable row identity + webhook dedupe key
script: <name>
runtime: "powershell" | "python"
repo: <repo name> | ""
trigger: "manual" | "cron" | "mcp"
status: "success" | "failure" | "killed" | "timeout" | "skipped"
success: bool (status=='success')
exitCode: int (-1 for start-errors/skipped)
startedAt / finishedAt: "yyyy-MM-ddTHH:mm:ss.fffZ" (UTC)
durationSec: double, rounded 1 decimal
host: [Environment]::MachineName
resources: {
  cpuAvgPercent, cpuMaxPercent, memAvgMb, memMaxMb: double, rounded 1 decimal
  samples: int
  cpuSeries, memSeries: double[] — only present if samples>0; downsampled to ≤60 points, max-of-bucket
}
logFile: absolute path | null
```
(NOTE: the webhook payload additionally carries `log` (tail text) — the history row itself does NOT.)

### 3.2 Log file naming
`<LogsDir>/<safeName>-<yyyy-MM-ddTHH-mm-ss-fffZ>.log`, `safeName` = script name with `[^A-Za-z0-9._-]` → `_`. Separately, the crontab wrapper redirects the cron-invocation's own stdout/stderr to `<LogsDir>/cron-<name>.log` (distinct from the per-run log).

### 3.3 Lock file format + reclaim semantics
`<LocksDir>/<Name>.lock`, content = UTF8 bytes of owning PID (no newline). Acquire = `[IO.File]::Open(CreateNew, Write)` (atomic create-exclusive), max 2 attempts. On collision: read PID, `Get-Process -Id` check — alive → not-acquired; dead → check file `LastWriteTime` age: **<10s → not-acquired (reclaim-race guard)**; ≥10s → delete + retry create. Unlock = delete file. Read-only probes (`Test-StoScriptLocked`, `Get-StoRunningScripts`) never reclaim/delete.

### 3.4 `webhook-queue.jsonl`
One compact-JSON payload per line (failed script_run/missed events; never `test`), appended via `Add-Content` UTF8. Flushed after every successful send.

### 3.5 Dead-letter flush (`Send-StoWebhookQueue`)
1. `[IO.File]::Move(queue, queue+".flush")` (throws if `.flush` already exists → another flush in progress).
2. On that throw: if the existing `.flush` is >10 min old (a died flusher), its lines are appended back onto the live queue and the stale `.flush` deleted; returns 0 either way (no send this call).
3. Otherwise: reads non-empty lines, sends **in order**, stops at first failure, **cap 50 sends per call**.
4. Unsent remainder is placed **in front of** whatever was queued live *during* the flush, written back atomically over the queue file. Empty remainder → `.flush` deleted.

### 3.6 `missed-state.json`
`{ <scriptName>: { expr, firstSeen: ISO 'o', lastAlerted: ISO 'o' | null } }`. Read/write via `ConvertFrom/ToJson -AsHashtable`/`-Depth 4`.

### 3.7 `.last-prune`
Empty stamp file under DataDir; its mtime gates the hourly retention throttle.

### 3.8 `config.json` — every key, default, validation
| Key | Default | Notes |
|---|---|---|
| scriptsRepo | `''` | legacy single-repo URL; overridden by `.env` `SCRIPTS_REPO` |
| branch | `'main'` | legacy single-repo branch |
| repos | `[]` | `[{name,url,branch}]`, overrides scriptsRepo/branch |
| dataDir | `'~/.scriptorium'` | `~` expanded to `$HOME` |
| n8nWebhookUrl | `''` | overridden by `.env` `N8N_WEBHOOK_URL` |
| pwshBin | `'pwsh'` | |
| pythonBin | `'python3'` | interpreter used to CREATE venvs only |
| monitorIntervalMs | `1000` | numeric-validated |
| logTailKb | `64` | numeric-validated |
| runTimeoutMinutes | `0` | numeric-validated; 0=no limit |
| maxOutputLines | `5000` | numeric-validated; TUI scrollback |
| openRouterModel | `'google/gemini-3.1-flash-lite'` | |
| syncOnLaunch | `false` | |
| logRetentionDays | `30` | numeric-validated; 0=keep forever |
| historyMaxLines | `50000` | numeric-validated; 0=uncapped; backstop only |
| historyDays | `30` | numeric-validated; ≤0 → retention still uses 30, tab shows last 200 |
| webhookTimeoutSec | `15` | numeric-validated |
| missedGraceMinutes | `5` | numeric-validated |
| colorMode | `'auto'` | `auto`\|`truecolor`\|`256` |
| mcpPort | `8765` | numeric-validated; `--port` overrides per run |
| mcpBind | `'all'` | `all`\|`localhost` |

Validation: unknown key → warning `"config.json: unknown key '<k>' — ignored (typo?)"` (value dropped). Numeric key with non-numeric value → warning `"…'<k>' must be a number, got '<v>' — using default <default>"` (default retained). `repos` entries: missing `url` → warning, skipped; `name` not matching `^[A-Za-z0-9_-]+$` → warning, skipped. Malformed JSON → throws `"config.json is not valid JSON: <msg>"`.

### 3.9 `.env` parsing rules (`Read-StoEnvFile`)
Line trimmed; blank or `#`-prefixed → skipped; first `=` splits key/value (key non-empty required, i.e. `=` not at index 0); value trimmed; if value starts+ends with matching `'…'` or `"…"`, exactly one layer of matching quotes stripped (no escape processing, no `export` prefix, no multi-line, no interpolation). App-level `.env` (next to `scriptorium.ps1`) loads into process env **only for keys not already set** (existing env wins); every loaded key also `Register-StoSecret`'d (name-pattern gated, not forced). Additionally `GITHUB_TOKEN`, `OPENROUTER_API_KEY`, `N8N_WEBHOOK_URL`, `MCP_AUTH_TOKEN` are pulled directly from process env and registered.

`Read-StoEnvDoc` (used for `.env.example` docs): preserves the `#`-comment block directly above each `KEY=VALUE` as that key's `Comment` (joined with spaces); pending comment buffer resets on a blank line or a non-KV line; default value stripped of **any number** of leading/trailing `"`/`'` chars via `.Trim('"',"'")` (looser than the matched-pair stripping in `Read-StoEnvFile`).

### 3.10 Per-script `.env`/`.env.example` resolution
Folder scripts: `<Dir>/.env`, `<Dir>/.env.example`. Loose root scripts: `<Dir>/<name>.env`, `<Dir>/<name>.env.example` (Dir = repo root). Env editor pre-fills from `.env` if present, else `.env.example`, else one blank line. Every per-script `.env` value (≥8 chars) is Force-registered as secret regardless of key name.

### 3.11 `script.json` schema (all keys optional)
`entry` (relative path; extension picks runtime; must resolve within the script dir — containment-checked), `description` (shown in status bar / MCP details), `args` (string array, default args prepended before extra args on every run), `timeoutMinutes` (number; overrides global `runTimeoutMinutes`; non-numeric value ignored → `null`).

### 3.12 Data-dir layout (`dataDir`, default `~/.scriptorium`)
- `scripts/` — legacy: repo root directly at `ScriptsDir`; multi-repo: `scripts/<repoName>/`
- `modules/<scriptName>/<ModuleName>/[<Version>/]` — PowerShell dep isolation
- `venvs/<scriptName>/bin/python` — Python venv isolation
- `logs/` — per-run logs + `cron-<name>.log` wrappers
- `locks/<name>.lock`
- `tools/` — PSScriptAnalyzer, saved on first lint use
- `history.jsonl`, `webhook-queue.jsonl` (+ transient `.flush`), `missed-state.json`, `.last-prune` — directly under DataDir

### 3.13 Legacy migrations
1. **Data-dir migration**: if `dataDir` config equals the built-in default AND resolved dataDir doesn't exist yet AND `~/.psscripts` exists → `Move-Item` the whole legacy dir in place (one-time); logs a warning on failure and continues with an empty new dir.
2. **Crontab block markers**: legacy `# >>> psscripts managed block — do not edit by hand >>>` / `# <<< psscripts managed block <<<` still *read*; any *save* rewrites under current `# >>> scriptorium managed block …` markers (whichever generation is found is replaced).
3. **Single-repo → multi-repo layout migration** (`Update-StoRepoLayout`): triggered when `repos` is configured (non-legacy) and a root-level `.git` exists at `ScriptsDir`; target repo picked by matching normalized remote URL (strips embedded token + `.git` suffix) against configured repos, defaulting to the first; move sequence: `ScriptsDir` → `ScriptsDir.migrating` → recreate empty `ScriptsDir` → move `.migrating` into `ScriptsDir/<targetName>`.
4. **install.sh pre-rename fallback**: `SCRIPTORIUM_APP_DIR` → `PSSCRIPTS_APP_DIR` → `~/scriptorium`; if that has no `.git` but `~/powershell-scripts-tui/.git` exists, uses that dir instead (announced). `psscripts.ps1` shim forwards to `scriptorium.ps1`. Legacy `~/.local/bin/psscripts` launcher removed. Legacy systemd units `psscripts-mcp(.service)` (system+user) retired before installing `scriptorium-mcp`.

---

## 4. RUN PIPELINE SEMANTICS

### 4.1 Entry-point resolution order (exact, `Resolve-StoEntry`)
1. `script.json` `"entry"` (must exist and resolve within the folder — containment-checked).
2. Conventional PowerShell names, case-insensitive, in order: `main.ps1`, `<folder>.ps1`, `run.ps1`.
3. Conventional Python names, case-insensitive, in order: `main.py`, `<folder>.py`, `run.py`, `__main__.py`.
4. Sole/first-alphabetical `.ps1` in the folder; else sole/first-alphabetical `.py`.
5. `null` (folder skipped) if none of the above.
Skip-dirs during discovery: `.git`, `.github`, `__pycache__`, `.venv`, `node_modules`. Loose `.ps1`/`.py` files directly in a repo root are also discovered as scripts.

### 4.2 Runtime detection
Purely by entry file extension: `.py` → python; anything else → powershell.

### 4.3 Per-runtime process setup
- **PowerShell**: `pwshBin -NoProfile -NonInteractive -File <entry> <scriptArgs> <extraArgs>`; env `PSModulePath = "<ModuleDir><PathSep><existing PSModulePath>"` (prepended, not replaced).
- **Python**: venv ensured first (created on-demand if missing — belt-and-suspenders for zero-import scripts that skipped the install flow: `pythonBin -m venv <VenvDir>` + `pip install --upgrade pip --quiet`); `<VenvDir>/bin/python <entry> <scriptArgs> <extraArgs>`; env `PYTHONUNBUFFERED=1`.
- **Common**: `WorkingDirectory = Script.Dir` (so `python-dotenv` finds `.env` natively); stdout/stderr redirected; `UseShellExecute=false`.
- **Env injection order**: (1) per-script `.env` values (each Force-registered as secret) → (2) caller-supplied `ExtraEnv` (MCP `run_script.env`) **overrides** `.env` values (also Force-registered) → (3, PowerShell only) `PSModulePath` prepend applied last.

### 4.4 Secret redaction rules
- `Register-StoSecret(Name, Value, [-Force])`: ignored if `Value` empty or **<8 chars** ("8-char rule"). Without `-Force`: only registered if `Name` matches (case-insensitive) `TOKEN|KEY|SECRET|PASSWORD|PASSWD|PASS|PAT|CREDENTIAL|WEBHOOK|AUTH|CONN|DSN|BEARER`. With `-Force`: registered regardless of name — used for **all** per-script `.env` values and all MCP `run_script.env` overrides.
- `Hide-StoSecret(Text)`: for every registered secret string, `Contains`→`Replace` with `***` (simple loop over a `HashSet<string>`).
- Applied to: every streamed output line before buffering/logging; sync (git) output; MCP tool outputs (install/update/sync commands); MCP internal-error exception messages; env-editor values re-registered post-save.

### 4.5 Lock acquire/skip/stale-reclaim rules (incl. 10s guard)
See §3.3. Skip result: `Start-StoRun` returns an immediately-finished handle with `Status='skipped'`, `ExitCode=-1`, no log file — never enters 'running'.

### 4.6 Timeout precedence chain (highest→lowest)
1. Per-call `TimeoutOverride` (>0) — MCP `run_script.timeout_minutes`.
2. `Script.TimeoutMinutes` (per-script `script.json.timeoutMinutes`, if non-null).
3. Global `config.runTimeoutMinutes` (0 = no limit).
Checked every poll tick against elapsed minutes since `StartedAt`; on trigger emits a line and calls `Stop-StoRun -Reason timeout`.

### 4.7 Kill escalation (TERM→KILL from snapshot)
Snapshot the whole process tree (via `/proc` parent-walk from the root pid) **before** signaling; SIGTERM to every snapshotted pid; wait up to 3s (100ms poll) for the root to exit; **re-check only the original snapshot pids** for surviving `/proc/<pid>` entries (a TERM-ignoring child reparented to init once root exits would be missed by a fresh tree-walk) and SIGKILL those; wait up to 2s more. Non-Linux (no `/proc`): `.NET Process.Kill($true)` (whole tree) + 3s wait.

### 4.8 Resource sampling method
- `Get-StoTreePids`: walks **every** `/proc/*/stat` on the machine, parses ppid (splits after the last `)`), builds a full ppid→children map, then DFS from RootPid.
- Per pid: `jiffies += utime(field 14) + stime(field 15)`; `rssBytes += rss-pages(field 24) * PageSize` (`getconf PAGESIZE`, default 4096).
- CPU%: `Δjiffies / ClkTck (getconf CLK_TCK, default 100) / Δseconds * 100 / CpuCount` — **whole-machine normalization** (`[Environment]::ProcessorCount`, min 1); clamped [0,100]; requires Δt>0.2s.
- Mem: instantaneous RSS sum / 1MB (not delta-based).
- Accumulates Sum (avg) + Max (peak) + Samples; full-resolution series kept for later downsampling.
- **Downsampling** (`Get-StoDownsampledSeries`, max-of-bucket): series ≤60 points passed through (rounded); else split into 60 buckets (`floor(b*n/60)` to `floor((b+1)*n/60)-1`), **max** of each bucket kept — "peaks survive".

### 4.9 History append (single-write)
`[IO.File]::AppendAllText` (not `Add-Content`, whose ~1KB buffer can split a long row) — goal: concurrent appenders (cron/MCP/TUI processes) can't interleave mid-row. **Unlock happens AFTER the history write**, not before, so a queued rapid re-run of a sub-second script can't append its own row first and violate last-status-wins for badges.

### 4.10 Status classification
`running`→(at completion) `success` (exit==0) or `failure` (otherwise); `killed`/`timeout` set explicitly by `Stop-StoRun -Reason`; `skipped` set immediately by `Start-StoRun` on lock-acquire failure (never passes through 'running').

### 4.11 Run queue semantics (TUI only)
`Start-TuiRunFlow`: if `S.Run` already set, appends `{Script, ExtraArgs}` to `S.Queue`, status "queued <name> (position N)". Drains one item/main-loop-iteration when `Run=null` AND `Queue.Count>0` AND `Mode ∈ {list, history, help}` (blocked while a modal — deps/confirm/input/env — is open). Dequeued script re-resolved by name against current `S.Scripts` (survives a mid-queue sync); missing → warns and skips. `X` clears the whole queue. (Headless/MCP never queue — they lock-skip or block synchronously.)

### 4.12 ETA median computation
On `Start-TuiRun`: gathers up to the last 200 history rows for `script name + status=success`, sorts `durationSec`, takes index `floor(count/2)` (upper-median). `0.0` (no ETA bar) if no successful history. Computed once at run start, never re-derived mid-render.

---

## 5. WEBHOOK CONTRACT

Endpoint: POST to `n8nWebhookUrl`/`N8N_WEBHOOK_URL`, `Content-Type: application/json`, per-attempt timeout = `webhookTimeoutSec` (default 15).

### 5.1 `script_run` payload
`event, runId(guid), script, runtime, repo, trigger, status, success, exitCode, startedAt, finishedAt, durationSec, host, resources{cpuAvgPercent,cpuMaxPercent,memAvgMb,memMaxMb,samples,cpuSeries?,memSeries?}, logFile, log(tail)`.

### 5.2 `test` payload
`{event:"test", host, at}` — **never queued on failure** (interactive/synchronous; user sees the failure directly).

### 5.3 `missed` payload
`{event:"missed", script, schedule, expectedAt("yyyy-MM-ddTHH:mm:ssZ" UTC), detectedAt(same), host}`.

### 5.4 Retry
1 send + on failure `Sleep 2s` + 1 retry = **2 attempts total**. Both fail → payload (compact JSON) appended to `webhook-queue.jsonl` (except `test` events). Any successful send (first, retry, or a flush send) triggers `Send-StoWebhookQueue`.

### 5.5 Dead-letter queue semantics
See §3.5: move-aside via `[IO.File]::Move` (throws if `.flush` exists → reclaim stale one after 10min); sends in order, stops at first failure, **cap 50/flush**; unsent remainder placed **in front of** anything queued during the flush; atomic move-back.

### 5.6 Log tail size
`Get-StoLogTail`: last `TailKb*1024` bytes (seek from end if longer). Webhook default 64KB (`logTailKb`). MCP `get_run_log.tail_kb`: default 64, clamp 1–256.

---

## 6. CRON

### 6.1 Managed block markers
- Current: `# >>> scriptorium managed block — do not edit by hand >>>` / `# <<< scriptorium managed block <<<`
- Legacy (read-only): `# >>> psscripts managed block — do not edit by hand >>>` / `# <<< psscripts managed block <<<`
- `Save-StoSchedules` always writes under the **current** markers, replacing whichever generation is found; everything outside the block untouched.

### 6.2 Entry line format (per scheduled script, sorted by name)
```
<cronExpr> cd '<appDir>' && '<pwshBin>' -NoProfile -File scriptorium.ps1 --run '<name>' --cron >> '<logsDir>/cron-<name>.log' 2>&1
```
The command portion (everything from `cd ` onward) has literal `%` escaped to `\%` before writing; the expression is written verbatim. See §6.8.

### 6.3 Reader
`--run '([^']+)'` extracts name; `^(@\S+|(?:\S+\s+){4}\S+)\s+cd ` extracts expression (either an `@keyword` or exactly 5 whitespace-separated fields before ` cd `).

### 6.4 Schedule parsing (5-field + `@` keywords)
- `@hourly`="0 * * * *", `@daily`/`@midnight`="0 0 * * *", `@weekly`="0 0 * * 0", `@monthly`="0 0 1 * *", `@yearly`/`@annually`="0 0 1 1 *"; `@reboot` → never computable (`Get-StoCronNext` returns null).
- 5 fields: minute[0-59] hour[0-23] dom[1-31] month[1-12, jan-dec] dow[0-7, sun-sat; both 0 and 7 = Sunday].
- Per comma-separated part: `*`=full range; `N`=single; `N-M`=range; `N/S`=step starting at N extended to Max; `*/S`=step Min..Max; names resolved case-insensitively.
- **Vixie dom/dow union rule**: when BOTH dom and dow fields textually do not start with `*` ("restricted"), a day matches if EITHER matches (union); if only one is restricted, only it governs; if neither, every day matches.
- **Next** (`Get-StoCronNext`): starts at `From+1min` (minute-truncated), walks forward up to 1462 days (4yr, covers leap years), returns first valid datetime; `null` on parse failure/`@reboot`/impossible date.
- **Prev** (`Get-StoCronPrev`, for missed-run "expected" time): tries widening lookback windows (1hr, 1day, 8day, 35day), within each repeatedly calls `Get-StoCronNext` forward from the lookback point (≤2000 iterations) keeping the last result ≤`From`; returns first non-null across windows; `null` for `@reboot`/unparseable.

### 6.5 Natural-language via OpenRouter
- First tries `Test-StoCronExpression` on raw text (full field-by-field validation via the same parser) — literal cron/`@keyword` short-circuits, no network call, `Source='literal'`.
- Else requires `OPENROUTER_API_KEY`; absent → error mentions the var name, `Source='ai'`, `Expression=null`.
- Model: `config.openRouterModel` (default `google/gemini-3.1-flash-lite`).
- POST `https://openrouter.ai/api/v1/chat/completions`, Bearer auth, system prompt: *"Convert the user's scheduling request into a single standard 5-field cron expression. Reply with ONLY the cron expression, nothing else."*, 30s timeout.
- Reply validation: strips backticks, splits lines, takes the **first line that validates** as cron (tolerates fenced/prose replies); none valid → error quoting the raw reply; network/API error → `"OpenRouter request failed: <msg>"`.
- TUI flow: shows a confirm ("schedule '<name>' as: <expr> ?") before writing. Empty input = remove schedule, no confirm.

### 6.6 Crontab wipe guard
`Get-StoCrontab` distinguishes "command failed" (`Ok=false` — spool permission/missing binary) from "empty crontab" (`Ok=true`, `crontab -l` exit 1 + empty stdout = "no crontab for user"). `Save-StoSchedules` **refuses to write anything** when `Ok=false`.

### 6.7 Name validation
Scheduled script names must match `^[A-Za-z0-9._-]+$` (checked in `Set-StoSchedule`) — protects both the shell-quoted crontab line and the `--run '([^']+)'` reader regex from corruption.

### 6.8 `%` escaping
Any literal `%` in the **command portion** of the cron line escaped to `\%` before writing (crontab treats bare `%` as a command/newline separator). `Cron.psm1:74` applies the replace to `$cmd` alone — `"$expr $($cmd -replace '%', '\%')"` — so a `%` in the *expression* is written unescaped (an expression containing one is not valid cron anyway).

---

## 7. MISSED-RUN DETECTION (full algorithm)

**Pure detector** (`Get-StoMissedRuns`):
1. Empty schedules → return `[]`.
2. Build `running` set from `Get-StoRunningScripts` (live lock scan).
3. Build `lastCron` map: newest cron-triggered history row's `startedAt` (local) per script (scan last 2000 rows, chronological → later overwrites).
4. Per scheduled script:
   a. `FirstSeen[name]` missing/invalid → **skip** (schedule "just appeared"; judged next sweep).
   b. `expected = Get-StoCronPrev(expr, Now)`; null → skip.
   c. `expected < FirstSeen` → skip (protects against schedule-change producing a stale expected time).
   d. `(Now-expected).TotalMinutes < GraceMinutes` (default `missedGraceMinutes`=5) → skip.
   e. Script currently locked/running → skip (fired, hasn't recorded a row yet).
   f. `lastCron[name] >= expected.AddMinutes(-1)` (1-min tolerance for cron's few-seconds-late fire) → skip (it ran).
   g. Else → flagged `{Name, Expression, ExpectedAt}`.

**Stateful wrapper** (`Invoke-StoMissedRunCheck`):
- Loads/creates `missed-state.json`.
- New/changed-expression schedule → (re)stamp `firstSeen=now`, `lastAlerted=null` (this is what makes new/changed schedules unjudged until their first post-change fire).
- Removes state for de-configured schedules.
- Runs the pure detector with the just-updated `FirstSeen`.
- **Dedup**: `lastAlerted` set AND `ExpectedAt <= lastAlerted` → skip re-alert (but the fire is still returned in the result set for UI display every sweep until it actually runs).
- New alert → `lastAlerted=ExpectedAt`, sends `missed` webhook.
- Persists state if dirty.
- **Known race** (documented `ponytail:` comment): no lock around the state file — two simultaneous sweeping processes could double-send one alert; mitigated by suggesting n8n dedupe on `script+expectedAt`, not fixed app-side.

**Where sweeps run**: TUI main loop every 60s (throttled); every headless `--run <script>` boot (piggybacked first, before dep-check — "each cron run is one" sweep, so alerts flow with no TUI open). **Not** run by `--sync`, `--list`, `--mcp` startup, or MCP tool calls directly.

**Grace**: `config.missedGraceMinutes`, default 5.

---

## 8. RETENTION (full policy, `Clear-StoOldData`)

Runs at every process startup (TUI + every headless CLI call), throttled to **once/hour** via `.last-prune` mtime (bypassable with `-Force`, tests only).

**8.1 Aged/orphaned log files**: if `logRetentionDays>0`, delete every `*.log` directly under `LogsDir` with `LastWriteTime` older than `now-logRetentionDays`; `0`=disabled. Pure filesystem sweep, independent of history content.

**8.2 History rows (+ their logs)**, full rewrite:
- Window: `historyDays` (default 30); `≤0` falls back to 30 **for this pass** (only the History-tab default view changes to "last 200", not retention).
- `histCutoff = now - historyDays`; `successCutoff = now - 1day`.
- "Frequent" scripts (`Get-StoFrequentScripts`): next two scheduled fires ≤10 minutes apart.
- **Pass 1**: stream the whole file once, regex-extract just `"script"`, record the LAST line index per script — this row always survives (status badges/`--list`/MCP `list_scripts` are last-row-wins per script).
- **Pass 2**: blank/unparseable row or no valid `startedAt` → unconditionally dropped (dead weight). `stale = (startedAt < histCutoff) OR (status=='success' AND startedAt < successCutoff AND script is 'frequent')`. Dropped if `stale` AND not the newest-index row for that script.
- **`historyMaxLines` cap** (default 50000, `0`=uncapped): after the above, trims **oldest** excess rows from the front — pure backstop, not the primary mechanism.
- Rewrite: atomic — write `.tmp`, `[IO.File]::Move(tmp, real, true)` (single rename, not `Move-Item -Force` which deletes-then-renames). Documented race: a concurrent append landing in that exact instant could be lost (mitigated, not eliminated, by the hourly throttle).
- Log deletion for dropped rows: only if the resolved absolute path is inside `LogsDir` (prefix-containment check against the resolved, separator-normalized `LogsDir`) — verified to never delete outside `LogsDir`, including a sibling dir that merely shares the string prefix (e.g. `<logsdir>-archive`).

**Effective summary**: normal scripts keep the full `historyDays` window for ALL statuses; scripts scheduled ≤10min keep only 1 day of **success** rows (their failure/killed/timeout/skipped rows still get the full window); every script's single newest row always survives regardless. Changing a schedule to ≤10min cadence reclassifies its entire existing success backlog on the very next sweep (not gradually).

---

## 9. DEPS

### 9.1 PowerShell dep detection (AST-based, `Get-StoScriptDeps`)
Three sources unioned by name (case-insensitive); a call carrying ANY version constraint always overwrites an existing entry (even a previously-versioned one); a later bare/unversioned call never overwrites an existing entry:
1. `#Requires -Modules` (`$ast.ScriptRequirements.RequiredModules`) — `Name`, `RequiredVersion` (exact), `Version` (→ used as MinimumVersion — PS's own confusing field naming), `MaximumVersion`.
2. `using module X` (`$ast.UsingStatements` where Kind=='Module').
3. `Import-Module`/`ipmo` `CommandAst` calls:
   - Only the **first bare positional** is treated as name(s); later positionals ignored as stray values.
   - String literal → one name; array literal (`@('A','B')` or `'A','B'`) → each string-literal element.
   - `-Name X` param form: the `-Name` token itself is skipped, the following bare element is picked up as the name via the same first-positional logic.
   - `-Param:value` form (colon-bound) skipped entirely.
   - Value-params list (`Function, Cmdlet, Variable, Alias, Prefix, MinimumVersion, MaximumVersion, RequiredVersion, ArgumentList, Args, FullyQualifiedName, Scope, PSSession, CimSession, CimResourceUri, CimNamespace` + common-parameter aliases `ErrorAction/ea, WarningAction/wa, InformationAction/infa, ProgressAction/proga, ErrorVariable/ev, WarningVariable/wv, InformationVariable/iv, OutVariable/ov, OutBuffer/ob, PipelineVariable/pv`) — causes the **next** element to be skipped too (its value), e.g. `-ErrorAction Stop` never misreads "Stop" as a module.
   - Any other named switch consumes nothing extra.
- **Exclusions** (applied after collection): builtin modules (15-entry list: `Microsoft.PowerShell.Archive/Core/Diagnostics/Host/Management/Security/Utility/PSResourceGet`, `PSReadLine`, `PackageManagement`, `PowerShellGet`, `ThreadJob`, `CimCmdlets`, `PSDiagnostics`, `Microsoft.WSMan.Management`); names with a path separator or `.psm1`/`.psd1`/`.dll` suffix (local/path import); a name matching a `<name>.psm1` file directly in the script's Dir; **for folder scripts only** (loose-script exemption — a loose script's Dir is the whole repo, where sibling script folders would false-match), a name matching a subfolder of Dir.
- **Gallery name mapping** (case-insensitive): `pester→Pester, az→Az, awstools→AWS.Tools.Common, awspowershell→AWSPowerShell.NetCore, sqlps→SqlServer`.
- Result sorted by Name.

### 9.2 Install command generation (PowerShell)
Creates `ModuleDir` if missing. Per dep: prefers `Save-PSResource` (PSResourceGet) when available — `Version` as NuGet-range string (`[minv,maxv]`, `[minv,)`, `(,maxv]`, or exact) — else `Save-Module` (RequiredVersion/MinimumVersion/MaximumVersion params). Both target `-Path <ModuleDir> -Repository PSGallery` (`-TrustRepository` / `-Force`). Continues past per-module failures (`ErrorActionPreference='Continue'`), overall `exit 1` if any failed.
**Layout read back**: `<ModuleDir>/<ModuleName>/<Version>/` — version subdirs detected via `-as [version]`; no valid version subdir → treated as version `0.0` (still "installed"). Unions with `Get-Module -ListAvailable -Name <depNames>` (name-restricted to avoid a full PSModulePath walk).

### 9.3 Dep satisfaction
Absent name → unsatisfied. `RequiredVersion` set → needs exact installed-version match (unparseable constraint → assume OK, fails open). Else Min/Max bounds checked (each optional).

### 9.4 Upgrade-all (PowerShell)
Walks every `<ModulesDir>/<scriptDir>/<moduleDir>`, re-saves each by name (no constraint = latest) into its existing path, same Save-PSResource/Save-Module preference, per-module try/catch.

### 9.5 Python dep detection (venv scanner)
- **`requirements.txt` precedence**: if present in `Script.Dir`, the AST scanner is skipped entirely. Parsed via `Read-StoRequirements`: `#` comments and `-`-prefixed option lines (e.g. `-r other.txt`) skipped; each remaining line split on first of `;[<>=!~ ` and first token kept (strips markers/extras/specifiers). "Missing" = names not in `pip list --format=json` inside the venv (underscore↔hyphen normalized both sides); no venv yet → everything is missing.
- **Else, embedded Python AST scanner** (run via venv python, or system `pythonBin` if no venv — just to FIND imports, its own installed/missing split ignored in that case):
  - Two `os.walk` passes: collect "local" names (every subdir not starting with `.`/`__`, every `.py` basename) across the tree; then parse each file's AST (skip SyntaxError files), collect top-level `ast.Import`/`ast.ImportFrom(level==0)` module roots.
  - `third_party = imports − stdlib_module_names(+__future__) − local`.
  - Per name, `importlib.util.find_spec` (try/except) classifies installed/missing.
  - Output: last stdout line = `{"missing":[...],"installed":[...]}` JSON.
  - Venv existed → missing = scanner's `missing` only. No venv → missing = `missing`+`installed` combined (nothing is actually installed anywhere).
- **Import→pip name map** (~30 entries): `cv2→opencv-python, PIL→pillow, sklearn→scikit-learn, skimage→scikit-image, bs4→beautifulsoup4, yaml→pyyaml, dotenv→python-dotenv, dateutil→python-dateutil, Crypto→pycryptodome, nacl→pynacl, serial→pyserial, usb→pyusb, psycopg2→psycopg2-binary, MySQLdb→mysqlclient, git→GitPython, github→PyGithub, jwt→PyJWT, docx→python-docx, pptx→python-pptx, fitz→PyMuPDF, magic→python-magic, websocket→websocket-client, websockets→websockets, telegram→python-telegram-bot, kafka→kafka-python, zmq→pyzmq, OpenSSL→pyopenssl, Levenshtein→python-Levenshtein, gi→PyGObject, cairo→pycairo, win32api→pywin32, attr→attrs, google→google-api-python-client`. Unmapped names install under their own import name. `requirements.txt`-driven names are used verbatim (no mapping — already real package names).

### 9.6 Venv creation
On-demand: (a) install-deps flow — if venv python missing, `pythonBin -m venv <VenvDir>` + quiet pip upgrade (failure hints at missing `python3-venv`); (b) at run start — same belt-and-suspenders check for zero-import scripts.

### 9.7 Upgrade-all (Python)
Upgrade system pip first (via `pythonBin`), with a PEP 668 externally-managed fallback (`--break-system-packages`, needed Ubuntu 23.04+), non-fatal either way. Then per existing venv (has `bin/python`): upgrade that venv's pip; list outdated via `pip list --outdated --not-required --format=json` (**`--not-required` deliberately restricts to top-level packages only** — force-upgrading a transitive dep like `pydantic-core` past its parent's exact pin has broken real venvs); upgrade only those; `pip check` afterward, reports+suggests delete-and-recreate if broken; overall non-zero exit if any venv ended up broken.

### 9.8 Lint
- **PowerShell**: PSScriptAnalyzer saved into `<DataDir>/tools` on first use (prepends to PSModulePath), preferring `Save-PSResource`; runs `Invoke-ScriptAnalyzer -Severity Information,Warning,Error`; prints each as `Severity(-11) LLine(-4) RuleName: Message`; "no findings — clean" if empty; **exit 1 only if any finding has Severity=='Error'**.
- **Python**: tries `pyflakes` in venv (or system python if none); auto-`pip install --quiet pyflakes` on first-use failure; runs `python -m pyflakes <entry>`, exit 1 with findings printed if it fails. If pyflakes remains unavailable after install attempt → fallback to `python -m py_compile <entry>` syntax-only check ("syntax OK"/exit 1) — never catches logic/style, only syntax errors.

---

## 10. REPO SYNC

### 10.1 Clone vs hard-reset
`Sync-StoOneRepo`:
- **Token injection**: `$env:GITHUB_TOKEN` set AND url starts `https://` AND no existing `@` → rewritten to `https://x-access-token:<token>@…`; used for both clone and every `git remote set-url origin` refresh (so an expired-then-regenerated token is picked up on the next sync automatically).
- **No `.git` yet** → `git clone --branch <branch> <url> <dir>`.
- **Existing clone** → hard-reset sequence, **each step's exit code checked individually, loop breaks on first failure**:
  1. `git remote set-url origin <url>` (refresh token, output discarded)
  2. `git fetch origin`
  3. `git checkout <branch>`
  4. `git reset --hard origin/<branch>`
  5. `git clean -fdx -e .env -e **/.env -e __pycache__ -e *.pyc`
- All git output piped through `Hide-StoSecret` before emission.
- Summary: `"[name] sync complete"` or `"[name] sync FAILED — check GITHUB_TOKEN in .env (the PAT needs Contents:Read on <url>)"`.

### 10.2 `.env` survival rules
The `git clean -fdx` exclude patterns (`-e .env -e **/.env -e __pycache__ -e *.pyc`) only protect **untracked** files. A `.env` that IS tracked in the scripts repo is overwritten by `reset --hard` whenever upstream content changes — called out as a footgun; recommended practice: gitignore `.env`, commit `.env.example`.

### 10.3 Multi-repo config + name qualification
`repos: [{name,url,branch}]` normalized (`Get-StoRepos`) to `{Name, Url, Branch(default main), Root=ScriptsDir/<Name>, Legacy=false}`; empty/invalid `url` or invalid `name` (`^[A-Za-z0-9_-]+$`) entries silently skipped. `repos` empty/unset → single synthetic legacy entry `{Name='scripts', Url=Get-StoScriptsRepo() [config.scriptsRepo, overridable by env SCRIPTS_REPO], Branch=config.branch, Root=ScriptsDir itself, Legacy=true}`, present even with no URL configured.
**Duplicate-name qualification** (`Get-StoScripts`): candidates collected across ALL repos first, per-base-name counts built simultaneously; ANY base name appearing >1 time (any repo, incl. folder+loose-file collision within one repo) is qualified as `<repoName>-<baseName>` **in every repo it appears in** — deliberately not first-wins, so identity never depends on repo sync order/content (Name keys locks/history/log-naming/crontab). Residual same-name collision after qualification (folder + loose file, same base, same repo) → second one gets `-2` suffix (fixed order: folders before loose files, alphabetical within each).

### 10.4 Layout migration
See §3.13 item 3.

### 10.5 Last-sync time source
`Get-StoLastSyncTime`: per repo, `<Root>/.git/FETCH_HEAD` mtime (touched by every fetch) else `<Root>/.git` dir mtime (fresh clone, never re-synced); **MAX across all configured repos** reported. Purely filesystem-mtime based — reflects syncs from any process (TUI, cron `--sync`, another session).

### 10.6 `--add-repo` config-write flow
If existing `scriptsRepo` set and `repos` still empty → legacy repo first converted into its own `repos` entry (name from URL, `-legacy` suffix if colliding with the new repo's name) so it keeps syncing. New name validated (`^[A-Za-z0-9_-]+$`, auto-sanitized if derived from URL); rejects duplicate name (case-insensitive) or duplicate URL (compared with embedded-credential and `.git`/trailing-slash normalization stripped) even under a different name. Rewrites `config.json` (`ConvertTo-Json -Depth 6`, UTF8), preserving all other top-level keys.

---

## 11. MCP SERVER

### 11.1 Transport
"Streamable HTTP" simplest legal form: stateless (no `Mcp-Session-Id`), no SSE, one JSON-RPC message per POST, plain `application/json` response.

### 11.2 Endpoints
- `GET /healthz` → 200 `"ok"` (text/plain), no auth.
- `POST /mcp` (bare `/` also accepted) → JSON-RPC dispatch, auth required.
- Any other path → 404 `{"error":"not found"}`.
- Non-POST/non-healthz-GET on `/mcp` → 405 `{"error":"method not allowed"}` (no GET/SSE, no DELETE/session-teardown — consistent with statelessness).

### 11.3 Auth
Single shared Bearer token (`MCP_AUTH_TOKEN`). Checked via case-sensitive regex `^\s*Bearer\s+(.+?)\s*$` + case-sensitive (`-ceq`) comparison, **before reading the body** (unauthenticated client can't drive server-side allocation). Failure → 401 `{"error":"unauthorized"}`. Server **refuses to even start** without a token (`Start-StoMcpServer` throws; `--mcp` checks env var first and errors before calling it).

### 11.4 Body cap
1MB (`$McpMaxBodyBytes`), enforced twice: fast-path via `Content-Length` header (immediate 413), then a defensive bounded-read loop into a `[cap+1]`-sized buffer regardless of what `Content-Length` claimed (chunked requests report `-1`) — also 413s if the actual byte count exceeds the cap.

### 11.5 Protocol versions
Advertises/accepts `2025-06-18`, `2025-03-26`, `2024-11-05`; default (unrecognized client version) = `2025-03-26`. Negotiated per-`initialize` call, no session state.

### 11.6 Status codes
200 — any complete JSON-RPC exchange (both `result` and JSON-RPC `error` objects ride HTTP 200). **202 with empty body** for a notification (request with **no `id` key present at all**, distinguished via `ContainsKey`, not null-check). 401 unauthorized. 404 unknown path. 405 wrong method. 413 too large. 500 only for an unhandled exception escaping the whole listener try/catch (`{"error":"internal"}`, distinct from JSON-RPC `-32603`).

### 11.7 JSON-RPC error codes
`-32700` parse error (not valid JSON / not an object) · `-32600` invalid request (missing `method`) · `-32601` method not found · `-32602` invalid params (missing `tools/call.name`, or unrecognized tool — message lists all valid tool names) · `-32603` internal error (exception inside a tool; message redaction-filtered via `Hide-StoSecret`).

### 11.8 Tool-level vs protocol-level errors
A tool that runs but reports failure (unknown script name, bad cron, a script exiting non-zero — reported as `status:"failure"`, NOT an MCP error) is a **200 result with `isError:true`** inside it. Only a dispatch-level exception (something throwing unexpectedly inside `Invoke-StoMcpTool`) becomes a true JSON-RPC `-32603` error.

### 11.9 Tools (12 total, exact order from `tools/list`)

1. **`list_scripts`** — in: `{}`. out: `{scripts:[{name,runtime,repo,description,entry,running:bool,lastStatus(status|"never run"),lastRunAt(ISO|null),lastDurationSec|null,schedule(cron|null),timeoutMinutes}]}`. `readOnlyHint+idempotentHint`.
2. **`get_script_details`** — in: `{script*}`. out: `{name,description,runtime,repo,entry,timeoutMinutes,defaultArgs[],readme(redacted, 16KB cap+"[truncated]"),envExample[{key,default,comment}],envConfigured[keys only],parameters[](PS only: {name,type,mandatory,default,validateSet[],isSwitch,description}),parameterSource,argsHint,help{synopsis,description}?,parseWarnings?}`. Unknown script → error listing valid names. `readOnlyHint`.
3. **`run_script`** — in: `{script*, args?(quote-aware string), env?(object), timeout_minutes?}`. out: `{script,status,exitCode,durationSec,startedAt,finishedAt,logFile,output(log tail or joined lines),resources{cpuAvgPercent,cpuMaxPercent,memAvgMb,memMaxMb},note?("already running…" on skip),installedModules?,depInstallWarning?}`. Blocks synchronously; full pipeline (auto-install no-prompt, lock, history trigger='mcp', webhook).
4. **`get_history`** — in: `{script?, limit?(default 20, max 200 — `ContainsKey` check, not falsy-check, since omitted defaults must not collapse to 1)}`. out: `{runs:[{script,trigger,status,exitCode,startedAt(re-serialized ISO),durationSec,logFile,logId}]}`, newest-first. `readOnlyHint`.
5. **`get_run_log`** — in: `{log_id*, tail_kb?(default 64, max 256)}`. out: `{logId,log}`. Strict validation: `^[A-Za-z0-9._-]+\.log$` AND no `..` — invalid shape errors before touching disk; valid-shape-but-missing errors with retention-aware hint. `readOnlyHint`.
6. **`sync_repos`** — in: `{}`. out: `{ok,output}`; `isError=!ok`. `idempotentHint` (mutates the checkout, not readOnly).
7. **`get_schedules`** — in: `{}`. out: `{schedules:[{script,cron,nextRun(ISO|null)}]}`, sorted by name. `readOnlyHint`.
8. **`set_schedule`** — in: `{script*, cron*}`. Validates via `Test-StoCronExpression` **before** writing (invalid → error with example valid forms, nothing written). out: `{script,cron,nextRun,note:"schedule saved to crontab"}`.
9. **`remove_schedule`** — in: `{script*}`. out: `{script,note:"schedule removed"|"no schedule was set"}` (never errors either way).
10. **`install_deps`** — in: `{script*}`. Nothing missing → `{script,upToDate:true}`. Else `{script,installed[],ok,output}`; `isError=!ok`. `idempotentHint`.
11. **`update_app`** — in: `{}`. `git -C <appDir> pull --ff-only`, redacted output. `{ok,output,note:"restart the MCP service to apply: systemctl restart scriptorium-mcp"}`. `idempotentHint`.
12. **`update_packages`** — in: `{}`. Checks passwordless sudo (`sudo -n true`); if yes, apt-upgrades powershell/python3/python3-pip/python3-venv (else logs skip + manual command); always then runs module-dir upgrade + venv upgrade in sequence (both redacted). `{ok(AND of module+venv success — apt result doesn't gate ok),output}`. `idempotentHint`.

(*=required. Only 5 tools carry `readOnlyHint`: list_scripts, get_script_details, get_history, get_run_log, get_schedules.)

### 11.10 Systemd unit contents (`Get-StoMcpServiceUnit`)
```ini
[Unit]
Description=scriptorium MCP server
After=network.target

[Service]
ExecStart=<PwshPath> -NoProfile -File <AppDir>/scriptorium.ps1 --mcp
WorkingDirectory=<AppDir>
Environment=HOME=%h
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```
(`Environment=HOME=%h` needed because a system-manager unit with no `User=` sets no `$HOME` otherwise, and the app expands `~/.scriptorium`; `%h` for the SYSTEM manager instance resolves to `/root`.)

### 11.11 Install flow (`--install-mcp-service`)
Linux-only (throws otherwise). Requires `MCP_AUTH_TOKEN` already set (throws with hint otherwise — "would just crash-loop"). **Root**: retires legacy `/etc/systemd/system/psscripts-mcp.service` first (`disable --now` + delete file) if present; writes `/etc/systemd/system/scriptorium-mcp.service`; `daemon-reload`; `enable`; `restart` (not `enable --now`, so re-running the command after e.g. a `mcpPort` change actually applies it). **Non-root**: same legacy retirement for `~/.config/systemd/user/psscripts-mcp.service`; writes user unit at `~/.config/systemd/user/scriptorium-mcp.service`; `systemctl --user daemon-reload/enable/restart`; **`loginctl enable-linger $USER`** so the user manager (and service) survives logout/reboot without an active session. Both paths print `systemctl [--user] status`/`journalctl [--user] -u … -f` hints. (README warns: never put `--mcp` inside the crontab managed block — it's fully regenerated by `Save-StoSchedules` and any foreign line inside the markers is dropped on the next schedule edit.)

### 11.12 API server — Go-only addition (2026-09-02, not a PS divergence)

The Go rebuild adds a REST surface (`internal/mcp/api.go`) co-hosted on the
same listener/token/1MB-cap as the MCP server above, mapping `/api/v1/*`
routes one-to-one onto the identical `Ops` methods `tools/call` dispatches
onto (never a forked implementation). This is not listed among the
"Deliberate divergences" at the end of this document because nothing in the
PowerShell app diverges — PS has no REST surface at all to compare against;
it is a pure Go-only addition, requested by the user alongside P9's MCP work.
See docs/superpowers/specs/2026-09-01-go-rebuild-design.md §"API server —
2026-09-02 user addition" for routes, error-mapping rationale and the
same-Ops parity test.

---

## 12. THEME/RENDERING

### 12.1 Night Owl palette hexes
`Bg=#011627, Fg=#d6deeb (canonical Night Owl soft blue-white — the embedded TUI palette differs from the shipped terminal-profile file's #cccccc), SelBg=#093b5e, Black=#011627, Red=#ef5350, Green=#22da6e, Yellow=#c5e478, Blue=#82aaff, Magenta=#c792ea, Cyan=#21c7a8, White=#ffffff, BrBlack=#637777, BrYellow=#ffeb95, BrCyan=#7fdbca, Border=#5f7e97`.
(Separate `night-owl-dark` file: a terminal-emulator profile users apply to their own terminal app — 16-color ANSI palette + `foreground=#cccccc`, `selection-background=#093b5e` — not read by the PowerShell TUI itself, just a matching reference theme.)

### 12.2 Semantic role mapping
`Reset, Bold, Dim`; `Bg/Fg/SelBg` = ANSI escapes for those colors; `Red/Green/Yellow/Blue/Magenta/Cyan/White` = fg escapes; `Muted`=BrBlack fg; `BrYellow/BrCyan` fg; `BlueBg`=Blue bg; `BlackFg`=Black fg; `Border` fg; `CardBg`=Bg blended 4.5% toward white (`Get-StoBlendHex Bg #ffffff 0.045`). Usage: success=Green, failure=Red, killed/timeout/skipped/missed-warn=BrYellow(missed itself=Red), scheduled=Cyan, running=spinner ramp, queued=Cyan, selected-row=SelBg+BoldWhite+Blue-bar, zebra=CardBg, python-tag=Yellow, powershell-tag=Blue, borders=Border(Blue focused), muted-text=Muted, key-hints=Magenta.

### 12.3 256-color fallback algorithm
`ConvertTo-Ansi256Index`: computes squared-distance to BOTH the 6×6×6 cube (steps `0,95,135,175,215,255` per channel, nearest step per channel independently, then exact-quantized-RGB distance) AND the 24-step grayscale ramp (indices 232–255, value=`8+10*round((avg-8)/10)` clamped 0-23); smaller-distance candidate wins (cube index `16+36r+6g+b` or `232+i`).

### 12.4 Truecolor detection
`colorMode='truecolor'` forces truecolor; `'256'` forces 256; `'auto'`(default) checks `$env:COLORTERM -match 'truecolor|24bit'` → truecolor if matched, else 256. Decided once at `Initialize-Sto`, cached for the session.

### 12.5 Display-width rules
- 0-width: ZWJ(U+200D), combining marks(U+0300–036F), variation selectors(U+FE00–FE0F), combining supplement(U+20D0–20FF).
- 2-width: Hangul Jamo(U+1100–115F), CJK+(U+2E80–A4CF), Hangul syllables(U+AC00–D7A3), CJK compat(U+F900–FAFF), CJK compat forms(U+FE30–FE4F), fullwidth forms(U+FF00–FF60), fullwidth signs(U+FFE0–FFE6), emoji+supplementary symbols(U+1F300–1FAFF), CJK ext(U+20000–3FFFD).
- Else 1-width. ASCII-only strings (`^[\x20-\x7e]*$`) take a fast path (`.Length`, no codepoint walk). Surrogate pairs advance index by 2.

### 12.6 Wrap algorithm
Word-aware, exact display-cell width. Finds largest codepoint-prefix fitting `width` cells (surrogate-aware); if cut point >0 (else forced to 1 for guaranteed progress on an unbreakable wide char), looks for the LAST space within that prefix — breaks there if it falls in the second half (`break > cut/2`, avoids tiny leading fragments), else hard-breaks exactly at the cell cut (mid-word if needed, no chars lost). Wrap width = `max(10, termWidth - listPaneWidth - 3 - 1)` (borders + scrollbar column).

### 12.7 Tab expansion
`Expand-TuiTabs`: only invoked when a raw line actually contains `\t`. Expands to 8-column stops, tracking a running **display-cell** column (not char count) across each tab-split segment so wide characters before a tab are correctly accounted for.

### 12.8 ANSI stripping regex
`$script:AnsiRegex = \x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\a\x1b]*(?:\a|\x1b\\)|\x1b.` — three alternatives: CSI sequences (ESC `[` + params + letter terminator), OSC sequences (ESC `]` + anything not BEL/ESC, terminated by BEL or ST/`ESC\`), catch-all "ESC + any single other byte" (prevents an unmatched lone ESC from corrupting frame/width math downstream). Applied to every captured script-output line **before** buffering (`Add-TuiOutput`) — the TUI's own rendering-time ANSI codes are separate and never re-stripped. Alongside stripping: tabs expanded first, then remaining C0 controls (`\x00-\x08`, `\x0b-\x1f`, `\x7f`) deleted outright (LF/CR already consumed by line-splitting).

### 12.9 Clipboard chain
1. External tools in order: `wl-copy` (no args), `xclip -selection clipboard`, `xsel --clipboard --input` — first found on PATH used; text piped via a raw `.NET Process`'s redirected stdin (NOT a PowerShell native-command pipe, which appends a trailing newline); waits ≤3000ms for exit code 0 → `"copied via <tool>"`.
2. Fallback OSC 52: caps text at **72KB** (keeps the LAST 72KB if longer — recent output judged more valuable — sets `capped`); base64-encodes UTF8 bytes; `\e]52;c;<b64>\a`. If `$env:TMUX` or `$env:STY` set, wraps in a DCS passthrough envelope (`\ePtmux;\e<osc>\e\\`, inner ESC doubled — tmux would otherwise swallow it; still requires the user's `allow-passthrough on`). Writes directly to `Console.Out`. Returns `"copied last 72KB via OSC 52"` or `"copied via OSC 52"`.

### 12.10 Frame rendering model
- **Damage diff** (`Write-TuiFrameDiff`): full array of pre-colored line-strings per render; ordinal exact-compare (`-cne`) against the previous frame; only differing rows get `\e[<row>;1H<content>\e[K`. Full `\e[2J` clear + full repaint only when there's no previous frame (first render) or the row COUNT changed (resize) — not on every content rebuild. Whole diff wrapped in synchronized-output (`\e[?2026h…\e[?2026l`) so a partial write is never visible as tearing over SSH.
- **Anim-only frames**: reuse the entire previous full frame's line array verbatim except exactly 2 rows — row 1 (top border, rebuilt for the run-spinner) and the second-to-last row (status line, for the ETA bar/elapsed time). Requires the previous frame to exist with the exact expected row count (H); falls through to a full rebuild otherwise.
- **Cadence/budgets**: `budget=16ms` (~60fps) whenever any animation is live OR something is running (own run or any externally-detected running script); else `budget=0` (idle: render only on Dirty, no polling redraws). A frame is "due" when Dirty, OR the marquee's scroll offset advanced to its next 165ms step, OR (`budget>0`) ≥`budget` ms elapsed since last frame. Hard floor of 15ms between ANY two consecutive frame writes regardless of due-ness. When due, a FULL rebuild happens if: Dirty, marquee stepped, any live animation, the wall-clock second boundary changed (drives 1Hz relative-age refresh, e.g. list ages / header "synced Xm ago"), OR (only while something runs) ≥100ms since the last full build (drives ~10Hz spinner/output-tail/activity-card updates). Every other due frame is an anim-only patch. Main loop additionally sleeps a flat 8ms/iteration regardless (input-poll granularity).

---

## 13. INSTALL.SH + SELF-UPDATE

### 13.1 `install.sh` (bash, `set -euo pipefail`)
- **Prerequisites** (each conditional on absence): `git` via `apt-get update && apt-get install -y git`. PowerShell 7 via official Microsoft apt repo — downloads `packages-microsoft-prod.deb` for the running Ubuntu's `$VERSION_ID` (from `/etc/os-release`) to `/tmp`, `dpkg -i`, removes temp deb, `apt-get update && apt-get install -y powershell`. `python3`+`python3-venv`+`python3-pip` — checked via BOTH `command -v python3` AND `python3 -m venv --help` actually working (python3 can exist without venv module on minimal images).
- **App location resolution**: if invoked FROM a checkout (script's own dir has a sibling `scriptorium.ps1`, via `$BASH_SOURCE[0]`), installs in place (no clone). Else (curl-pipe case, no source file to introspect) resolves via fallback chain: `$SCRIPTORIUM_APP_DIR` → `$PSSCRIPTS_APP_DIR` (pre-rename env var, still honored) → `~/scriptorium` default. If that resolved dir has no `.git` but `~/powershell-scripts-tui/.git` exists (literal pre-rename dir name), redirects to that dir instead (announced) rather than cloning alongside it. Still no `.git` → fresh `git clone`.
- **Repo tracking/self-update** (runs every invocation, even on an already-installed checkout): reads current `origin` URL; if ≠ canonical `https://github.com/yshah-aromatech/scriptorium.git`, repoints `origin` (handles both a foreign remote and specifically the pre-rename repo URL). Always `git fetch origin` then attempts `git pull --ff-only origin main`. On ff failure:
  - If origin had to be repointed just above (old/foreign-history install being converted) → `git reset --hard origin/main` (justified: pre-rename history doesn't share ancestry, ff is structurally impossible).
  - Else (remote was already correct, so ff failure means genuinely diverged LOCAL history — e.g. a dev's own commits) → does **nothing destructive**, prints `"NOTE: could not fast-forward (local changes or commits?) — left as is"`, continues with whatever is checked out.
  - This asymmetry is the safety property: local dev work is never silently discarded by re-running install.sh, but a broken/pre-rename install IS force-converted (no legitimate local history worth preserving there).
- **Config bootstrap**: `config.json.example → config.json` (only if absent, reminder to set scriptsRepo/n8nWebhookUrl); `.env.example → .env` (only if absent, reminder to set GITHUB_TOKEN). Idempotent re-runs never overwrite existing files.
- **Launcher**: writes `~/.local/bin/scriptorium` (bash wrapper: `exec pwsh -NoProfile -File '<APP_DIR>/scriptorium.ps1' "$@"`), `chmod +x`; regenerated (overwritten) every run, so it always points at the currently-resolved APP_DIR.
- **Legacy launcher cleanup**: removes `~/.local/bin/psscripts` if present (file or symlink — checked separately since bare `-e` misses a broken symlink).
- **PATH check**: warns (doesn't modify shell rc files) if `~/.local/bin` isn't on `$PATH`, prints the exact export line.
- **Final message**: `"done. Edit <APP_DIR>/config.json + .env, then run: scriptorium"`.

### 13.2 `psscripts.ps1` compatibility shim
6-line forwarder: `& (Join-Path $PSScriptRoot 'scriptorium.ps1') @args; exit $LASTEXITCODE`. Kept in-repo so any launcher/cron-line/systemd-unit still pointing at the old `psscripts.ps1` path keeps working transparently after a `git pull` on an old checkout, without forcing an immediate `install.sh` re-run. Comment recommends re-running `./install.sh` (for the new launcher) or repointing directly at `scriptorium.ps1` when convenient.

### 13.3 Self-update from inside the app (distinct from install.sh)
- **`U` key / `update_app` MCP tool**: `git pull --ff-only` on the app's own checkout ONLY — no remote repointing, no legacy-dir detection, no force-reset fallback (that machinery lives only in install.sh). Failure just reports the error. Requires a manual restart afterward to pick up new code (pulling new `.psm1` files does nothing to an already-running process holding old modules in memory) — both the TUI status message and the MCP tool's `note` field say this explicitly.
- **Re-running the curl one-liner** (or `cd ~/scriptorium && git pull`) is documented as "also safe" and is the ONLY update path that also picks up prerequisite changes (new PowerShell/git/python requirement) or `config.json.example`/`.env.example` additions — the in-app `U` key never re-runs any prerequisite or config-bootstrap step.

---

## Reference — files read in full

- `/Users/y.shah/development/work/scriptorium/README.md`
- `/Users/y.shah/development/work/scriptorium/scriptorium.ps1`
- `/Users/y.shah/development/work/scriptorium/install.sh`
- `/Users/y.shah/development/work/scriptorium/config.json.example`
- `/Users/y.shah/development/work/scriptorium/.env.example`
- `/Users/y.shah/development/work/scriptorium/psscripts.ps1`
- `/Users/y.shah/development/work/scriptorium/night-owl-dark`
- `/Users/y.shah/development/work/scriptorium/src/Core.psm1`
- `/Users/y.shah/development/work/scriptorium/src/Scripts.psm1`
- `/Users/y.shah/development/work/scriptorium/src/Deps.psm1`
- `/Users/y.shah/development/work/scriptorium/src/Runner.psm1`
- `/Users/y.shah/development/work/scriptorium/src/Cron.psm1`
- `/Users/y.shah/development/work/scriptorium/src/Mcp.psm1`
- `/Users/y.shah/development/work/scriptorium/src/Tui.psm1` (full 2395 lines)
- `/Users/y.shah/development/work/scriptorium/tests/Core.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/tests/Cron.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/tests/Deps.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/tests/Runner.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/tests/Scripts.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/tests/Mcp.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/tests/Tui.Tests.ps1`
- `/Users/y.shah/development/work/scriptorium/.github/workflows/ci.yml`
- `/Users/y.shah/development/work/scriptorium/.gitignore`

This report is exhaustive against the current codebase as read; nothing was summarized away — every keybinding, config key, file format, algorithm, and tool schema documented above traces to specific lines in the files listed. Use it directly as the Go-rebuild parity checklist.

---

## Deliberate divergences (Go rebuild)

1. **Numeric config keys reject JSON `bool`/`null`.** PS's `-as [double]` cast coerces `true`→`1` and `null`→`0` (a latent PS bug — a typo'd boolean or a stray `null` silently becomes a real numeric value instead of warning). The Go decoder only accepts a JSON number or a numeric string and warns+defaults on anything else, `bool`/`null` included.
2. **Numeric config keys reject non-finite or out-of-int32-range values with a warning.** PS defers to a runtime `[int]` cast that throws only when the value is actually used. Go's `int()` conversion of `NaN`/`±Inf`/an overflowing float is undefined/platform-dependent, so `jsonNumber` rejects those up front (warn+default) rather than risk garbage config values reaching the rest of the app.
3. **Secret redaction replaces longest-first, deterministically.** PS loops a `HashSet<string>` in enumeration order (nondeterministic across runs); when one registered secret is a substring of another, which one "wins" the overlap is unspecified in PS. Go always redacts the longest match first, so an overlap deterministically redacts *more* text (the longer secret's full span), never less.
4. **ZWJ/grapheme display widths follow uniseg's segmentation.** Already noted in `testdata/psfixtures/README.md` — PS's .NET-based width heuristic and Go's `uniseg` package can disagree on exotic ZWJ sequences; uniseg's Unicode-standard segmentation is treated as authoritative going forward.
5. **Cron steps larger than int32 are accepted when they fit the platform int.** Cron step values exceeding int32 but fitting int64 are parsed successfully and expand to just the base value (e.g., `*/3000000000` on a 64-bit platform); PS's `[int]` cast throws an uncaught OverflowException that crashes the caller. Steps beyond int64 are rejected as unparseable via strconv.
6. **README truncation is rune-based, not UTF-16-code-unit-based.** `Get-StoScriptDetail`'s 16384 cap is PS's `.Length`, i.e. UTF-16 code units (a character outside the BMP counts as 2). Go's `scripts.readmeCap` counts runes (Unicode code points) instead, so the two disagree by exactly the astral-character count for a README containing any — each such character counts once in Go, twice in PS. Both stay well clear of splitting a single code point's own UTF-8 bytes.
7. **`envfile.Read`/`envfile.Keys` dedupe keys case-sensitively.** PS's `[ordered]@{}` hashtable backing `Read-StoEnvFile` is case-INsensitive: two keys differing only by case (e.g. `FOO=1` then `foo=2`) collapse to ONE entry, keeping the first-appearance *position* but the LAST spelling for both the displayed key and the value (verified against live pwsh). Go's map-based `Read`/`Keys` treat differently-cased spellings as distinct keys, so a `.env`/`.env.example` with such a collision produces more entries in Go than PS would.
8. **`sortNamesCI`'s case-only tiebreak is a fixed lowercase-first rule, not PS's culture-aware `Sort-Object`.** For two discovery candidates whose names differ only by case (e.g. folders `Foo` and `foo`), Go orders the lowercase spelling first — chosen to match `[string]::Compare("A","a") == 1` (verified against live pwsh), a defensible proxy since PS's actual `Sort-Object Name` is a *stable* sort whose real tie order for same-case-fold names depends on `Get-ChildItem`'s OS-returned enumeration order, not a secondary string comparison. The Go rule is deterministic and cross-platform; it need not literally match a live PS run's incidental tie order on every filesystem.
9. **A script.json `entry` that is itself a same-folder symlink pointing outside the folder bypasses the containment guard — in both PS and Go.** Both `Resolve-StoEntry`'s `Resolve-Path -LiteralPath` and Go's `filepath.Abs`+`filepath.Clean` operate on the symlink's own in-folder path, not its resolved target (verified against live pwsh: `Resolve-Path` on a same-folder symlink pointing outside returns the symlink's own path, not the target it points to) — so the prefix check against the folder root passes for both, and whatever the symlink points at is what actually executes. This is inherited PS behavior, not a Go-introduced gap; noted here informationally only.
10. **`startedAt`/`durationSec` include first-run venv creation on Python scripts.** PS builds the `ProcessStartInfo` — venv creation and the `pip install --upgrade pip` included — and only then calls `New-StoHandle`, which stamps `StartedAt` immediately before `$proc.Start()`. Go stamps `s.startedAt` in `Runner.Start` before `buildCmd`, which is where `ensureVenv` runs, so the first run of a Python script with no venv yet reports a `startedAt` earlier than PS would and a `durationSec` larger by the venv-creation time (tens of seconds on a cold `python -m venv` + pip upgrade). Every subsequent run of that script is identical in both, since the venv already exists and `ensureVenv` is skipped. Deliberate: one timestamp taken at the top of `Start` is what makes the log-file name, the history row and the skipped-run synthetic handle all agree, and a run's own setup cost is arguably part of its duration. The timeout is unaffected — its timer starts in `supervise`, after `cmd.Start()`, in both implementations.
11. **CPU% denominators differ inside a CPU-limited container.** Both implementations divide tree CPU by a processor count to report percent-of-machine, but the two runtimes count differently: Go's `runtime.NumCPU()` reports the CPUs available to the process's *affinity mask*, ignoring cgroup CPU quota, while .NET's `[Environment]::ProcessorCount` (PS's divisor) honours the cgroup v1/v2 quota since .NET Core 3.0. On bare metal, a VM, or a container with no `--cpus` limit the two agree. Under a quota — `docker run --cpus=2` on a 16-core host — PS divides by 2 and Go by 16, so the same workload reports an eight-fold lower `cpuAvgPercent`/`cpuMaxPercent` in Go, and Go's clamp to 100 effectively never engages. Not reproduced because reading the cgroup quota means parsing `/sys/fs/cgroup/cpu.max` (v2) plus the v1 pair and handling `max`/unset/nested-namespace cases — a real chunk of platform code for a deployment shape (scriptorium under a CPU-capped container) that the app does not currently have. Revisit if the app is ever packaged for one.
12. **A timeout can fire in the sliver between child exit and pipe-EOF, where PS's `HasExited` guard would suppress it.** PS gates its whole timeout branch on `-not $proc.HasExited` (`Update-StoRun`), so a deadline coming due after the child has already exited is never classified a timeout — .NET keeps `HasExited` current independently of the output streams. Go has nothing equivalent to consult during the supervisor's pipes-open phase: `cmd.Wait()` is the only thing that reaps, it must not run while the pipe readers are still on the pipes (Wait closes them), and `kill(pid, 0)` cannot stand in for it because a zombie answers it exactly as a live process does. So in the instant between the child exiting and its pipes reaching EOF, a firing timer classifies the run `timeout` where PS would have let it finish. The window is the pipe-drain latency — sub-millisecond in practice — and it closes entirely once the loop reaches its wait phase, where `waitDone` is polled with priority before either deadline branch acts (the same guard, expressed with the reap instead of `HasExited`). Not reproduced because the only ways to close it in the pipes-open phase — parsing `/proc/<pid>/stat` for state `Z` (Linux-only) or adding a second reaping path alongside `cmd.Wait()` — buy a sub-millisecond window at the cost of platform-specific process bookkeeping in the one place the app most needs to stay simple.
13. **Go runs a post-reap snapshot pass over the process tree; PS does no post-exit walk at all.** `Stop-StoRun` returns immediately on `$proc.HasExited`, so once the root is gone PS never touches the tree again: a child that ignored SIGTERM and was reparented to init, or a grandchild that left the group with `setsid`, simply survives. Go's `killTree` keeps going — after the reap it still walks the sampler's last pid snapshot and SIGKILLs each member that `/proc` confirms is still there. That is a deliberate improvement (it is what makes `TestSetsidEscapeeIsKilledFromTheSnapshot` possible), bounded on both sides: it is Linux-only, since off `/proc` there is nothing to confirm against; and since the r3 fix-wave it **excludes the root's own pid**, which is precisely the pid the kernel is free to recycle the moment it is reaped — the same reasoning that takes the group signals off the table post-reap. Where the sampler never ticked at all (a run killed inside its first monitor interval) the pass is skipped entirely after a reap rather than re-walking from a possibly-recycled root pid, which lands exactly on PS's behaviour. What remains is a one-monitor-interval staleness window on the non-root pids: `/proc` proves the pid exists, not that it is still the same process.
14. **Go redacts the runner's own notice lines; PS emits them raw.** The skip notice (`skipped: <name> is already running (pid N)`) and the start-failure notice (`failed to start: <exec error>`) go through `Sec.Redact` in Go, because they route through the same `emitLine` chokepoint as child output. PS's `Update-StoRun` adds both to its returned lines directly, without `Hide-StoSecret` (the redaction call sits only in the stream-drain loop above it). In practice the two agree — a script name and an exec error rarely contain a registered secret — but where they differ Go redacts and PS does not, which is the safe direction, and having exactly one function that can put a line on the wire is worth more than reproducing the gap. The timeout notice routes through the same chokepoint for the same reason; it interpolates only a number, so there is nothing there to redact either way. None of the three reach the log file in either implementation.
15. **A repos-entry field's non-string, non-number JSON shapes decode to `""`, where PS stringifies anything.** PS's `"$($e.url)"` interpolation stringifies whatever type a `repos` entry field holds — verified against live pwsh: a JSON `true` becomes the PS string `"True"`, and a JSON array becomes its elements space-joined (e.g. `[1,2]` -> `"1 2"`). Go's `config.repoScalar` (the tolerant per-field decoder behind `decodeRepos`) only reproduces the string-or-number cases from the reviewer-verified table; a bool, array, or object field decodes to `""` instead of PS's stringified form. Not reproduced because no real config shape needs it — `repos` entries are user-authored JSON where a url/name/branch as a bool or array isn't a realistic typo (unlike the verified `url:123` case, which numeric-vs-quoted-string config typos make plausible).
16. **`missed-state.json` writes are flock-serialized; PS's are not.** `missed.Check` takes `LOCK_EX|LOCK_NB` on the state file and silently returns nothing when another sweep holds it, so two concurrent cron boots can never double-send one missed alert. PS acknowledges the race in a `ponytail:` comment (`Runner.psm1`) and relies on n8n-side dedupe; the spec's §3 concurrency table mandates this upgrade. The cost is the inverse edge: under contention one Go sweep is skipped entirely (the winner alerts; the loser retries on its next boot), which is strictly better than PS's duplicate.
17. **`--history` renders an empty `when` column for an unparseable `startedAt`; PS falls back to the raw string.** PS's `$started = $h.startedAt -as [datetime]` fallback prints whatever junk the row held (misaligning its own columns); Go prints `""` in the same width. Unreachable from rows either implementation writes — `startedAt` is always machine-stamped — so this only differs on a hand-corrupted history file.
18. *(retired — P10 shipped the TUI, and the bare-invocation stub was this entry's last clause; nothing else lived here, since its `--mcp`/`--install-mcp-service` and dependency-auto-install remarks were pointers to §11.9-11.12 and entry 20 rather than divergences of their own. The number stays so 19/20/21 do not move.)*
19. **The managed block's lines are ordinally sorted; PS sorts culture-aware.** `Save` uses `sort.Strings`; `Save-StoSchedules` uses `Sort-Object`, whose default comparer is culture-aware, so for `[A-Za-z0-9._-]` names the two orders differ on case and punctuation weighting (PS: `_tmp, a-b, A1, a1, ab, apple, Backup`; Go: `A1, Backup, _tmp, a-b, a1, ab, apple`). Nothing observes the order — cron is order-insensitive and both readers return a map — so the only effect is that a PS-written and a Go-written block of the same schedules are not byte-identical. Not reproduced because .NET collation in Go requires `golang.org/x/text/collate`, a new dependency the rebuild forbids for a cosmetic ordering. *(Same entry: the OpenRouter transport-error text inside the byte-pinned `OpenRouter request failed: <msg>` wrapper is Go-shaped, not `Invoke-RestMethod`-shaped — Go emits `response status code does not indicate success: 500 (Internal Server Error)` where PS 7 emits the same sentence capitalized and terminated with a period, and dial-failure text differs more. The wrapper is byte-exact; `<msg>` was never pinned.)*
20. **The PowerShell dependency scan is Go-only degraded and cached; PS has neither.** `deps.Scanner.ScanPS` shells out to an embedded pwsh script for the real AST-based scan; if pwsh can't even be started (missing/unrunnable — not merely a script-level failure), Go degrades to a regex-based name scan instead of failing the whole `--run`: dep *names* only (from `#Requires -Modules` bare names, `using module X`, and `Import-Module`/`ipmo` first-argument simple literals, with the builtin/path exclusions and gallery name-map applied Go-side), `Missing` always empty (no pwsh means no installs are possible anyway, so reporting "everything missing" would only invite a doomed install attempt), `Degraded=true`, and a warning printed to stderr — PS has no such fallback; it simply *is* pwsh, so this failure mode cannot occur there. Separately, `Scanner` keeps an in-process cache keyed by entry path, invalidated on `(size, mtime)` drift, so two scans of the same unchanged script within one process cost one pwsh invocation — PS's `Get-StoScriptDeps`/`Get-StoMissingDeps` rescan from scratch (a fresh AST parse + `Get-Module -ListAvailable` walk) every single call, with no cache at any layer. Known ceiling: an edit that lands within the same filesystem-mtime second AND happens to leave the file the same byte size can be served a stale cached scan once, until the next mtime tick — accepted because the cache is process-lifetime only (a fresh `--run` invocation starts a fresh `Scanner`) and the window is sub-second. The cache key only observes the entry file, not `moduleDir`'s contents or the system's installed modules — both of which an install command changes without touching `entry` at all — so every caller that runs an install through a `Scanner` it intends to keep using MUST call `Scanner.Invalidate(entry)` afterward (`internal/cli`'s `--run` flow does, immediately after its install command exits); a fresh `--run` process has nothing to invalidate, since its `Scanner` is new, but this becomes load-bearing the moment a long-lived `Scanner` is shared across actions (P9's MCP server, P10's TUI) and must not be dropped when either wires an install path against the same `Scanner`. A third Go-only deviation in the same flow: a scan ERROR (not pwsh-absence — e.g. an unreadable entry file) is swallowed by `--run`, which proceeds with no dep install, where PS's `$ErrorActionPreference = 'Stop'` at the top of `scriptorium.ps1` would abort the whole run with a non-zero exit before the script ever started — deliberate: a transient scan hiccup should not kill a scheduled cron run whose script may not even need the dep that failed to scan.
21. **`cron.Set`/`Remove` read the crontab once; PS reads it twice.** PS's `Set-StoSchedule` calls `Get-StoSchedules` (whose failed read silently yields an empty map) and then `Save-StoSchedules` (whose own read is wipe-guarded) — so a transient read failure during the first read with a healthy second read rewrites the managed block containing ONLY the entry being set, silently dropping every sibling schedule. Go's `Set`/`Remove` take one guarded read, mutate that snapshot, and save that snapshot: the same transient failure surfaces as an error and writes nothing. Deliberately safer than PS (P9 exposed `set_schedule` to remote agents, making the wipe scenario reachable); the regression test reproduces the PS-shaped wipe against the old code.
22. **Clipboard writes send OSC 52 unconditionally, then ALSO a local tool when present; PS prefers local tools and uses OSC 52 as the fallback.** Go inverts the order deliberately: this is an SSH-first app, and OSC 52 is the only channel that reaches the local Mac's clipboard through SSH+tmux — a headless server's xclip would "succeed" into a clipboard nobody can see. tmux wrapping uses x/ansi's upstream TmuxPassthrough (byte-identical to the PS-proven `\ePtmux;…\e\\` wrapper) behind the same TMUX/STY check; bubbletea v2.0.9's own SetClipboard does no tmux wrapping (verified empirically). The 72KB cap and no-trailing-newline rules are preserved.
23. **`theme` is a Go-only config key.** Selects a registered palette (night-owl default, catppuccin/gruvbox/tokyo-night alternates); PS has no such key and would print its unknown-key warning on a shared config, so the key is documented as post-cutover-only. Unknown value → warning + Night Owl.
24. **Per-view key-hint lists are load-bearing for palette routing.** The palette resolves a shared key's owner per view via the view's own hint list (a key a view handles but does not hint would be switched away from). PS has no palette, so no counterpart; the invariant is pinned by TestFooterShowsOnlyLiveKeys plus the palette owning-view tests. Any new handled key MUST appear in its view's hint list.
25. **`colorMode: auto` honours the wider no-colour ecosystem; PS's `auto` only ever chose truecolor or 256.** §12.4 is `$env:COLORTERM -match 'truecolor|24bit'` → truecolor, else 256 — a terminal that wants no colour at all has no way to say so, and PS paints it anyway. `theme.Profile` delegates `auto` to lipgloss v2's `colorprofile.Env` (spec §2's "truecolor detection delegated to lipgloss v2"), which implements exactly that COLORTERM rule and additionally respects `NO_COLOR`, `CLICOLOR`/`CLICOLOR_FORCE`, `TERM=dumb` and terminfo. PS's floor is kept on top — `auto` never lands *below* 256 merely because `TERM` is unhelpful — but a terminal that explicitly refuses colour is now believed, so `auto` can resolve to Ascii where PS would have emitted 256-colour SGRs. Pinned by `TestProfile` ("auto honours NO_COLOR" → Ascii, "auto floors a bare TERM at 256"). `colorMode: 'truecolor'`/`'256'` still force, unchanged.
26. **§12.2's focused-border role is folded onto Accent (Magenta), not Blue.** The design's semantic-token contract is thirteen tokens (`Bg Fg Muted Border Accent Success Warning Danger Info SelBg CardBg RuntimePS RuntimePy`), and §12.2 asks two different roles for a colour that has to share one: `borders=Border(Blue focused)` and `key-hints=Magenta`. Both land on `Accent`, which Night Owl fills with Magenta (`#c792ea`), so a focused pane's rule and title read in the same voice as the key hints — "you are here / press this" — and Blue (`#82aaff`) is left meaning exactly one thing in the UI, the `ps` runtime tag. Every other §12.2 role is honoured hex-for-hex from `Core.psm1` (success=Green, failure/missed=Red, killed/timeout/skipped=BrYellow, scheduled/queued=Cyan, python=Yellow, powershell=Blue, muted=BrBlack, selection=SelBg, zebra ground=CardBg). The only Core.psm1 colour left with no role is BrCyan (`#7fdbca`); White survives as CardBg's blend target. Observable effect: a focused pane border is Magenta where the PS TUI drew Blue.
