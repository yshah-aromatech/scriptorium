# Go Rebuild — Package Architecture Draft (generated 2026-09-01)

Drafted by the architecture agent against the full PowerShell source; referenced by
`2026-09-01-go-rebuild-design.md`. This is the implementer-level detail behind the
spec's Architecture section.

## Package layout

```
cmd/scriptorium/main.go          single binary; builds config, wires cobra root, dispatches
internal/
  envfile/    .env + .env.example parsing (PS quoting rules), doc-comment reader
  secret/     secret registry + Redact(string) + redacting line writer
  config/     config.json load, unknown-key/type warnings, path resolution, dataDir migration
  lockfile/   per-script PID lock files: acquire (O_EXCL), probe, list-live, stale reclaim
  procstat/   /proc tree walk, jiffy+RSS snapshot, CPU%/mem sampler
  cron/       field parser, Next, Prev, Validate, crontab managed-block read/write, NL->cron
  history/    history.jsonl append/read (PS-era tolerant), last-status map, log tail
  webhook/    n8n POST + retry + dead-letter queue flush (.flush rename protocol)
  scripts/    repo sync (git shellout), discovery, entry resolution, script.json, detail
  deps/       PS dep scan (pwsh shellout) + python import scan/venv, install command builders
  runner/     process launch, line streaming, sampling, timeout, pgroup kill, classify, finalize
  missed/     missed-fire detector + missed-state.json + one-shot 'missed' webhook
  retention/  prune orchestration: history rows, their logs, aged logs, frequent-script policy
  migrate/    first-run migrations: crontab block rewrite, legacy markers, systemd unit swap
  app/        the service facade every frontend uses (one implementation, no interfaces)
  tui/        bubbletea program: root model, sub-models, messages, theme, components
  cli/        cobra commands and exit-code mapping
  mcp/        JSON-RPC dispatch (pure), tool implementations, HTTP server, systemd unit
```

Dependency direction (arrows point at the importee, no cycles):

```
envfile, secret, procstat          leaves
config      -> envfile, secret
lockfile    -> config
history     -> config
cron        -> config
webhook     -> config, secret
scripts     -> config, secret, envfile
deps        -> config, scripts
runner      -> config, secret, scripts, lockfile, history, webhook, procstat
missed      -> config, cron, history, lockfile, webhook
retention   -> config, history, cron
migrate     -> config, cron, scripts
app         -> everything above
tui, cli, mcp -> app  (+ config for read-only values; tui also -> its own theme pkg)
```

Hard rule enforced by an import-lint test: nothing in the domain packages may import
bubbletea, lipgloss, bubbles, cobra, or net/http handlers. Every domain package is
testable with `go test` and a `t.TempDir()` data dir.

`internal/app` is the only orchestration layer — a struct with methods, not an
interface (one implementation exists and one will exist):

```go
type App struct {
    Cfg   *config.Config
    Paths config.Paths
    Sec   *secret.Registry
    Runner *runner.Runner
    Hist  *history.Store
    Hook  *webhook.Client
    Cron  *cron.Crontab
    Scan  *deps.Scanner
}
func Open(appDir string) (*App, []string /*config warnings*/, error)
func (a *App) Scripts() ([]scripts.Script, error)
func (a *App) Run(ctx context.Context, spec runner.Spec) (*runner.Handle, error)
func (a *App) Sync(ctx context.Context, onLine func(string)) error
func (a *App) MissedSweep() ([]missed.Miss, error)
func (a *App) Prune(force bool) error
```

## Concurrency model

The domain layer is blocking and synchronous by default; concurrency lives at the
edges. Nothing under `internal/` except `runner` starts a goroutine.

**Run lifecycle.** `runner.Start` spawns exactly four goroutines and returns one
channel: stdout reader, stderr reader (each `bufio.Reader.ReadString('\n')` into a
shared chan, cap 256), sampler (ticker at monitorIntervalMs), and a supervisor that
selects over lines/samples/timeout/`cmd.Wait()`, owns the log writer and the event
channel, and on exit does classify -> history append -> unlock -> webhook -> EvDone ->
close(Events). The unlock sits between the append and the webhook deliberately: the
row must land before a queued re-run can append its own (last-status-wins), but the
webhook may retry and queue for seconds and must not hold the lock that long.

