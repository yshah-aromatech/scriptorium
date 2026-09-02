package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/webhook"
)

// Wave B3: the five PS-floor items wave A left unbound — `/` live filter,
// n/N output search (behind ctrl+f), `U` self-update and `t` webhook test,
// plus restoring the apt stage `u` dropped. See actions.go/outputpane.go/
// runview.go for the implementation; these are the behavioural proofs.

// ---------------------------------------------------------------------------
// / — live script-list filter
// ---------------------------------------------------------------------------

func TestRunViewLiveFilterNarrowsAsYouType(t *testing.T) {
	m := runAt(t, 120, 40)
	press(m, "/")
	if m.ov == nil || m.ov.kind() != overlayInput {
		t.Fatalf("/ opened %T, want the input overlay", m.ov)
	}
	for _, c := range "back" {
		press(m, string(c))
	}
	if got := len(m.run.list.Items()); got != 1 {
		t.Fatalf("filtering to 'back' left %d items, want 1: %+v", got, m.run.list.Items())
	}
	it, ok := m.run.list.Items()[0].(scriptItem)
	if !ok || it.s.Name != "backup-db" {
		t.Errorf("the one filtered item is %+v, want backup-db", it)
	}

	press(m, "esc")
	if m.ov != nil {
		t.Error("esc left the filter prompt open")
	}
	if got := len(m.run.list.Items()); got != len(m.scripts) {
		t.Errorf("esc did not restore the unfiltered list: %d items, want %d", got, len(m.scripts))
	}
}

// A filter that matches nothing says so rather than showing an empty list
// that reads like a bug.
func TestRunViewFilterNoMatches(t *testing.T) {
	m := runAt(t, 120, 40)
	press(m, "/")
	for _, c := range "zzz" {
		press(m, string(c))
	}
	press(m, "enter")
	if !strings.Contains(plainFrame(m), "no matches — esc restores") {
		t.Errorf("a filter with no matches did not say so:\n%s", plainFrame(m))
	}
}

// selectByName (the Fleet deep-link's landing point) has to index into the
// FILTERED list, not m.scripts directly — the root-cause fix for the bug a
// naive filter would introduce.
func TestFilterKeepsDeepLinkSelectionCorrect(t *testing.T) {
	m := runAt(t, 120, 40)
	press(m, "/")
	for _, c := range "heart" {
		press(m, string(c))
	}
	press(m, "enter")
	if m.run.filter != "heart" {
		t.Fatalf("filter = %q, want heart", m.run.filter)
	}
	m.run.selectByName(m, "heartbeat")
	sel := m.run.selected(m)
	if sel == nil || sel.Name != "heartbeat" {
		t.Fatalf("selectByName landed on %+v, want heartbeat", sel)
	}
	if m.run.list.Index() != 0 {
		t.Errorf("index into the filtered list = %d, want 0 (heartbeat is the only match)", m.run.list.Index())
	}
}

// The list title carries the active filter, matching PS's inset " [/filter]".
func TestFilterShowsInTheListTitle(t *testing.T) {
	m := runAt(t, 120, 40)
	press(m, "/")
	for _, c := range "sync" {
		press(m, string(c))
	}
	press(m, "enter")
	if !strings.Contains(plainFrame(m), "[/sync]") {
		t.Errorf("the list title does not show the active filter:\n%s", plainFrame(m))
	}
}

// ---------------------------------------------------------------------------
// ctrl+f / n / N — output search
// ---------------------------------------------------------------------------

func ctrlKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func TestOutputSearchJumpsAndWraps(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.out.reset("output", 5000)

	// gap body-heights of plain lines around and between both matches: large
	// enough, relative to the anchor's own +-body/2 reach, that scrolling to
	// either match never runs into maxScroll's clamp — the edge case that
	// would otherwise make a match sitting near either end of the buffer
	// un-wrappable (the anchor can never scroll past it). The exact viewport
	// height depends on the frame size, so this reads it back rather than
	// assuming one.
	body := max(m.run.out.rows(), 10)
	gap := body * 3
	n := 0
	fill := func() {
		for range gap {
			m.run.out.append(fmt.Sprintf("line %d", n))
			n++
		}
	}
	fill()
	m.run.out.append("needle one")
	fill()
	m.run.out.append("needle two")
	fill()
	m.run.out.toTop() // a deterministic anchor instead of wherever follow left it

	m.Update(ctrlKey('f'))
	if m.ov == nil {
		t.Fatal("ctrl+f did not open the search prompt")
	}
	for _, c := range "needle" {
		press(m, string(c))
	}
	m.Update(cmdMsg(press(m, "enter")))
	if m.run.searchTerm != "needle" {
		t.Fatalf("searchTerm = %q, want needle", m.run.searchTerm)
	}
	if !strings.Contains(m.statusText, "match 1/2 for 'needle'") {
		t.Errorf("status after the first jump = %q", m.statusText)
	}

	// n moves to the second match, then wraps back to the first
	m.Update(cmdMsg(press(m, "n")))
	if !strings.Contains(m.statusText, "match 2/2 for 'needle'") {
		t.Errorf("status after n = %q", m.statusText)
	}
	m.Update(cmdMsg(press(m, "n")))
	if !strings.Contains(m.statusText, "match 1/2 for 'needle'") {
		t.Errorf("n did not wrap to the first match: %q", m.statusText)
	}

	// N from the first match wraps back to the last (nothing precedes it)
	m.Update(cmdMsg(press(m, "N")))
	if !strings.Contains(m.statusText, "match 2/2 for 'needle'") {
		t.Errorf("N did not wrap to the last match: %q", m.statusText)
	}
}

// A match is highlighted in reverse video on screen — the ANSI form, not
// just the plain text (the plain frame can't tell a highlight from a plain
// occurrence of the word).
func TestOutputSearchHighlightsMatches(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.out.append("the needle is here")
	m.run.searchTerm = "needle"
	m.run.out.search = "needle"

	if frame := m.frame(); !strings.Contains(frame, "\x1b[7mneedle\x1b[27m") {
		t.Errorf("the match is not reverse-videoed in the frame:\n%q", frame)
	}
}

// n/N with no search term set says so rather than silently doing nothing —
// Move-TuiSearch's own guard.
func TestOutputSearchWithNoTermWarns(t *testing.T) {
	m := runAt(t, 120, 40)
	msg, ok := cmdMsg(press(m, "n")).(StatusMsg)
	if !ok || !strings.Contains(msg.Text, "no search term") {
		t.Errorf("n with no term gave %#v", msg)
	}
}

// A term with no matches says so.
func TestOutputSearchNoMatches(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.out.append("nothing interesting here")
	m.Update(ctrlKey('f'))
	for _, c := range "xyzzy" {
		press(m, string(c))
	}
	msg := cmdMsg(press(m, "enter"))
	got, ok := msg.(StatusMsg)
	if !ok || !strings.Contains(got.Text, "no matches for 'xyzzy'") {
		t.Errorf("a term with no hits gave %#v", msg)
	}
}

// Empty input clears the search term.
func TestOutputSearchEmptyClears(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.searchTerm = "leftover"
	m.run.out.search = "leftover"
	m.Update(ctrlKey('f'))
	press(m, "backspace", "backspace", "backspace", "backspace", "backspace",
		"backspace", "backspace", "backspace")
	m.Update(cmdMsg(press(m, "enter")))
	if m.run.searchTerm != "" || m.run.out.search != "" {
		t.Errorf("empty submit left searchTerm=%q out.search=%q", m.run.searchTerm, m.run.out.search)
	}
	if !strings.Contains(m.statusText, "search cleared") {
		t.Errorf("status = %q", m.statusText)
	}
}

// ---------------------------------------------------------------------------
// U — self-update (mirrors the MCP update_app op's command assembly)
// ---------------------------------------------------------------------------

func TestSelfUpdateStreamsGitPullAndReportsSuccess(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)
	shimPath(t, map[string]string{"git": "#!/bin/sh\necho \"git $*\"\nexit 0\n"})

	pump(t, m, press(m, "U"), 10*time.Second, nil)
	out := strings.Join(m.run.out.buf.Lines, "\n")
	if !strings.Contains(out, "pull --ff-only") {
		t.Errorf("git was not invoked with the self-update args:\n%s", out)
	}
	if !strings.Contains(out, "self-update (git pull --ff-only) · done") {
		t.Errorf("the task did not report success:\n%s", out)
	}
	if !strings.Contains(m.statusText, "app updated — restart scriptorium to apply") {
		t.Errorf("status = %q", m.statusText)
	}
}

