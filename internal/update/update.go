// Package update is the binary-era self-update path: check the latest
// GitHub release against the running version, and — for a stamped release
// binary only — download and install it in place. Everything that actually
// talks to GitHub goes through the Source seam below, so this package's own
// tests never touch the network; a caller (the TUI's U key, the MCP
// update_app op) is responsible for the buildinfo.Version=="dev" mode gate
// that keeps a dev build on the existing git-pull path instead of calling
// Apply at all.
package update

import (
	"context"
	"strconv"
	"strings"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// repoSlug is the GitHub repo every release check and self-update targets —
// the same one install.sh downloads from.
const repoSlug = "yshah-aromatech/scriptorium"

// Source is the seam between Check/Apply and the actual go-selfupdate round
// trip. Production leaves the default (ghSource) in place; tests swap in a
// stub via SetSource.
type Source interface {
	// Latest returns the newest release version go-selfupdate can see
	// ("" + false when the repo has no releases at all, not an error).
	Latest(ctx context.Context) (version string, found bool, err error)
	// Replace downloads and installs the latest release over the running
	// binary (creativeprojects/go-selfupdate's UpdateSelf: it re-detects the
	// latest release itself, comparing against current). It returns the
	// version left running afterward — equal to current when there was
	// nothing newer to install.
	Replace(ctx context.Context, current string) (installed string, err error)
}

type ghSource struct{}

func (ghSource) Latest(ctx context.Context) (string, bool, error) {
	rel, found, err := selfupdate.DetectLatest(ctx, selfupdate.ParseSlug(repoSlug))
	if err != nil || !found {
		return "", false, err
	}
	return rel.Version(), true, nil
}

func (ghSource) Replace(ctx context.Context, current string) (string, error) {
	rel, err := selfupdate.UpdateSelf(ctx, current, selfupdate.ParseSlug(repoSlug))
	if err != nil {
		return "", err
	}
	return rel.Version(), nil
}

var active Source = ghSource{}

// SetSource overrides the seam for tests. Callers must invoke the returned
// restore func (typically via t.Cleanup) so later tests see the real
// default again.
func SetSource(s Source) (restore func()) {
	prev := active
	active = s
	return func() { active = prev }
}

// Check reports the latest release version and whether it is newer than
// current. Safe to call unconditionally (including with current=="dev"):
// the TUI's startup notice calls this regardless of self-update mode, so it
// always has something useful to say. A "dev" build — or any other current
// value that isn't v-prefixed dotted-numeric semver — can't be compared, so
// any release found is reported as available: it's the caller's own choice
// (git pull vs self-update) which mechanism "press U" ends up running.
func Check(current string) (latest string, available bool, err error) {
	latest, found, err := active.Latest(context.Background())
	if err != nil || !found {
		return "", false, err
	}
	return latest, versionNewer(latest, current), nil
}

// Apply installs the latest release over the running binary if one is
// newer than current, returning the version left running (unchanged from
// current when there was nothing newer). Callers must gate this behind
// buildinfo.Version != "dev" themselves — the real Source's Replace calls
// creativeprojects/go-selfupdate with current as a semver string, which
// rejects "dev" with an error rather than doing anything destructive, but
// there is no reason for a dev build to ever reach the network here.
func Apply(current string) (installed string, err error) {
	return active.Replace(context.Background(), current)
}

// versionNewer reports whether latest is a newer version than current. Both
// are expected in v-prefixed dotted-numeric form ("v1.2.3"); a value that
// doesn't parse that way (e.g. "dev") is treated as behind any release that
// was found, per Check's own doc comment.
func versionNewer(latest, current string) bool {
	lp, lok := parseSemver(latest)
	cp, cok := parseSemver(current)
	if !cok {
		return lok
	}
	if !lok {
		return false
	}
	for i := range lp {
		if lp[i] != cp[i] {
			return lp[i] > cp[i]
		}
	}
	return false
}

// parseSemver reads "v1.2.3" (the "v" optional, any "-pre"/"+build" suffix
// on the patch segment ignored) into [major, minor, patch]. Anything else —
// "dev", a malformed tag — reports ok=false.
func parseSemver(v string) (out [3]int, ok bool) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if i == 2 {
			if idx := strings.IndexAny(p, "-+"); idx >= 0 {
				p = p[:idx]
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