One event channel, one tagged union:

```go
type Event struct {
    Kind   EventKind // EvLine | EvSample | EvDone
    Line   string    // already redacted
    Sample Sample
    Result *Result
}
```

`RunToCompletion(ctx, spec, onEvent)` wraps it for CLI and MCP — the analogue of
`Invoke-StoRunToCompletion`.

**Bubble Tea bridge — two idioms only, plus a documented ban.**
- Periodic work -> `tea.Tick`, self-rescheduling from Update. Three tickers: 1 Hz
  (relative ages, next-run hints, status fade), 2 s (lock-dir poll), 60 s (missed sweep).
- Channels/blocking calls -> a `tea.Cmd` using a **batched drain reader** (block for
  the first event, then take up to ~512 already-queued, return the batch as one Msg;
  Update appends and re-issues the drain Cmd). Ordered, back-pressured, cannot fall
  behind a chatty script the way one-Msg-per-line would. Same pattern serves repo sync
  and streamed system tasks.
- `program.Send` is **not used** (sole permitted future exception: a SIGTERM handler).

**File-level serialization** (idiom per file):

| File | In-process | Cross-process | Idiom |
|---|---|---|---|
| history.jsonl append | mutex on Store | POSIX O_APPEND | open O_APPEND, ONE Write of line+"\n" (never bufio — split writes interleave rows) |
| history.jsonl prune | same mutex | flock on `<dataDir>/.prune.lock` | flock, re-read, filter, write .tmp, os.Rename; skip if held; keep .last-prune throttle |
| webhook-queue.jsonl | mutex | `os.Rename(qf, qf+".flush")` AS the mutex | keep the PS protocol verbatim (PS and Go interlock during migration); keep 10-min stale reclaim + 50/flush cap |
| missed-state.json | mutex | flock on the state file | fixes the PS double-alert race without changing the format |
| `<script>.lock` | n/a | O_CREATE\|O_EXCL is the mutex | keep the 10-second freshness backoff before stale reclaim |
| crontab | mutex | none available | crontab -l / crontab -; keep the "abort write if read failed" guard — data-loss guard, never simplify |

## Run pipeline

```go
type Spec struct {
    Script    scripts.Script
    Trigger   string            // "manual" | "cron" | "mcp"
    ExtraArgs []string
    ExtraEnv  map[string]string
    Timeout   time.Duration     // 0 = none; caller resolves override > script.json > config
}
type Handle struct {
    Name   string
    Events <-chan Event
    Kill   func(reason string)  // "killed" | "timeout"
}
```

- **Launch build**: one function + a switch (no runtime interface). Base env =
  os.Environ(); overlay per-script .env then ExtraEnv; every value registered as a
  forced secret BEFORE process start. PS: pwsh -NoProfile -NonInteractive -File +
  PSModulePath prepend. Python: ensure venv (create + pip upgrade if missing), run
  venv python, PYTHONUNBUFFERED=1. Both: Dir = script dir, SysProcAttr{Setpgid: true}.
- **Streaming**: pipes + bufio.Reader.ReadString (NOT a pty — a pty merges streams,
  triggers pwsh progress ANSI, changes buffering; NOT bufio.Scanner — ErrTooLong on
  long lines silently truncates the stream). Strip trailing \r.
- **Redaction chokepoint**: exactly one `sink.emit(raw)` sees unredacted text; log
  file written redacted; webhook tail read back FROM the log; MCP output = same tail.
  Redact sorts secrets longest-first (deterministic; fixes PS HashSet order).
- **/proc sampling**: hardcode userHZ=100 (the /proc ABI constant — no getconf),
  os.Getpagesize(), CPU = delta/userHZ/dt*100/NCPU clamped [0,100], dt > 0.2s guard.
  The sampler returns its pid snapshot so the killer reuses it.