func TestSelfUpdateFailureReportsStatus(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)
	shimPath(t, map[string]string{"git": "#!/bin/sh\nexit 1\n"})

	pump(t, m, press(m, "U"), 10*time.Second, nil)
	if !strings.Contains(m.statusText, "app update failed") {
		t.Errorf("status = %q", m.statusText)
	}
}

// ---------------------------------------------------------------------------
// t — webhook test
// ---------------------------------------------------------------------------

func TestWebhookTestKeySendsAndReportsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := runAt(t, 120, 40)
	m.app.Hook = webhook.NewClient(srv.URL, 2*time.Second, filepath.Join(t.TempDir(), "webhook-queue.jsonl"))

	msg, ok := cmdMsg(press(m, "t")).(StatusMsg)
	if !ok || msg.Kind != StatusOK || !strings.Contains(msg.Text, "webhook test event sent") {
		t.Errorf("t gave %#v", msg)
	}
}

func TestWebhookTestKeyReportsFailure(t *testing.T) {
	m := runAt(t, 120, 40)
	// no URL configured — SendTest's own "disabled" path, exactly like a
	// misconfigured n8nWebhookUrl
	m.app.Hook = webhook.NewClient("", time.Second, filepath.Join(t.TempDir(), "webhook-queue.jsonl"))

	msg, ok := cmdMsg(press(m, "t")).(StatusMsg)
	if !ok || msg.Kind != StatusErr || !strings.Contains(msg.Text, "webhook test FAILED") {
		t.Errorf("t gave %#v", msg)
	}
}

// ---------------------------------------------------------------------------
// u — the restored apt stage
// ---------------------------------------------------------------------------

// With passwordless sudo, apt runs FIRST, then modules, then venvs — three
// stages in that order, restoring Invoke-TuiUpdate in full.
func TestSystemUpdateRunsAptStageFirstWhenSudoAllows(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)
	shimPath(t, map[string]string{
		"sudo": "#!/bin/sh\nexit 0\n",
		"bash": "#!/bin/sh\necho apt-stage-ran\nexit 0\n",
	})

	pump(t, m, press(m, "u"), 10*time.Second, nil)
	out := strings.Join(m.run.out.buf.Lines, "\n")
	aptAt := strings.Index(out, "apt-stage-ran")
	modAt := strings.Index(out, "upgrade script modules · done")
	venvAt := strings.Index(out, "upgrade python venvs · done")
	if aptAt < 0 || modAt < 0 || venvAt < 0 {
		t.Fatalf("not all three stages ran:\n%s", out)
	}
	if !(aptAt < modAt && modAt < venvAt) {
		t.Errorf("stages ran out of order (apt=%d modules=%d venvs=%d):\n%s", aptAt, modAt, venvAt, out)
	}
	if !strings.Contains(m.statusText, "modules and venvs upgraded") {
		t.Errorf("status = %q", m.statusText)
	}
}

// Without passwordless sudo the apt stage is skipped with the manual command
// (never silently) and the module/venv stages still run — the PS behaviour
// wave A intentionally dropped and this restores.
func TestSystemUpdateSkipsAptStageWithoutSudo(t *testing.T) {
	ta := seedToolApp(t, "echo hi\n")
	m := toolModel(t, ta)
	// no "sudo" shim at all: exec.LookPath("sudo") fails in a PATH containing
	// only this shim dir, exactly like a probe with no cached credential and
	// no controlling tty
	shimPath(t, map[string]string{})

	pump(t, m, press(m, "u"), 10*time.Second, nil)
	out := strings.Join(m.run.out.buf.Lines, "\n")
	if !strings.Contains(out, "apt stage skipped: passwordless sudo unavailable") {
		t.Errorf("the skip note is missing:\n%s", out)
	}
	if !strings.Contains(out, "upgrade script modules · done") || !strings.Contains(out, "upgrade python venvs · done") {
		t.Errorf("the module/venv stages did not still run:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// shimPath points PATH at a directory of fake executables for the rest of
// the test — git/sudo/bash are resolved through PATH exactly like PS
// resolves them (unlike pwshBin/pythonBin, which are config paths), so this
// is what stands in for them without ever touching the real binaries.
func shimPath(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		p := filepath.Join(dir, name)
		write(t, p, body)
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}
