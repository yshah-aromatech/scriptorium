package scripts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

// cleanExcludes are the `git clean -fdx` exclusions: local per-script .env
// files and python cruft that regenerates on every run must survive a sync.
var cleanExcludes = []string{"-e", ".env", "-e", "**/.env", "-e", "__pycache__", "-e", "*.pyc"}

// SyncOne is the port of Sync-StoOneRepo: clones repo if its Root has no
// .git yet, else hard-resets it to origin/<branch>. Cancelling ctx kills the
// git child (a clone can run for minutes, and the TUI's `x` has to be able to
// stop one). Every emitted line is
// redacted through reg.
func SyncOne(ctx context.Context, repo Repo, reg *secret.Registry, onLine func(string)) bool {
	emit := func(l string) { onLine(reg.Redact(l)) }

	url := repo.URL
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.HasPrefix(url, "https://") && !strings.Contains(url, "@") {
		injected := "https://x-access-token:" + token + "@" + strings.TrimPrefix(url, "https://")
		// registered BEFORE any git call, so even a failed clone's own
		// error output (which can echo the remote URL) comes out redacted
		reg.Add("", injected, true)
		url = injected
	}

	dir := repo.Root
	branch := repo.Branch
	var ok bool

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		emit(fmt.Sprintf("[%s] cloning %s (branch %s)...", repo.Name, repo.URL, branch))
		out, cerr := exec.CommandContext(ctx, "git", "clone", "--branch", branch, url, dir).CombinedOutput()
		emitLines(emit, string(out))
		ok = cerr == nil
	} else {
		emit(fmt.Sprintf("[%s] syncing %s (hard reset to origin/%s)...", repo.Name, repo.URL, branch))
		_, _ = exec.CommandContext(ctx, "git", "-C", dir, "remote", "set-url", "origin", url).CombinedOutput() // refresh token; output discarded like PS's Out-Null

		steps := [][]string{
			{"fetch", "origin"},
			{"checkout", branch},
			{"reset", "--hard", "origin/" + branch},
		}
		steps = append(steps, append([]string{"clean", "-fdx"}, cleanExcludes...))

		ok = true
		for _, step := range steps {
			if ctx.Err() != nil {
				emit(fmt.Sprintf("[%s] cancelled", repo.Name))
				return false
			}
			args := append([]string{"-C", dir}, step...)
			out, serr := exec.CommandContext(ctx, "git", args...).CombinedOutput()
			emitLines(emit, string(out))
			if serr != nil {
				emit(fmt.Sprintf("[%s] git %s failed (exit %d)", repo.Name, step[0], exitCode(serr)))
				ok = false
				break
			}
		}
	}

	if ok {
		emit(fmt.Sprintf("[%s] sync complete", repo.Name))
	} else {
		emit(fmt.Sprintf("[%s] sync FAILED — check GITHUB_TOKEN in .env (the PAT needs Contents:Read on %s)", repo.Name, repo.URL))
	}
	return ok
}

// Sync is the port of Sync-StoRepo: syncs every URL-configured repo, each
// step continuing past a prior repo's failure (the caller sees the
// aggregate result).
func Sync(ctx context.Context, cfg *config.Config, paths config.Paths, reg *secret.Registry, onLine func(string)) bool {
	all := Repos(cfg, paths)

	var withURL []Repo
	for _, r := range all {
		if r.URL != "" {
			withURL = append(withURL, r)
		}
	}
	if len(withURL) == 0 {
		onLine("no scripts repo configured — set `repos` (or scriptsRepo) in config.json, or SCRIPTS_REPO in .env")
		return false
	}

	allOk := true
	for _, repo := range withURL {
		if !SyncOne(ctx, repo, reg, onLine) {
			allOk = false
		}
	}
	return allOk
}

// LastSyncTime is the port of Get-StoLastSyncTime: the newest of every
// repo's FETCH_HEAD mtime (touched by every fetch), falling back to the
// .git dir itself for a fresh clone with no fetch yet. Zero when no repo
// has synced.
func LastSyncTime(repos []Repo) time.Time {
	var latest time.Time
	for _, r := range repos {
		for _, p := range []string{filepath.Join(r.Root, ".git", "FETCH_HEAD"), filepath.Join(r.Root, ".git")} {
			if fi, err := os.Stat(p); err == nil {
				if fi.ModTime().After(latest) {
					latest = fi.ModTime()
				}
				break
			}
		}
	}
	return latest
}

// emitLines splits git's combined output into non-empty lines, matching PS
// capturing `2>&1` as one object per line and skipping blanks.
func emitLines(emit func(string), s string) {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			emit(l)
		}
	}
}

// exitCode extracts a failed command's exit code, or -1 when it isn't an
// *exec.ExitError (e.g. the binary itself couldn't be started).
func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
