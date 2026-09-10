Scriptorium review — 9 September 2026

Review only. No application fixes were made. The highest priorities are protecting scheduled work and saved data, then making run state reliable. The existing Go race suite and Go vet pass, but five isolated probes reproduce defects outside their coverage.

Enhanced prompt used for this review:

> Review this app end to end using Ponytail principles and relevant skills. Understand its purpose and main workflows first. Identify reproducible bugs, security and data integrity risks, unnecessary complexity, and measurable inefficiencies. Review usability, accessibility, consistency, visual hierarchy, and functionality that improves actual workflows. Rank findings by severity and user impact. Provide evidence, affected files or screens, reproduction steps where possible, and the smallest effective fix. Separate confirmed defects, hypotheses, and aesthetic preferences. Run appropriate checks and inspect the interface where feasible. Deliver a prioritized improvement plan and disclose coverage gaps. Review and recommend only; do not implement fixes.

Applied skills: Ponytail, Ponytail Audit for complexity, TUI Design, Systematic Debugging, and Verification Before Completion. The application is a full-screen Go/Bubble Tea terminal session with companion headless commands; its central loop is select → inspect → run → observe output → investigate history or adjust scheduling.

Priority labels: P1 = high impact, address first; P2 = normal priority. “Reproduced” means a temporary executable probe demonstrated the defect. “Code-confirmed” identifies a direct implementation path, without claiming a production incident.

1. **P1 — Failed crontab reads can be treated as an empty crontab. Reproduced.**

   [crontab.go:72](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/cron/crontab.go:72), [crontab.go:116](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/cron/crontab.go:116).

   The subprocess wrapper discards stderr. An unsuccessful `crontab -l` with no stdout is then accepted as an empty crontab, including permission or spool errors. Set/Remove may write a replacement without the user's existing entries if writing succeeds. A fake crontab that reports “permission denied” on stderr and exits 1 caused Set to write a replacement and return nil. No real crontab was touched.

   Smallest fix: retain the failure diagnostic and distinguish a positively recognized “no crontab” result from other failures. Refuse writes when existing contents are unknown. Keep a regression check at the subprocess boundary, not only the injected runner's simplified truth table.

2. **P1 — Run preparation does not reserve the UI's active slot. Reproduced state defect; downstream consequences traced in code.**

   [runctl.go:38](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/runctl.go:38), [runctl.go:126](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/runctl.go:126).

   Request two scripts before the first dependency scan finishes. Both requests start scans; the queue remains empty and active() remains false. Later RunStarted messages overwrite the single handle. Event messages carry no run identity, and the next drain uses the currently stored handle, so overlapping launches can mix output or stop draining a prior run. The same gap permits quitting during preparation without treating it as active work. A per-script lock does not serialize different scripts.

   Smallest fix: reserve a preparing state synchronously before issuing the scan, keep it through launch, and queue subsequent requests. Clear it on every failure/cancel path and make quit cancel or await preparation. Cover rapid requests and quit during preparation with state-transition tests.

3. **P1 — History pruning can erase concurrently completed runs. Code-confirmed.**

   [retention.go:128](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/retention/retention.go:128), [retention.go:175](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/retention/retention.go:175), [history.go:65](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/history/history.go:65).

   Pruning reads history, constructs a replacement, then renames it over the original. Appenders do not acquire the prune lock. Any row appended between the snapshot and replacement can disappear. The source explicitly documents this race in a `ponytail:` comment; accepting lost run records is not an appropriate simplification for a scheduler.

   Smallest fix: use one stable interprocess lock file for both append and prune's read/replace transaction. All writers must participate; if legacy PowerShell and Go run together, update both implementations or avoid concurrent pruning during migration. An atomic rename alone does not solve this.

4. **P2 — A failed .env save discards the editor buffer. Reproduced.**

   [envedit.go:102](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/envedit.go:102), [overlay.go:69](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/overlay.go:69).

   Ctrl+S immediately closes the editor before its asynchronous write succeeds. Forcing a write error produced an error message with no editor or unsaved text left to recover. Separately, WriteFile truncates the original directly, so a partial write can damage the saved file.

   Smallest fix: keep the editor and dirty buffer open until save success; write to a temporary file in the same directory and rename on success. Preserve the original and show the error inline on failure.

5. **P2 — Distinct scripts can receive the same identity. Reproduced.**

   [discover.go:104](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/scripts/discover.go:104).

   Put `job/main.py`, `job.py`, and `job.ps1` in one repo. Discovery assigns the second and third candidates the same `scripts-job-2` name. The duplicate fallback adds `-2` once without checking whether it is already taken. Names key script lookup, locks, environments, history, and scheduling, so this merges unrelated state and makes one script ambiguous to address.

   Smallest fix: search for an unused numbered suffix using the existing case-insensitive seen map. Include collisions with naturally numbered names in the check.

