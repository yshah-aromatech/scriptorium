package buildinfo

import "testing"

func TestVersionDefault(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must default to a non-empty string")
	}
	if Version != "dev" {
		t.Fatalf("unlinked builds report %q, want \"dev\"", Version)
	}
}
