// Package pwshtest is the shared gate every pwsh-interop test across the
// module (lockfile, scripts discovery cross-check, ...) uses to find pwsh:
// locally, a missing pwsh skips; in CI a missing pwsh is a hard failure,
// since a silently skipped interop test defeats the point of having one.
// It lives directly under internal/ (not nested in any one caller's
// package) because it's shared by multiple, unrelated packages.
package pwshtest

import (
	"os"
	"os/exec"
	"testing"
)

// decide turns a LookPath outcome and the CI env value into the CI-loud
// policy: found -> proceed; missing in CI -> fatal; missing locally -> skip.
// A pure function so the policy is unit-testable without exercising
// testing.T's fail/skip machinery (which would fail the enclosing test).
func decide(found bool, ci string) (fatal, skip bool) {
	if found {
		return false, false
	}
	if ci != "" {
		return true, false
	}
	return false, true
}

// require resolves an interpreter, or fails the test per CI policy:
// os.Getenv("CI") != "" turns a missing binary into t.Fatal; otherwise it's
// t.Skip.
func require(t *testing.T, bin string) string {
	t.Helper()
	p, err := exec.LookPath(bin)
	fatal, skip := decide(err == nil, os.Getenv("CI"))
	if fatal {
		t.Fatalf("%s required in CI — interop gate must not skip", bin)
	}
	if skip {
		t.Skipf("%s not on PATH", bin)
	}
	return p
}

// RequirePwsh returns the resolved pwsh path under the CI-loud policy.
func RequirePwsh(t *testing.T) string { t.Helper(); return require(t, "pwsh") }

// RequirePython returns the resolved python3 path under the same policy —
// the runner's python end-to-end tests are as much of an interop gate as
// the pwsh ones.
func RequirePython(t *testing.T) string { t.Helper(); return require(t, "python3") }
