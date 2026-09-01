package pwshtest

import "testing"

// White-box test of the CI-loud policy itself — exercised as a pure
// function so a deliberately-triggered "must fail in CI" case doesn't mark
// this package's own test run as failed.
func TestDecide(t *testing.T) {
	cases := []struct {
		name      string
		found     bool
		ci        string
		wantFatal bool
		wantSkip  bool
	}{
		{"found regardless of CI", true, "1", false, false},
		{"found, no CI", true, "", false, false},
		{"missing in CI is fatal", false, "1", true, false},
		{"missing locally skips", false, "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fatal, skip := decide(c.found, c.ci)
			if fatal != c.wantFatal || skip != c.wantSkip {
				t.Fatalf("decide(%v, %q) = (%v, %v), want (%v, %v)", c.found, c.ci, fatal, skip, c.wantFatal, c.wantSkip)
			}
		})
	}
}