6. **P2 — History copies the entire retained dataset every frame. Measured.**

   [history.go:70](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/history.go:70), [history.go:218](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/history.go:218), [anim.go:48](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/anim.go:48).

   A 120×40 frame benchmark on this Apple M5 Pro, ten iterations per case, measured:

   | Loaded rows | Time per frame | Allocated bytes per frame |
   | --- | ---: | ---: |
   | 200 | 1.27 ms | 519,276 |
   | 50,000 | 15.36 ms | 52,584,348 |

   These are local microbenchmark averages, not Ubuntu production measurements. The renderer rebuilds a reversed/filtered slice of every history row even though only one page is visible. The animation clock remains active while external runs exist, including in History. Repeated frames therefore amplify this allocation cost.

   Smallest fix: rebuild the filtered ordering when history or scope changes; render only the visible range. Avoid animating a screen when nothing visible changes. No replacement UI framework is needed.

7. **P2 — Queued scripts skip dependency preparation. Code-confirmed.**

   [runctl.go:242](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/runctl.go:242).

   While A runs, queue a never-run B that needs dependencies. Dequeue calls launch directly, bypassing the dependency scan/prompt used for an immediate run. B can fail solely because it was queued.

   Smallest fix: feed dequeued work through the same preparation path as direct requests, coordinated with the active-slot fix above.

8. **P2 — Fleet status can remain stale or incorrectly say never run. Code-confirmed.**

   [root.go:269](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/root.go:269), [root.go:346](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/root.go:346).

   Lock polling replaces only the live-run list, so an external cron/MCP completion does not refresh the stored last statuses or recent runs. Fleet loading also calculates all statuses from only the latest 200 rows globally: a frequently running script can push another script's last run out of the status source even though its history remains on disk.

   Smallest fix: refresh status data when history changes, using an inexpensive change check, and calculate last-per-script status independently from the small recent-activity window. Keep expensive disk work out of View.

9. **P2 — requirements.txt version changes are ignored when the package name is installed. Code-confirmed.**

   [pyscan.go:111](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/deps/pyscan.go:111), [requirements.go:26](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/deps/requirements.go:26).

   The scanner strips version specifications and compares names only. If a venv contains an old version and a repo changes its requirement to a newer pinned version of that same package, the scanner returns no missing dependency and installation is skipped. Extras and included requirement files are also not fully represented by this parser.

   Smallest fix: let pip process the manifest when it changes, recording success only after installation succeeds. Include referenced requirement files in change detection or run the existing pip install command each preparation; do not build another dependency resolver.

10. **P2 — API runs retain their complete output in memory before returning a bounded tail. Code-confirmed.**

    [ops.go:324](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/mcp/ops.go:324).

    Every output line is appended to a slice, then joined; ordinary runs subsequently replace that string with the log tail. Memory scales with total run output despite the response limit. Long, chatty scripts can exhaust the service's memory. The runner also retains all resource samples until final downsampling, a separate duration-dependent growth path.

    Smallest fix: retain only a bounded fallback tail for runs without logs and use the on-disk tail for ordinary runs. Bound resource history while retaining the existing running sums and maxima.

11. **P2 — Schedules and History context is taken from Fleet's selection. Reproduced for Schedules.**

    [fleet.go:513](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/fleet.go:513), [statusbar.go:62](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/statusbar.go:62).

    In the fixture, Schedules highlights `nightly-report` while the bottom context line describes `backup-db` and its cron expression. The shared selection helper understands Run and otherwise defaults to Fleet, including History and Schedules. The existing golden snapshot preserves the mismatch.

    Smallest fix: resolve context from the current view's selected script or history row. Assert the identity as well as snapshotting the output.

Additional code-confirmed issues worth a follow-up: runner log-write and history-append errors are ignored ([runner.go:495](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/runner/runner.go:495), [runner.go:583](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/runner/runner.go:583)), so successful execution can appear fully recorded when storage failed; source-build self-update pulls source but does not rebuild the executable ([actions.go:211](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/actions.go:211)), so “restart to apply” is insufficient for a compiled binary. Surface persistence failures separately from script exit status, and either rebuild source installs or print accurate rebuild instructions.

The interface has a useful foundation: four consistent workflow views, visible keyboard hints, text/symbol status cues, semantic themes, and substantial frame coverage. Keep those. The best UI improvements are specific:

