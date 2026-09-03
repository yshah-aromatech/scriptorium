# Cutover runbook

Owner-facing. Nothing in the `go-rewrite` branch or its CI performs any of
these steps automatically — every one below is a command an owner types on
purpose. The Go binary and the PowerShell app are safe to run side by side
throughout: they share file formats (`history.jsonl`, `webhook-queue.jsonl`,
`missed-state.json`), the crontab managed block (each reads the other's
spelling), and lock files (byte-compatible both directions) — nothing here
is a point of no return until step 7's merge.

Run every step from the real server, against the real `~/.scriptorium` data
dir (or wherever `dataDir` points) the PowerShell app has been using.

---

## 1. Get the binary onto the server

Either build and copy it over:

```bash
GOOS=linux GOARCH=amd64 go build -o scriptorium ./cmd/scriptorium   # or arm64
scp scriptorium user@server:~/scriptorium-bin
```

or, once a release exists, run `install.sh` in binary mode (see step 7 for
when that release exists on the real repo — until then, use the scp path,
or point `install.sh` at a pre-release build via a manual copy into
`~/.local/bin/scriptorium`). For steps 2-3, do **not** point it at the
PowerShell app's app dir's `config.json`/`.env` yet if you want to keep the
two fully separate during the sanity pass — `SCRIPTORIUM_APP_DIR` can target
a scratch directory, and the binary can live anywhere (`~/scriptorium-bin`
above is fine) while it's only ever invoked by its full path.

**Before step 4:** move (or re-copy) the binary to its FINAL path —
`~/.local/bin/scriptorium` — and confirm that's what's on `$PATH` as
`scriptorium`. `--migrate` bakes the running binary's own absolute path
(`os.Executable()`) into every cron line it writes, and re-running
`--migrate` later never re-adopts a block that's already in the current
markers — so migrating from a scratch/scp path (like `~/scriptorium-bin`)
pins cron to that path forever, even after step 7 installs the real
`v1.0.0` release at `~/.local/bin/scriptorium`. If this is ever missed, the
recovery is one command, not a re-migrate: re-`Set` any one schedule (the
Schedules view's `e`/`Enter`, or the MCP `set_schedule` tool) — that
re-renders the WHOLE managed block against whatever binary is currently
running, fixing every line's path in one shot.

**Rollback:** delete the binary. Nothing else has been touched.

---

## 2. Side-by-side sanity check

Point the Go binary at the *real* app dir (`SCRIPTORIUM_APP_DIR=~/scriptorium`
or wherever `config.json` actually lives) and compare its headless output
against the running PowerShell app, on the same data:

```bash
scriptorium --list      # compare against: pwsh -File scriptorium.ps1 --list
scriptorium --history   # compare against: pwsh -File scriptorium.ps1 --history
```

Both must show the same scripts, the same last-run statuses, and the same
schedules (bracket-shaped in `--list`, same rows in `--history`). Do **not**
run `scriptorium --migrate` yet.

Be aware: `--list`/`--history` don't write anything THEMSELVES, but every
`scriptorium` invocation (any flag, this one included) runs the same
startup retention prune the PowerShell app already applies — hourly-
throttled and flock-serialized, so it's safe, but the first Go command
against the real data dir may age out old history rows and delete old log
files under `logRetentionDays`/`historyDays`, same as PowerShell has been
doing all along. Nothing about the crontab, `config.json`, or `.env` is
touched by this step.

**Rollback:** none needed — the crontab, config, and every script are
untouched; a retention prune only ever removes what the existing policy
already marked for deletion.

---

## 3. Webhook-diff week

Let both apps run their normal cron schedules for about a week, both
still pointed at n8n. Nothing changes on the server for this step — it is
purely an observation window:

- Compare the `script_run` payloads landing in n8n from each app for the
  same script/schedule: status, exit code, duration, resource averages
  should agree closely (§ divergence notes in the parity inventory cover
  the handful of expected, harmless differences — e.g. CPU rounding).
- Confirm the Go binary's `missed`-fire detection is not firing spuriously
  and not missing a real one.

Only move to step 4 once a week of payloads looks right.

**Rollback:** stop running the Go binary's cron entries (it isn't
scheduling anything yet — PowerShell still owns the crontab at this point,
so there is nothing to undo).

---

## 4. Migrate cron duty to the Go binary

```bash
scriptorium --migrate
```

