package scripts

import "testing"

// M6: a lowercase-first tiebreak for case-only name ties, matching PS's
// verified [string]::Compare("A","a") == 1 semantics (see the fix-wave
// report for the live-pwsh transcript). Exercised as an internal
// (white-box) test because a case-insensitive filesystem — e.g. macOS's
// default APFS volume — can't hold two on-disk entries differing only by
// case, so this can't be driven through Discover() against real folders.
func TestSortNamesCILowercaseFirstTiebreak(t *testing.T) {
	names := []string{"Foo", "foo"}
	sortNamesCI(names)
	want := []string{"foo", "Foo"}
	if names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("got %v, want %v", names, want)
	}
}
