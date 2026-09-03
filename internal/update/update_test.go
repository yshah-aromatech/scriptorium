package update_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/update"
)

// stubSource is the seam every test in this file drives — nothing here ever
// reaches the network.
type stubSource struct {
	latest      string
	found       bool
	latestErr   error
	replaced    string
	replaceErr  error
	replaceCall int
	gotCurrent  string
}

func (s *stubSource) Latest(context.Context) (string, bool, error) {
	return s.latest, s.found, s.latestErr
}

func (s *stubSource) Replace(_ context.Context, current string) (string, error) {
	s.replaceCall++
	s.gotCurrent = current
	return s.replaced, s.replaceErr
}

func use(t *testing.T, s update.Source) {
	t.Helper()
	restore := update.SetSource(s)
	t.Cleanup(restore)
}

func TestCheckReportsAvailableWhenNewer(t *testing.T) {
	use(t, &stubSource{latest: "v2.0.0", found: true})
	latest, available, err := update.Check("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v2.0.0" || !available {
		t.Errorf("Check() = %q, %v, want v2.0.0, true", latest, available)
	}
}

func TestCheckReportsUnavailableWhenCurrent(t *testing.T) {
	use(t, &stubSource{latest: "v1.0.0", found: true})
	latest, available, err := update.Check("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v1.0.0" || available {
		t.Errorf("Check() = %q, %v, want v1.0.0, false", latest, available)
	}
}

func TestCheckReportsUnavailableWhenCurrentIsNewer(t *testing.T) {
	use(t, &stubSource{latest: "v1.0.0", found: true})
	_, available, err := update.Check("v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Error("available = true when current is already ahead of the latest release")
	}
}

func TestCheckPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	use(t, &stubSource{latestErr: wantErr})
	latest, available, err := update.Check("v1.0.0")
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if latest != "" || available {
		t.Errorf("Check() = %q, %v on error, want zero values", latest, available)
	}
}

func TestCheckReportsUnavailableWhenNoReleaseFound(t *testing.T) {
	use(t, &stubSource{found: false})
	latest, available, err := update.Check("v1.0.0")
	if err != nil || latest != "" || available {
		t.Errorf("Check() = %q, %v, %v, want \"\", false, nil", latest, available, err)
	}
}

// A "dev" build (or any other unparsable current version) is always
// reported behind a real release found — the notice has something useful
// to say no matter which update mechanism (self-update or plain git pull)
// a caller ends up using.
func TestCheckDevCurrentIsAlwaysBehindAFoundRelease(t *testing.T) {
	use(t, &stubSource{latest: "v1.2.3", found: true})
	latest, available, err := update.Check("dev")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v1.2.3" || !available {
		t.Errorf("Check(\"dev\") = %q, %v, want v1.2.3, true", latest, available)
	}
}

func TestCheckDevCurrentWithNoReleaseFoundIsUnavailable(t *testing.T) {
	use(t, &stubSource{found: false})
	_, available, err := update.Check("dev")
	if err != nil || available {
		t.Errorf("Check(\"dev\") available = %v, err = %v, want false, nil", available, err)
	}
}

func TestApplyDelegatesToSource(t *testing.T) {
	s := &stubSource{replaced: "v3.0.0"}
	use(t, s)
	installed, err := update.Apply("v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if installed != "v3.0.0" {
		t.Errorf("installed = %q, want v3.0.0", installed)
	}
	if s.replaceCall != 1 {
		t.Errorf("Replace called %d times, want 1", s.replaceCall)
	}
	if s.gotCurrent != "v2.0.0" {
		t.Errorf("Replace got current=%q, want v2.0.0", s.gotCurrent)
	}
}

func TestApplyPropagatesError(t *testing.T) {
	wantErr := errors.New("network is down")
	use(t, &stubSource{replaceErr: wantErr})
	_, err := update.Apply("v1.0.0")
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

// SetSource's restore must put the previous source back, so tests that
// forget nothing still can't see each other's stubs.
func TestSetSourceRestoresPrevious(t *testing.T) {
	s1 := &stubSource{latest: "v1.0.0", found: true}
	restore1 := update.SetSource(s1)
	s2 := &stubSource{latest: "v2.0.0", found: true}
	restore2 := update.SetSource(s2)

	if latest, _, _ := update.Check("v0.0.0"); latest != "v2.0.0" {
		t.Fatalf("latest = %q, want v2.0.0", latest)
	}
	restore2()
	if latest, _, _ := update.Check("v0.0.0"); latest != "v1.0.0" {
		t.Fatalf("after restore2: latest = %q, want v1.0.0", latest)
	}
	restore1()
}
