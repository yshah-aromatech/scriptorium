package mcp

import (
	"os/exec"
	"strings"
	"testing"
)

// TestAppVersion is judgment-call 1's follow-up: appVersion's own derivation
// (git -C <appDir> rev-parse --short HEAD) had no direct test — only the
// fixture replay's normalized-away usage of it. A white-box (package mcp)
// test is needed since appVersion is unexported.
func TestAppVersion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	t.Run("not a git repo returns empty", func(t *testing.T) {
		if got := appVersion(t.TempDir()); got != "" {
			t.Errorf("appVersion(non-git dir) = %q, want \"\"", got)
		}
	})

	t.Run("a real checkout returns its HEAD short SHA", func(t *testing.T) {
		dir := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
			cmd.Env = append(cmd.Env,
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		run("init", "-q")
		run("commit", "--allow-empty", "-q", "-m", "x")

		want, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		wantStr := strings.TrimSpace(string(want))

		if got := appVersion(dir); got != wantStr {
			t.Errorf("appVersion(dir) = %q, want %q", got, wantStr)
		}
	})
}
