# Scriptorium Go Rebuild — Design & Scope

**Date:** 2026-09-01 · **Status:** awaiting owner approval · **Target:** Go 1.25 + Bubble Tea v2

A ground-up rebuild of scriptorium (currently PowerShell 7, ~7.5k lines) as a single
static Go binary, keeping every capability — running PowerShell and Python scripts
through the full pipeline — while rebuilding the TUI's information architecture.

Companion documents in this directory:
- `2026-09-01-go-rebuild-parity-inventory.md` — exhaustive feature/contract checklist
  extracted from the PS source (the parity bible; nothing ships until it's covered).
- `2026-09-01-go-rebuild-architecture.md` — package layout, concurrency model, run
  pipeline, compat layer, risk register (implementer-level detail).

---

## 1. Decisions (settled with the owner, 2026-09-01)

| # | Decision | Choice |
|---|---|---|
| 1 | Compatibility | **Full drop-in**: same `~/.scriptorium` data dir + file formats, config keys, webhook payloads, MCP tool names/schemas, CLI flags/exit codes. First run auto-migrates the crontab managed block from pwsh invocations to the Go binary (with full crontab backup). |
| 2 | Repo | Same repo (`yshah-aromatech/scriptorium`), developed on `go-rewrite` branch; at cutover Go replaces the root on main, PS tagged `powershell-final`. |
| 3 | UI/UX | **Full redesign license**, exercised as IA option A (workflow views, §4); every current capability survives. |
| 4 | Cuts | **None.** Lint, apt update, NL→cron, .env editor, MCP — all carried. |
| 5 | Distribution | goreleaser → GitHub Releases (linux amd64+arm64, CGO_ENABLED=0); install.sh fetches the binary; in-TUI `U` = binary self-update. |
| 6 | Phasing | Core-first: P1–P9 headless core proves parity side-by-side on real data (cron duty migrates at P6); TUI next; MCP/extras; cutover. |
| 7 | Testing | Unit (race detector) + teatest/v2 golden frames (pinned size + ascii profile) + PS-generated parity fixtures, incl. a reverse-direction CI job where PowerShell reads Go-written files. |
| 8 | Workflows weighted | All four are daily drivers: watching live runs, fleet status at a glance, failure forensics, schedule/config management. |
| 9 | Layout | **A — workflow views** (mockup approved): Fleet / Run / History / Schedules as distinct screens. |

## 2. Stack (from ecosystem research, verified Sept 2026)

| Concern | Choice | Note |
|---|---|---|
| TUI | `charm.land/bubbletea/v2` v2.0.9+, bubbles v2.2.1, lipgloss v2.0.6 | v2's renderer does cell-diff damage rendering, synchronized output (mode 2026), 60fps default (`tea.WithFPS`), native OSC 52 clipboard — the entire rendering layer we hand-built in PS is framework-native. Go 1.25 required. |
| CLI | Cobra (+ fang optional) | Legacy flag spellings preserved as a compat layer over subcommands. |
| Cron | Port the PS field expander literally (~150 lines) | The frozen truth-table fixture (400 exprs × 12 timestamps from the PS engine) is the arbiter; `adhocore/gronx` (has native PrevTick) is an acceptable substitute ONLY if it passes that fixture 100%. robfig/cron is out (no Prev, edge divergence). |
| MCP | Hand-rolled JSON-RPC dispatch + net/http (~400 lines) | Parity with recorded PS request/response fixtures wins over the official `modelcontextprotocol/go-sdk`; the SDK is the documented upgrade path if protocol evolution ever matters. |
| Width math | `rivo/uniseg` | Grapheme-aware; what lipgloss v2 uses internally. Never `len()`. |
| File locks | `gofrs/flock` (prune, missed-state) | Lock files themselves stay O_CREATE\|O_EXCL + PID (PS interop). |
| /proc stats | Hand-rolled (userHZ=100 const, Getpagesize) | gopsutil rejected — we need 3 fields, Linux-only. |
| Processes / git | `os/exec` | Pipes + bufio.Reader (no pty — stream separation and parity depend on it); git CLI shell-out (no go-git). |
| Config | stdlib encoding/json | viper explicitly rejected. |
| OpenRouter | stdlib net/http | One chat-completions call. |
| Self-update | `creativeprojects/go-selfupdate` | Owner chose in-TUI self-update; research's lighter alternative (re-run install.sh + startup version notice) is the fallback if it misbehaves. Version notice ships regardless. |
| Release | goreleaser v2, checksummed assets, hand-rolled install.sh | godownloader is dead; install.sh verifies sha256. |

## 3. Architecture (summary — full detail in the architecture doc)

Three thin frontends (`tui`, `cli`, `mcp`) over one orchestration facade
(`internal/app`) over 14 TUI-free domain packages (`envfile secret config lockfile
procstat cron history webhook scripts deps runner missed retention migrate`). An
import-lint test enforces that no domain package imports bubbletea/cobra/http.

Key invariants carried from the PS app (each has a named test):
- history.jsonl appends are ONE write() with O_APPEND; append happens BEFORE unlock.
- webhook dead-letter queue keeps the `.flush` rename protocol verbatim so a PS
  process and the Go binary interlock during the migration window; 50/flush cap.
- lock files: O_EXCL create, PID content, 10s freshness guard before stale reclaim —
  byte-compatible both directions (Go respects PS locks and vice versa).
- crontab writes abort if the read failed (the wipe guard), preserve foreign lines
  byte-for-byte, escape `%`, and the managed-block line format stays PS-readable.
- secret redaction has exactly one chokepoint from child output to any sink, sorted
  longest-first; log file, webhook tail, and MCP output all read the redacted log.
- All float rounding through RoundToEven (PS [Math]::Round is banker's rounding).
- PS dependency scanning shells out to an embedded pwsh scanner script (deps +
  param() block in one JSON doc, mtime-cached); degraded regex fallback with a
  visible warning when pwsh is absent. We do not reimplement a PowerShell parser.

Concurrency: domain layer is synchronous; `runner` owns exactly four goroutines per
run (stdout, stderr, sampler, supervisor) feeding one Event channel; the TUI consumes
it via a batched-drain `tea.Cmd` (block for first event, drain up to 512, one Msg) —
`program.Send` is banned. Periodic work is self-rescheduling `tea.Tick`s (1s ages,
2s lock poll, 60s missed sweep).

## 4. TUI design — workflow views (IA option A)

Four screens sharing one spatial grammar (header/view-switcher top, content, status
bar, key hints), switched by `1–4`, the command palette (`:` / `ctrl+p`), or
deep-links. Night Owl remains the default identity; themes are semantic tokens →
palettes (Catppuccin, Gruvbox, Tokyo Night as day-one alternates — one `Register`
call each).

> **2026-09-03 (v1.0.1) token-list amendment:** the semantic-token contract grew
> from thirteen to FIFTEEN tokens: `Bg Fg Muted Border Primary Pulse Accent
> Success Warning Danger Info SelBg CardBg RuntimePS RuntimePy`. `Primary`
> (focus borders, selection accent, hint keys, active tab) and `Pulse` (spinner,
> focused-pane title) restore the PS Blue↔BrCyan focus interplay that v1.0.0 had
> folded onto `Accent`; `Accent` retreats to the brand chip and palette-overlay
> highlight. Every frame row now paints a full-width `Fg`-on-`Bg` ground (the
> `terminal` palette excepted, by design — it inherits the user's scheme via
> ANSI 0-15 + default fg/bg); Border sits just above 3:1 against Bg, Muted at
> ≥4.5:1, Fg at ≥7:1, all test-enforced. Palettes now also resolve through the
> bubbletint adapter (any of ~340 scheme IDs) with a live `]`/`[` cycler. See
> parity-inventory entries 23, 26, 27, 28.

1. **Fleet (home)** — the at-a-glance view: summary strip (`● ok ✗ failing ⚠ missed
   ⏲ due <1h`), per-script rows (status badge, last run age, cpu sparkline, schedule,
   missed flag), upcoming-runs agenda, live activity. Enter → Run view for that
   script; `f` filters to failures.
2. **Run** — today's two-pane evolved: script list left (badges/runtime/age/marquee),
   live output right (viewport: follow, search with n/N, drag-select-to-copy with
   wrapped-line rejoin, click-to-copy device codes), details card, ETA progress,
   queue. All run actions (`r a e v s i l u x X y c /`) live here.
3. **History** — full-width forensics: filterable table (when/age/status/script/
   duration/cpu-peak/heat-sparkline/mem/trigger), Enter opens the log in a preview
   pane (not a mode swap), `r` re-runs, `f` scopes to one script.
4. **Schedules** — agenda sorted by next fire, cron editing (expression or natural
   language with confirm), missed-fire status, next-run countdowns.

Overlays (any view): command palette, help (`?`), confirm, input prompts, deps
prompt, .env editor (bubbles/textarea). Every action reachable by keyboard; palette
lists everything; footer hints come from the same key.Binding set as help (single
source of truth). Mouse: wheel scrolls hovered pane, click focuses/selects, drag
selects+copies in output/log panes.

80×24 floor: Fleet collapses sparkline+schedule columns; Run hides the details card
below 14 body rows (as today); History drops to when/status/script/duration; below
40×10 → "terminal too small". Golden frames pin all of this at three sizes.

> **2026-09-03 (v1.1.0) panel + animation amendment:**
>
> *Panels.* One primitive (`internal/tui/panel.go`) frames every pane, card and
> modal at and above **100 columns**: rounded corners `╭╮╰╯`, the title inset in
> the top border (`╭─ Fleet ─────╮`), and the pane's own keys inset in the
> bottom border (`╰── r run · x kill ──╯`) — rendered from the SAME
> `key.Binding` list the footer and help read (`primaryHints` is the single
> source; `tailHints` is its view-owned subset), so the border can never
> advertise a key the rest of the app does not know. Focused = Primary border
> with the bold title voice; unfocused = quiet Border; ASCII profiles get
> `+-|`. Panels pad content one extra cell at ≥120 columns (the breathing
> pass), and Fleet gains a blank row under its summary strip there.
>
> *Floor rules, per view (all below 100 columns):* every view keeps the
> v1.0.1 rule grammar — a top rule with an inset title, no side or bottom
> borders — because a full frame costs two columns per pane side and one row
> per pane bottom, exactly the budget the 80×24 floor protects. Fleet: rule
> headers on the table and each stacked card; column collapse unchanged. Run:
> rule headers on list/output/details; the pane separator stays a single `│`
> column. History: rule headers on table and preview. Schedules: one rule
> header. Overlays are the exception: modals are rounded boxes at EVERY size —
> they cover the view, so their frame costs no data. The 80×24 frames are
> byte-identical to v1.0.1 apart from the intended glyph changes (braille
> sparklines, the 8-frame spinner).
>
> *Animation engine* (`internal/tui/anim.go`): ONE self-rescheduling 16 ms
> clock, armed only while something on screen actually moves and disarmed by
> its own beat otherwise — idle is zero ticks. Every stepper is a pure
> function of the injected clock: the braille spinner (`⠋⠙⠹⠸⠼⠴⠦⠧`) at exact
> 80 ms boundaries; the marquee's 165 ms rune cadence evaluated
> frame-accurately (its standalone tick is gone); the status fade
> interpolating per-frame toward Bg in truecolor and stepping through five
> stops below it (its 100 ms tick is gone); the ETA bar filling in
> eighth-block sub-cell steps (`▏▎▍▌▋▊▉█`) and easing toward its target over
> 300 ms; and the live-activity title breathing Pulse↔Muted on a ~2 s period
> at low amplitude while anything runs. An animated 120×40 frame builds in
> ~1.1 ms — under the 2 ms budget a 60 fps clock allows — and a 16 ms step
> dirties only the rows that animate, so Bubble Tea's differ repaints cells,
> not screens.

## 5. Parity & migration strategy

> **2026-09-02:** migration machinery removed — fresh-install cutover chosen by the
> owner. The first-run migration bullet below (and decisions #1/#6 above) describe
> what was actually built and shipped through P7; none of it survives now that the
> owner ruled out ever transitioning an existing PowerShell-era install. This doc is
> left as the historical record — see `docs/superpowers/specs/2026-09-01-go-rebuild-parity-inventory.md`
> §3.13 for the retired inventory and `.superpowers/sdd/cutover-removal-report.md`
> for the removal itself.

- `hack/gen-fixtures.ps1` (run once against the PS app) freezes: mixed-era
  history.jsonl, webhook-queue.jsonl, missed-state.json, three crontab fixtures,
  config corpus, .env corpus, run log, recorded MCP request/response pairs, the cron
  truth table, and a [Math]::Round table. Committed under `testdata/psfixtures/`.
- Round-trip tests + a reverse CI job (PS reads Go-written rows/payloads/blocks).
- Lock interop tests in both directions against live PIDs.
- Side-by-side window: from P6 the Go binary takes cron duty on the real server
  while the PS TUI stays usable (shared formats + interlocking locks make this safe);
  webhook payloads diffed in n8n for a week before cutover.
- First-run migration: crontab block rewrite (backup + printed diff), ~/.psscripts
  dataDir migration, scripts layout migration, systemd unit swap — all idempotent.
- Cutover (P12): merge to main, tag PS as `powershell-final`, install.sh switches to
  release-asset download (pwsh becomes optional-but-recommended, for the AST scanner).
  *(Reversed 2026-09-03, v1.1.0, owner directive: install.sh installs pwsh again on
  apt systems via the Microsoft repo — parity-inventory divergence 31.)*

## 6. Build plan — phases, gates, and agent assignments

Orchestration model: Fable (this session's model) plans, reviews integration, and
holds context across phases; implementation phases are delegated to subagents sized
to the risk of the work. Every phase = one PR on `go-rewrite`; every PR passes its
gate + `go test -race ./...` + golangci-lint + an opus code-review agent before merge.

| Phase | Scope | Gate | Builder | Reviewer |
|---|---|---|---|---|
| P0 | Scaffold, CI, import-lint test, gen-fixtures.ps1 + committed fixtures | CI green, fixtures documented | sonnet | haiku (docs) |
| P1 | envfile, secret, config | PS test cases ported; warning strings byte-identical | sonnet | opus |
| P2 | cron engine (Next/Prev/Validate/expander) | 100% on the 400×12 truth table | sonnet | opus |
| P3 | history, retention, lockfile | retention cases ported; lock interop both directions | **opus** | opus |
| P4 | scripts discovery + sync | discovery output identical to PS `--list` on fixture tree | sonnet | opus |
| P5 | runner, procstat, webhook (+DLQ) | e2e .ps1 + .py runs; payload golden; setsid-escapee kill; DLQ ordering | **opus** | opus + Fable integration pass |
| P6 | CLI (legacy flags, exit codes) + missed detection | exit-code matrix; `--list`/`--history` diffed vs PS on same data dir. **First shippable binary — cron migrates.** | sonnet | opus |
| P7 | crontab write + migrate (block rewrite, backups, systemd swap) | block-rewrite goldens; failed-read-aborts test | **opus** (blast radius: user crontab) | opus |
| P8 | deps (embedded pwsh scanner, python scanner, venv, installers) | scanner output vs live PS AST on fixture corpus | sonnet | opus |
| P9 | MCP server (12 tools, transport matrix) | recorded-request replay suite; status/error matrix | sonnet | opus |
| P10 | TUI foundation (root model, theme, Fleet + Run views, run wiring) | goldens ×3 sizes; headless key-sequence full-run test | **opus** | Fable design review vs mockups |
| P11 | TUI depth (History/Schedules views, overlays, drag-copy, palette, animations) | golden per view/overlay; clipboard sequence tests; drag rejoin test | opus + sonnet (split per component) | opus |
| P12 | goreleaser, install.sh, self-update, docs, cutover | fresh-VM install; upgrade-VM test with populated data dir + live PS lock | sonnet | Fable final pass |

Parallelism: P1‖P2 after P0; P8‖P9 after P6; within P11, components fan out to
parallel sonnet workers with an opus integrator. Research-class questions that
surface mid-build (library quirks, protocol edges) go to throwaway sonnet
research agents rather than burning builder context.

## 7. Risks (top of register — full list in architecture doc)

1. PS AST scanning → pwsh shell-out with embedded scanner (never a Go PS parser).
2. Banker's rounding / duration formatting parity → RoundToEven + frozen tables.
3. Vixie cron dom/dow OR-rule + `5/15` semantics → truth-table fixture decides.
4. Redaction coverage → single chokepoint + end-to-end leak test.
5. OSC 52 + drag selection in tmux with BT v2 → verify wrapping early; DCS fallback.
6. MCP wire parity (202-empty-body, batch rejection, auth-before-read) → replay suite.
7. Crontab rewrite blast radius → backup + abort-on-failed-read + printed diff.
8. Ecosystem note: opencode reportedly left Go/Bubble Tea over TUI performance
   (secondary-sourced). Our scale (one screen, ~dozens of scripts, batched streaming)
   is far below theirs; P10's gate includes a chatty-script stress test to confirm.

## 8. Out of scope (deliberate)

Runtime plugin interface (third runtime earns it), config hot-reload, Windows
support (unchanged from today), multi-host anything, general REST API, DAG
orchestration — all previously flagged as over-engineering and still are.

## API server — 2026-09-02 user addition

§8 flags "general REST API" as deliberately out of scope; this is narrower and
does not reverse that call. The user asked, at P9 kickoff, for an HTTP API
alongside the MCP server. What shipped is a **co-hosted** REST surface, not a
second product: `internal/mcp/api.go` adds `/api/v1/*` routes to the *same*
`net/http.Handler` the MCP server already serves — same listener, same
`MCP_AUTH_TOKEN` bearer check, same 1MB body cap, same `internal/mcp/ops.go`
tool implementations. There is no new port, no new config key, no new
systemd unit, and no second auth mechanism to operate. The MCP tool set
already defines the whole surface a caller can reach (list/run/history/logs/
schedules/deps/update); the REST routes are thin one-to-one mappers onto the
identical `Ops` methods tools.go's `tools/call` dispatches onto — see
ops.go's `Ops.Call` and api.go's `serveAPI` for the shared switch each frontend
routes through. A dedicated test (`TestAPIAndMCPShareTheSameOpsResult`,
internal/mcp/api_test.go) proves the two frontends return byte-identical JSON
for the same operation.

Routes, one per MCP tool, all under `/api/v1` on the same listener/auth:

| Route | Tool |
|---|---|
| `GET  /api/v1/scripts` | list_scripts |
| `GET  /api/v1/scripts/{name}` | get_script_details |
| `POST /api/v1/scripts/{name}/run` (body `{args?,env?,timeout_minutes?}`) | run_script |
| `POST /api/v1/scripts/{name}/deps/install` | install_deps |
| `GET  /api/v1/history?script=&limit=` | get_history |
| `GET  /api/v1/logs/{log_id}?tail_kb=` | get_run_log |
| `POST /api/v1/sync` | sync_repos |
| `GET  /api/v1/schedules` | get_schedules |
| `PUT  /api/v1/schedules/{script}` (body `{cron}`) | set_schedule |
| `DELETE /api/v1/schedules/{script}` | remove_schedule |
| `POST /api/v1/update/app` · `POST /api/v1/update/packages` | update_app / update_packages |

Error mapping
is REST-honest rather than uniform: an ops-layer exception is `500` with a
redacted message; an unknown script or a missing/malformed log_id is `404`/
`400` (the smallest new sentinel, `mcp.ToolError.NotFound`, is what both the
JSON-RPC `isError` content and the REST status code read); a script that ran
and failed, or a sync/install/update that reports `ok:false`, is always `200`
— the body's own fields speak, exactly as MCP callers already read them.
Responses are the `Ops` result's JSON directly, with no JSON-RPC envelope.

Parity-inventory §11 gets one short new subsection (§11.12) — a single
paragraph, matching the document's existing §11.x structure — noting the
co-hosted addition and pointing at this section for the rationale. It sits
outside the "Deliberate divergences" list at the document's end: nothing in
the PowerShell app diverges here, since PS simply has no REST surface to
diverge from.