- **Make narrow layouts genuinely usable.** At 80×24, Run splits into a 26-column list and 53-column output. At 60 columns it keeps 24 for the list and 35 for output; at 40×10 only 15 remain for output. Details and long names truncate aggressively. Below 80 columns, show a full-width list or output with Tab switching between them and a clear focus label. Keep 40×10 as a minimum only if that fallback is usable; smaller frames already show a truthful too-small state.
- **Give data space before decoration.** The 120×40 Fleet snapshot has four separately framed panels with only five script rows in the main panel. Border depth is one, so nested borders are not the problem. Borders alone occupy 376 cells, about 7.8% of the 4,800-cell frame. The recent card truncates names such as `heartbeat` while displaying both a status glyph and the word “success.” Remove that duplicate word, shorten timestamps consistently, and give the saved cells to names. Move per-pane hints out of the global footer when they already appear in the panel border. This is an aesthetic preference backed by visible truncation, not a correctness defect.
- **Prioritize discoverability at 80 columns.** The Fleet footer truncates before commands/help because it spends width on separate up/down hints and longer action labels. Reserve room for `? help` and `: commands`; combine movement hints. Label the contextual `f` action “failures” in Fleet and “scope” in History instead of “failures / scope.”
- **Show preparation, queue, and errors explicitly.** A compact “checking dependencies” state supports the active-slot fix. Show the next queued script and queue count near output. Keep save errors beside the edited content. Transient status currently loses priority to the running-state line, so errors during a run can be hidden.
- **Make theme selection searchable and optionally persistent.** Cycling hundreds of themes is cumbersome. Reuse the existing palette/filter pattern to select by name; add a deliberate save action. Avoid a new settings framework. Offer reduced motion for users who do not want breathing titles, moving names, and fades.
- **Keep accessibility claims bounded.** NO_COLOR handling, status glyphs/text, headless commands, and existing ASCII-panel tests are useful. This review did not verify screen-reader behavior, every Unicode glyph on older terminals, or real SSH/tmux color rendering. Avoid replacing textual meaning with color alone.

Ponytail simplification recommendations: remove whole-history copying from rendering; remove full-output accumulation that is immediately discarded; reuse the direct-run preparation path for queued work; and cache selected-script environment metadata instead of reading .env and statting directories on every Run frame ([runview.go:624](/Users/y.shah/development/work/scriptorium-go-rewrite/internal/tui/runview.go:624)). These have demonstrated or direct operational value. No generic service layer, new database, dependency resolver, or wholesale TUI rewrite is justified by this review. I am not estimating deleted line/dependency totals without a concrete patch.

Suggested implementation order:

1. Protect crontab reads, history append/prune, and failed editor saves. Retain one focused regression test per failure mechanism.
2. Fix run preparation/queue ownership, then route queued runs through dependency preparation. Verify rapid requests, cancellation, and completion.
3. Fix unique identities, fresh per-script status, requirements handling, and view-specific selection.
4. Remove the measured History allocation hotspot and API output growth. Repeat the same benchmark after the change.
5. Add the narrow single-pane layout and simplify duplicated hints/status text. Compare pinned 80×24, 60×24, and 40×10 frames before changing the overall visual style.

Validation and limits:

- `go test -race ./...`: passed across the existing Go suite.
- `go vet ./...`: passed.
- Five temporary defect probes: all failed their expected-correctness assertions, reproducing findings 1, 2, 4, 5, and 11. A separate frame-export probe passed.
- Rendered model frames inspected at 60×24, 40×10, 80×24, and 120×40, alongside checked-in Fleet, Run, Schedules, and Help snapshots. This was headless rendering/model inspection, not a manual terminal-emulator session.
- The suite includes headless full-run integration with a stub interpreter. PowerShell is absent on this machine, so real PowerShell/Pester, Ubuntu /proc and systemd behavior, live dependency installation, real webhook delivery, and release update flows were not exercised. No live external actions were requested or performed by the review probes.
- Review coverage prioritized the Go execution, persistence, scheduling, dependency, API, and TUI flows. It is not an exhaustive audit of legacy PowerShell code or every installer branch, nor a security certification.
- Review probes and the Go overlay are in `/private/tmp/scriptorium-review-20260909`. Application source files were not edited. The overlay lets the probes run without adding failing tests to the repository.

Reproduce the defect probes:

```sh
go test -overlay /private/tmp/scriptorium-review-20260909/overlay.json ./internal/tui ./internal/cron ./internal/scripts -run '^TestReview' -v
```

Reproduce the frame benchmark:

```sh
go test -overlay /private/tmp/scriptorium-review-20260909/overlay.json ./internal/tui -run '^$' -bench '^BenchmarkReview' -benchtime=10x -benchmem
```
