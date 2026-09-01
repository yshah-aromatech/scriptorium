package runner

import (
	"testing"
	"time"
)

// The timeout notice interpolates the minutes exactly as PowerShell does.
// Every expectation below is what live pwsh prints for `"$([double]x)"` —
// .NET's "G15", which PS restored after .NET Core 3.0 switched the default
// double format to the shortest round-trip form.
func TestFormatMinutesMatchesPSDoubleInterpolation(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{6 * time.Second, "0.1"},                       // 0.1
		{2 * time.Second, "0.0333333333333333"},        // 2/60 — G15, not the round-trip 0.03333333333333333
		{time.Second, "0.0166666666666667"},            // 1/60
		{30 * time.Minute, "30"},                       // trailing zeros trimmed
		{90 * time.Second, "1.5"},                      //
		{0, "0"},                                       // no timeout ever reaches the notice, but the format holds
		{time.Duration(1<<63 - 1), "153722867.280913"}, // the largest Duration still formats decimal, not as an exponent
	}
	for _, c := range cases {
		if got := formatMinutes(c.in); got != c.want {
			t.Errorf("formatMinutes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
