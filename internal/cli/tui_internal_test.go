package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/app"
)

// Bare invocation launches the TUI — the stub that stood here through P1-P9 is
// gone. Booting a real Bubble Tea program needs a terminal, so these drive the
// wiring through launchTUI: that it is reached, that it is handed the opened
// app, that a clean quit is exit 0 and a failure is exit 1 with the error on
// stderr. The TUI's own behaviour is covered in internal/tui.

func swapLaunch(t *testing.T, fn func(*app.App) error) {
	t.Helper()
	prev := launchTUI
	launchTUI = fn
	t.Cleanup(func() { launchTUI = prev })
}

// tuiTestApp points the resolver at a temp app dir with its own data dir, so
// nothing here reads the real ~/.scriptorium.
func tuiTestApp(t *testing.T) string {
	t.Helper()
	t.Setenv("N8N_WEBHOOK_URL", "")
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := fmt.Sprintf(`{"dataDir":%q}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIPTORIUM_APP_DIR", appDir)
	return dataDir
}

func TestBareInvocationLaunchesTheTUI(t *testing.T) {
	dataDir := tuiTestApp(t)

	var got *app.App
	swapLaunch(t, func(a *app.App) error { got = a; return nil })

	var out, errw bytes.Buffer
	if code := Main(nil, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errw.String())
	}
	if got == nil {
		t.Fatal("bare invocation did not reach the TUI")
	}
	// the opened app is handed over, not a second open
	if got.Paths.DataDir != dataDir {
		t.Errorf("the TUI got dataDir %q, want %q", got.Paths.DataDir, dataDir)
	}
	if out.Len() != 0 {
		t.Errorf("bare invocation wrote to stdout: %q", out.String())
	}
}

func TestBareInvocationReportsATUIFailure(t *testing.T) {
	tuiTestApp(t)
	swapLaunch(t, func(*app.App) error { return errors.New("no terminal attached") })

	var out, errw bytes.Buffer
	if code := Main(nil, &out, &errw); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !bytes.Contains(errw.Bytes(), []byte("no terminal attached")) {
		t.Errorf("stderr = %q, want the TUI's error", errw.String())
	}
}

// Config warnings still print before the TUI takes the terminal — they are the
// only chance a user has to see them before the alt-screen opens.
func TestBareInvocationPrintsWarningsFirst(t *testing.T) {
	tuiTestApp(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("SCRIPTORIUM_APP_DIR"), "config.json"),
		[]byte(`{"dataDir":"`+t.TempDir()+`","notAKey":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	swapLaunch(t, func(*app.App) error { return nil })

	var out, errw bytes.Buffer
	if code := Main(nil, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !bytes.Contains(errw.Bytes(), []byte("WARNING:")) {
		t.Errorf("stderr = %q, want the config warning", errw.String())
	}
}

// A flag-parsing regression must not fall through to the TUI.
func TestFlaggedInvocationsDoNotLaunchTheTUI(t *testing.T) {
	tuiTestApp(t)
	launched := false
	swapLaunch(t, func(*app.App) error { launched = true; return nil })

	for _, args := range [][]string{{"--help"}, {"--list"}, {"--history"}} {
		var out, errw bytes.Buffer
		Main(args, &out, &errw)
		if launched {
			t.Fatalf("%v fell through to the TUI", args)
		}
	}
}
