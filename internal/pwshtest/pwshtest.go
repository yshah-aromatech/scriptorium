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

// RequirePwsh returns the resolved pwsh path, or fails the test per CI
// policy: os.Getenv("CI") != "" turns a missing pwsh into t.Fatal; otherwise
// it's t.Skip.
func RequirePwsh(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("pwsh")
	fatal, skip := decide(err == nil, os.Getenv("CI"))
	if fatal {
		t.Fatal("pwsh required in CI — interop gate must not skip")
	}
	if skip {
		t.Skip("pwsh not on PATH")
	}
	return p
}