- **Kill**: SIGTERM to -pgid; wait <=3s on cmd.Wait; then SIGKILL to -pgid AND to
  every snapshot pid still present in /proc (catches setsid escapees + reparenting).
- **Classification**: skipped (lock held, synthetic finished handle with PS-identical
  message), killed/timeout win over exit code, exit 0 = success, else failure; exec
  error = failure/-1.
- **Finalization**: runId UUID; timestamps via a Stamp type marshalling
  `2006-01-02T15:04:05.000Z` UTC; ALL rounding through
  `round1(x) = math.RoundToEven(x*10)/10` (PowerShell [Math]::Round is banker's
  rounding; Go math.Round is not — this matters for byte-comparable payloads);
  series max-of-bucket downsampled to 60; history append BEFORE unlock, then webhook.

## TUI decomposition

Root model owns size, *app.App, mode, focus, and sub-models; layout math at the root,
rendering delegated.

Bubbles mapping: script list -> bubbles/list + custom ItemDelegate (badges, runtime
tag, age, marquee); output -> bubbles/viewport + a ported wrap buffer (Lines/Wrapped/
WrapSrc — drag-copy rejoin depends on WrapSrc); .env editor -> bubbles/textarea;
prompts -> one bubbles/textinput with a Kind field; help + footer -> bubbles/help over
one key.Binding set (kills the two hand-maintained lists that can drift); spinner ->
bubbles/spinner; ETA -> bubbles/progress (its default partial-cell creep replaces the
hand-rolled eighth-block bar); history -> custom rowsview (~80 lines: viewport +
selection + row func — bubbles/table can't render per-cell heat-colored sparklines).

Custom pure render funcs (`func(data, width, theme) []string`, golden-testable):
details card, activity card, recent-runs card, sparklines with heat ramp, status
badges, status bar.

Message taxonomy: runStartedMsg / runEventsMsg{batch,closed} / runDoneMsg /
runQueuedMsg; tickMsg / lockPollMsg / missedMsg; syncEventsMsg / syncDoneMsg /
taskEventsMsg / taskDoneMsg; scriptsLoadedMsg / depsScannedMsg / historyLoadedMsg /
logLoadedMsg / cronParsedMsg; clipboardMsg / statusMsg / errMsg — plus Bubble Tea's
own WindowSize/KeyPress/Mouse* messages.

Drag selection: anchor/extent live in the output pane model; rendered as an
inverse-video span into the content before viewport.SetContent. Clipboard: v2
tea.SetClipboard (OSC 52) with wl-copy/xclip/xsel exec fallback (stdin verbatim, no
trailing newline) and the 72 KB cap preserved; verify v2's tmux DCS wrapping early and
keep the hand-rolled wrapper behind a TMUX/STY check as fallback.

Theme: semantic tokens (Bg Fg Muted Border Accent Success Warning Danger Info SelBg
CardBg RuntimePS RuntimePy) -> Palette -> prebuilt lipgloss styles. Night Owl is the
registered default with the exact hexes from Core.psm1; adding a palette is one
Register call. lipgloss/v2 owns downsampling (ConvertTo-Ansi256Index disappears);
config colorMode maps to profile override.

## Compat / migration

Only `internal/migrate` is a dedicated compat package (one-shot migrations): crontab
block rewrite (both marker generations recognized; KEEP the CLI spelling
`cd '<appDir>' && '<binary>' --run '<name>' --cron >> '<log>' 2>&1` so PS can still
read the Go-written block; full crontab backup to `<dataDir>/crontab.bak.<RFC3339>`
before first rewrite; abort whole write if read failed; keep %-escaping), dataDir
migration (~/.psscripts), scripts layout migration, systemd unit swap
(psscripts-mcp retirement + ExecStart rewrite + daemon-reload + restart).

Tolerant readers live in their owning packages: config (unknown-key + numeric-type
warnings with PS-identical strings, warnings returned not logged), history (rows may
lack runtime/repo/runId/resources/series; Stamp accepts fffZ / Z / offset forms;
re-marshalling a PS-era row must not invent fields), envfile (exact PS semantics:
first '=' at index >= 1, matching-quote strip only, last key wins, doc-comment
reader), cron (both marker pairs on read; Ok-vs-empty distinction preserved).

**Parity-test strategy**: (1) hack/gen-fixtures.ps1 run once against the PS app ->
testdata/psfixtures/ (mixed-era history, webhook queue, missed-state, 3 crontab
fixtures, config corpus, .env corpus, run log, recorded MCP request/response pairs);
(2) round-trip tests (parse -> re-marshal -> parse, semantic equality; Go row field
set superset of PS row); (3) reverse-direction CI job — PS readers consume Go-written
rows/payloads/blocks (pwsh-gated) — the test that protects the migration window;
(4) cron truth table: ~400 expressions x 12 timestamps frozen from Get-StoCronNext/
Prev as CSV; (5) rounding fixture (200 floats through [Math]::Round); (6) lock
interop both directions against live foreign PIDs; (7) teatest golden frames at
80x24 / 120x40 / 200x60 per overlay mode, COLORTERM set and unset.

## Risk register

1. **PS AST dep scanning + param() parsing** — shell out to pwsh with ONE embedded
   scanner script (go:embed) emitting deps + param block as one JSON doc; cache on
   (path,size,mtime); degraded regex fallback with a VISIBLE warning when pwsh absent.
   Never reimplement a PowerShell parser.
2. **Rounding/format parity** — round1 via RoundToEven; Format-StoDuration /
   Format-StoRelativeTime ported character-for-character with table tests.
3. **Vixie cron rules** — port the field expander literally (~150 lines) against the
   frozen truth table: dom/dow OR-rule when both restricted, `5/15` = from-5-step-15,
   dow 7==0, names, @reboot never fires. robfig/cron has no Prev and diverges on
   edges; gronx acceptable only if it passes the full truth table — the table decides.
4. **Redaction coverage** — one chokepoint; longest-first ordering; documented
   inherited limits (per-line, re-encoded values); end-to-end leak test greps log +
   history tail + webhook body + MCP output for an echoed .env value.
5. **256-color fidelity** — delegate to lipgloss/v2 profiles; exact index parity is
   cosmetic; test NO_COLOR / TERM=xterm-256color / tmux in goldens; keep colorMode
   override.
6. **OSC 52 + drag selection in tmux** — selection re-implemented in the output pane
   (WrapSrc rejoin); verify v2 clipboard tmux wrapping early; DCS fallback kept.
7. **MCP edge cases** — 202 empty body for notifications (explicit, Go defaults 200);
   reject JSON-RPC batch arrays (parity, n8n never batches); auth before body read;
   bounded read regardless of Content-Length; 405/413/-32700/-32601/-32602/-32603
   matrix replay-tested; single-flight tool mutex; redact error strings.
8. **Crontab rewrite blast radius** — abort on failed read; backup before first
   rewrite; byte-preserve everything outside the block; print before/after diff at
   migration; upgrade notes say retire the PS install in the same session.

## Build order (each phase = one PR; 1 and 2 parallel after 0)

P0 scaffold+fixtures -> P1 envfile/secret/config -> P2 cron engine -> P3 history/
retention/locks -> P4 scripts discovery+sync -> P5 runner/procstat/webhook ->
P6 CLI + missed (FIRST SHIPPABLE BINARY — cron can point at it) -> P7 crontab+migrate
-> P8 deps -> P9 MCP -> P10 TUI foundation -> P11 TUI depth -> P12 packaging+cutover.
Per-phase gates are listed in the main design doc.

Deliberately skipped: runtime plugin interface (two runtimes, one switch — add at the
third), config hot-reload, a cron dependency and an MCP-SDK-shaped abstraction where
parity is the constraint, and a separate compat package for tolerant readers.