This is the **only** command that ever rewrites the managed crontab block —
nothing else in either app calls it implicitly. It prints the current
managed block, the block it is about to write, backs up the *entire*
crontab (every line, managed or not) to
`<dataDir>/crontab.bak.<RFC3339-timestamp>` — printing that path — and then
rewrites the block so every scheduled script invokes this binary instead of
`pwsh -File scriptorium.ps1`. Running it again is a no-op: it prints
`already migrated — nothing to do` and touches nothing.

If the crontab could not be read at all (permissions, a broken `crontab`
binary), it prints the exact reason to stderr, makes no backup, writes
nothing, and exits 1 — the wipe guard. Fix the underlying problem and
re-run; nothing is lost either way.

**Rollback:** restore the exact pre-migration crontab from the backup
`--migrate` printed:

```bash
crontab <dataDir>/crontab.bak.<the-timestamp-it-printed>
```

That one command replaces the whole crontab — managed block and everything
else — back to exactly what it was before `--migrate` ran.

---

## 5. Swap the MCP service (if you run one)

```bash
scriptorium --install-mcp-service
```

Installs and starts a systemd unit named `scriptorium-mcp` — a system unit
at `/etc/systemd/system/scriptorium-mcp.service` when run as root, a user
unit (plus `loginctl enable-linger` so it survives logout) otherwise. Either
way, it first disables and removes any leftover pre-rename `psscripts-mcp`
unit it finds. If a PowerShell-based MCP server was running under some
other name or in the foreground, stop it manually before this step so
nothing is listening on the same port twice.

Check it: `systemctl status scriptorium-mcp` (`--user` for the user
variant); logs: `journalctl -u scriptorium-mcp -f`. Point n8n's MCP Client
Tool at the same URL as before — the tool set and JSON shapes are unchanged.

**Rollback:**

```bash
systemctl disable --now scriptorium-mcp           # or: systemctl --user disable --now scriptorium-mcp
```

then start whatever was serving MCP before this step (the PowerShell
launcher is untouched — see step 7 for when it actually goes away).

---

## 6. Switch interactive use to the Go TUI

Once cron and MCP are both on the Go binary and have soaked without
surprises, start using `scriptorium` (the Go TUI) day to day instead of
`pwsh -File scriptorium.ps1`. Both remain fully functional and share every
data file, so this step is a habit change, not a technical one — there is
nothing to configure and nothing to roll back beyond "keep using the other
one."

**Rollback:** run the PowerShell app instead. Nothing it reads or writes has
changed shape.

---

## 7. Merge and tag (the actual cutover)

Only after a real soak period with no surprises from steps 3-6:

```bash
# on main, in the PowerShell-era checkout:
git tag powershell-final
git push origin powershell-final

# merge the Go rewrite in:
git checkout main
git merge go-rewrite
git push origin main

# tag the first Go release — this is what triggers .github/workflows/release.yml:
git tag v1.0.0
git push origin v1.0.0
```

Once the release workflow finishes (check the Actions tab — it builds
linux amd64/arm64 archives, checksums them, and publishes a GitHub Release,
all via `.goreleaser.yml`), re-run `install.sh` on the server as the final
acceptance test of the whole distribution path:

```bash
curl -fsSL https://raw.githubusercontent.com/yshah-aromatech/scriptorium/main/install.sh | bash
```

This is the first time `install.sh` runs in true binary mode against a real
published release — confirm it downloads the right archive for the server's
architecture, verifies its checksum, and that `scriptorium --version`
reports `v1.0.0` afterward. The old `scriptorium.ps1`/`psscripts.ps1`
launcher files are still present in the repo history (tagged
`powershell-final`) and still runnable from a checkout of that tag if you
ever need them again — nothing about this step deletes them from disk on
the server; `install.sh` only ever touches `~/.local/bin/scriptorium`,
`config.json`, and `.env`.

**Rollback:** `git checkout` the `powershell-final` tag on the server (or
keep a checkout of it around) and go back to running that. The crontab
still points at whichever binary step 4 pointed it at — if you need cron
back on PowerShell too, restore the step-4 backup (see step 4's rollback)
and re-run install.sh from a `powershell-final` checkout to get the old
launcher back at `~/.local/bin/scriptorium`.

---

## Rollback summary

| Step | Rollback |
| --- | --- |
| 1. Get the binary | delete it |
| 2. Sanity check | nothing was written |
| 3. Webhook-diff week | nothing was written |
| 4. `--migrate` | `crontab <dataDir>/crontab.bak.<timestamp>` |
| 5. MCP service swap | `systemctl disable --now scriptorium-mcp`, restart the old one |
| 6. Switch interactive use | just use the other one |
| 7. Merge + tag | `git checkout powershell-final`; restore step 4's backup if cron needs to go back too |
