# App review implementation

Implementation follows the five stages in [the approved plan](../superpowers/plans/2026-09-09-review-fixes.md), based on [the original review](2026-09-09-app-review.md). Work is in the existing isolated worktree, with changes left uncommitted for review.

## 1. Persistence — complete and reviewed

Failed crontab reads now preserve diagnostics and refuse writes unless the error positively identifies an absent crontab. History append and retention share a process-safe sidecar lock, including the updated legacy PowerShell writer. Environment saves use an atomic private file replacement and keep the editor and buffer on failure. Runner persistence failures are visible separately from the script's exit result across the CLI, TUI, and API.

The seven affected Go package suites passed. A separate review approved the implementation. Legacy PowerShell interoperability checks exist but were skipped locally because PowerShell is unavailable. Mixed Go/PowerShell deployments must update both legacy modules before relying on coordinated retention; the README documents this requirement.

## 2. Run preparation and lifecycle — complete and reviewed

Preparation reserves the active slot before scanning, and queued runs use the same dependency flow as direct runs. Async results carry ownership so stale results cannot launch or replace newer work. Kill and confirmed quit cancel preparation and drain it before releasing ownership. Declining quit restores the previous prompt. Preparation, queue depth/next script, errors, and persistence warnings remain visible during activity.

Review caught parent-only cancellation that could leave pip or other tool children alive. A small shared subprocess helper now cancels the owned process group and bounds pipe draining. A hermetic child-process regression reproduced the failure in all five affected paths before the fix and passed afterward. TUI, dependency, scripts, and runner cancellation/timeout checks passed. Deliberately detached processes are outside the process-group boundary; bounded draining prevents their pipes from blocking shutdown indefinitely.

## 3. Discovery and refreshed state — complete and reviewed

Script names remain unique across case differences and suffix collisions. Fleet, CLI, and API derive latest status from complete history while keeping the recent display bounded. The existing poll detects external history changes and refreshes the active views. Context follows History and Schedules selection; environment metadata is loaded outside rendering and refreshed after relevant actions.

Requirements files go intact to pip, including pins, extras, and includes. Source-update messages now explain that pulled source needs rebuilding. Repository examples clearly distinguish language-independent `scriptsRepo`/`repos` from the interpreter settings.

Focused regressions and affected golden checks passed. One broad run measured 2.153 ms/frame against an existing 2 ms timing threshold; the isolated rerun and final suites passed without changing the threshold.

Review found and corrected a history-signature ordering race. A deterministic append-during-load regression demonstrates that concurrent completion remains detectable by the next poll.

## 4. Rendering and buffers — complete and reviewed

History caches scoped ordering and column width when data changes, then renders visible rows. Invisible content no longer drives animation. API run output retains a bounded fallback tail, including when the disk log is unavailable. Resource charts retain at most 512 peak buckets per metric; count, sums, and maxima still cover every received sample. Chart timing becomes approximate within merged buckets after long runs.

Affected suites and regressions passed, including 100,000 samples and a large Unicode output stream. Same-machine 120×40 benchmarks used ten iterations; values are microbenchmark averages, not production guarantees.

| History rows | Before frame time | After frame time | Before bytes/frame | After bytes/frame |
| ---: | ---: | ---: | ---: | ---: |
| 200 | 1.25 ms | 0.82 ms | 511,696 | 389,572 |
| 50,000 | 16.76 ms | 1.17 ms | 69,910,326 | 1,584,778 |

Those measurements include initial cache construction. Warmed frames allocate approximately 380 KB at either size, so steady rendering allocation no longer grows with history length. Cache rebuilds still use O(N) time and storage. The simple fallback tail shifts at most the configured byte limit per incoming line; a ring buffer is a future option if large configured tails show CPU pressure.

## 5. UI — complete and reviewed

Below 80 columns, Run shows a full-width list or output pane, switched with Tab; 80 columns retains the split. Mouse routing follows the visible pane. Recent cards give names more space, and contextual hints avoid duplication while keeping quit, focus, commands, and help discoverable. `T` opens searchable theme selection; arrows preview, Enter uses the theme for the session, Escape restores, and Ctrl+S saves atomically while preserving other JSON fields. `reducedMotion` removes decorative animation while retaining progress and message expiry.

Review corrected theme-save completion during quit confirmation and deferred dependency preparation, preserving errors and retry state. Hidden narrow-list marquees no longer drive the frame clock. Semantic tests, narrow/standard/wide plain and color goldens, and scoped race regressions pass. Final review approved all five stages and the complete change.

## Final validation and limits

- Full Go suite passed; the final minimal picker-lifetime correction then passed the full TUI suite.
- Full race suite on final source, `go vet ./...`, final binary build, and `git diff --check` passed.
- An isolated terminal smoke verified 40×10 and 60×24 single-pane navigation, a successful local script run, theme filtering and persistent save with other settings retained, the 80×24 split, and normal exit.
- PowerShell is unavailable on this host. Conditional legacy runtime/interoperability tests remain unverified locally; shell stubs validate application wiring rather than PowerShell itself.
- No new dependency, installation, release, commit, push, or production configuration mutation was needed. Changes are left in the existing worktree.

## Execution decisions

The existing review served as the approved design, and the existing linked worktree supplied isolation. Work proceeded one implementation stage at a time with independent review; Astra handled concurrency/resource algorithms and final integration review, while Terra/Sol handled bounded state/UI work and review. The cost of choosing the existing design is ordinary rework if the intended scope differs; the reviewed plan records the implemented scope.

Repository configuration stayed compatible: only examples and explanations changed. A different per-language repository model would require a separate feature. The worktree and review ledger remain available because the changes are uncommitted; they can be integrated or cleaned up later. The final theme-save finding received a minimal follow-up despite the workflow's suggested one-wave cap, because preserving retry state was required; preparation now waits while the theme picker remains open.
