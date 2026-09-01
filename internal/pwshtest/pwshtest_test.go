package pwshtest_test

import (
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

// Locally (CI unset), a missing pwsh must skip, not fail — a Skip'd subtest
// still reports the parent as passed.
func TestRequirePwshSkipsLocallyWhenMissing(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("PATH", t.TempDir())
	ok := t.Run("inner", func(t *testing.T) {
		pwshtest.RequirePwsh(t)
	})
	if !ok {
		t.Fatal("expected inner subtest to skip (counts as passed), not fail")
	}
}

func TestRequirePwshFindsRealPwsh(t *testing.T) {
	p := pwshtest.RequirePwsh(t)
	if p == "" {
		t.Fatal("expected a resolved pwsh path")
	}
}
