package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// ---------------------------------------------------------------------------
// GATE: the exact bytes
// ---------------------------------------------------------------------------

// The OSC 52 sequence, byte for byte, in both forms. These bytes are the whole
// feature: a terminal never answers a clipboard write, so a wrong byte here is
// invisible until someone tries to paste.
func TestOSC52Bytes(t *testing.T) {
	got := osc52("hello")
	want := "\x1b]52;c;aGVsbG8=\a"
	if got != want {
		t.Errorf("osc52 = %q, want %q", got, want)
	}

	// tmux/screen swallow OSC 52 unless it is wrapped in a DCS pass-through
	// with every inner ESC doubled. bubbletea v2.0.9 does NOT do this for us
	// (see clipboard.go's empirical note), which is why we build it here.
	gotTmux := tmuxWrap(got)
	wantTmux := "\x1bPtmux;" + "\x1b\x1b]52;c;aGVsbG8=\a" + "\x1b\\"
	if gotTmux != wantTmux {
		t.Errorf("tmuxWrap = %q, want %q", gotTmux, wantTmux)
	}
	// and it is the PS app's own construction (Copy-StoClipboard): ESC P tmux ;
	// ESC <the whole sequence, itself starting with ESC> ESC backslash
	if wantTmux != "\x1bPtmux;\x1b"+got+"\x1b\\" {
		t.Error("the wrapper diverges from the PS-proven form")
	}
}

// $TMUX / $STY decide the form, and an empty value is not "under a
// multiplexer" (a shell that exported an empty TMUX is not tmux).
func TestUnderMultiplexer(t *testing.T) {
	for _, c := range []struct {
		env  []string
		want bool
	}{
		{[]string{"TERM=xterm"}, false},
		{[]string{"TMUX=/tmp/tmux-1000/default,123,0"}, true},
		{[]string{"STY=4242.pts-0.host"}, true},
		{[]string{"TMUX="}, false},
	} {
		if got := underMultiplexer(c.env); got != c.want {
			t.Errorf("underMultiplexer(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}

// The 72 KB cap keeps the TAIL: a sequence over the terminal's limit is
// dropped whole, so the end of the output is the half worth keeping.
func TestClipboardCapKeepsTheTail(t *testing.T) {
	text := strings.Repeat("a", 100*1024) + "END"
	got, capped := capClipboard(text)
	if !capped {
		t.Fatal("a 100KB payload was not capped")
	}
	if len(got) > clipboardCap {
		t.Errorf("capped payload is %d bytes, over the %d cap", len(got), clipboardCap)
	}
	if !strings.HasSuffix(got, "END") {
		t.Error("the cap kept the head instead of the tail")
	}
	if short, capped := capClipboard("small"); capped || short != "small" {
		t.Errorf("a short payload was capped: %q %v", short, capped)
	}
	// the cut never lands inside a rune
	wide := strings.Repeat("日", 40*1024) // 3 bytes each, well over the cap
	cut, _ := capClipboard(wide)
	if !strings.HasPrefix(cut, "日") || strings.ContainsRune(cut, '�') {
		t.Error("the cap split a multi-byte rune")
	}
}

// Under tmux the copy goes out as a RAW write of the wrapped bytes (bubbletea
// has no tmux path); otherwise it hands the payload to tea.SetClipboard.
func TestCopyPicksTheRightMechanism(t *testing.T) {
	m := newFixtureModel(t, truecolorEnv)
	noTools(t)

	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	if msg := findMsg[tea.RawMsg](t, m.copyToClipboard("hello")); fmt.Sprint(msg.Msg) != tmuxWrap(osc52("hello")) {
		t.Errorf("under tmux the raw write was %q", fmt.Sprint(msg.Msg))
	}

	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	cmds := batchCmds(m.copyToClipboard("hello"))
	var handed bool
	for _, c := range cmds {
		if got := fmt.Sprint(c()); got == "hello" {
			handed = true
		}
	}
	if !handed {
		t.Error("outside tmux the payload never reached tea.SetClipboard")
	}

	// either way the toast reports what was copied
	rep := findMsg[ClipboardMsg](t, m.copyToClipboard("hello"))
	if rep.Chars != 5 || !strings.Contains(rep.How, "OSC 52") {
		t.Errorf("clipboard report = %+v", rep)
	}
}

// GATE: the exec fallback's argv and its stdin. The text goes in VERBATIM —
// a trailing newline here is a newline in everyone's paste buffer.
func TestExecFallbackArgvAndStdin(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	// wl-copy is first in the chain, so a shim for it is the one that must run
	// PATH is the shim dir ALONE (so no real clipboard tool can be reached),
	// which means the shim's own helpers have to be absolute paths.
	write(t, filepath.Join(dir, "wl-copy"), fmt.Sprintf(`#!/bin/sh
printf 'argv:%%s\n' "$*" >> %[1]q
printf 'stdin:' >> %[1]q
/bin/cat >> %[1]q
printf '\n' >> %[1]q
`, record))
	if err := os.Chmod(filepath.Join(dir, "wl-copy"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	tool, ok := execCopy("line one\nline two")
	if !ok || tool != "wl-copy" {
		t.Fatalf("execCopy = %q %v, want wl-copy", tool, ok)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "argv:\nstdin:line one\nline two\n"
	if string(got) != want {
		t.Errorf("the shim recorded %q, want %q (no arguments, stdin verbatim)", got, want)
	}
}

// The chain order is the PS one, and a missing tool is skipped rather than
// failing the copy.
func TestExecFallbackSkipsMissingTools(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	// only xsel exists: wl-copy and xclip must be skipped, not attempted
	write(t, filepath.Join(dir, "xsel"), fmt.Sprintf("#!/bin/sh\nprintf 'xsel:%%s\\n' \"$*\" >> %q\n/bin/cat > /dev/null\n", record))
	if err := os.Chmod(filepath.Join(dir, "xsel"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	tool, ok := execCopy("x")
	if !ok || tool != "xsel" {
		t.Fatalf("execCopy = %q %v, want xsel", tool, ok)
	}
	got, _ := os.ReadFile(record)
	if string(got) != "xsel:--clipboard --input\n" {
		t.Errorf("xsel argv = %q", got)
	}

	// with nothing on PATH the copy still happens (OSC 52), it just has no
	// local tool to report
	t.Setenv("PATH", t.TempDir())
	if tool, ok := execCopy("x"); ok {
		t.Errorf("execCopy found %q on an empty PATH", tool)
	}
}

// ---------------------------------------------------------------------------
// GATE: the drag rejoin
// ---------------------------------------------------------------------------

// Dragging across a WRAPPED line copies the original unwrapped source text.
// The CJK line is deliberate: it is the case the phase-10 UTF-16 midpoint fix
// was for, where the wrap's break CHOICE differs if the midpoint comparison is
// made in the wrong units — so it also pins that the rejoin reads the spans the
// wrapper actually produced.
func TestDragCopiesTheOriginalUnwrappedText(t *testing.T) {
	m := runAt(t, 120, 40)
	noTools(t)
	m.focus = focusOutput

	long := "the quick brown fox jumps over the lazy dog and keeps running well past the edge of the pane"
	cjk := "日本語 abcdefgh ijklmnop qrstuvwx 日本語テキストが折り返される長い行 the end"
	m.run.out.reset("output", 5000)
	m.run.out.resize(40, 20) // narrow enough to fold both lines several times
	m.run.out.append(long, cjk)

	if len(m.run.out.buf.Wrapped) <= 2 {
		t.Fatalf("the fixture lines did not wrap: %q", m.run.out.buf.Wrapped)
	}
	for i, src := range []string{long, cjk} {
		from, to := rowsOf(t, &m.run.out, i)
		m.run.out.beginDrag(from, 0)
		m.run.out.dragTo(to, maxCell)
		if got := m.run.out.selectedText(); got != src {
			t.Errorf("rejoined line %d as\n  %q\nwant the original\n  %q", i, got, src)
		}
	}

	// and across BOTH lines the real line break survives as a newline
	last := len(m.run.out.buf.Wrapped) - 1
	m.run.out.beginDrag(0, 0)
	m.run.out.dragTo(last, maxCell)
	if got := m.run.out.selectedText(); got != long+"\n"+cjk {
		t.Errorf("a two-line drag rejoined as %q", got)
	}
}

// rowsOf is the first and last wrapped row belonging to source line i.
func rowsOf(t *testing.T, o *outputPane, line int) (first, last int) {
	t.Helper()
	first, last = -1, -1
	for i, src := range o.buf.WrapSrc {
		if src.Line == line {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		t.Fatalf("source line %d produced no wrapped rows", line)
	}
	return first, last
}

// A drag is drawn in reverse video while it is live, and released it copies.
func TestDragGestureSelectsAndCopies(t *testing.T) {
	m := runAt(t, 120, 40)
	noTools(t)
	lay := runLayoutFor(120, m.bodyHeight())
	x0 := lay.listW + 1

	m.run.out.reset("output", 5000)
	m.run.out.resize(lay.outW, m.bodyHeight())
	m.run.out.append("device code ABCD1234XY here", "second line of output")

	// press → drag → release across the first line
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x0, Y: headerRows + 1})
	if m.run.out.anchor == nil {
		t.Fatal("the press did not arm a drag")
	}
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x0 + 10, Y: headerRows + 1})
	if !m.run.out.selecting() {
		t.Fatal("motion did not extend the selection")
	}
	frame := m.frame()
	if !strings.Contains(frame, "\x1b[7m") {
		t.Error("the live selection is not drawn in reverse video")
	}
	// the pane un-follows while a selection is live, so it cannot scroll away
	if m.run.out.follow {
		t.Error("a drag left the pane following the tail")
	}

	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x0 + 10, Y: headerRows + 1})
	if m.run.out.selecting() {
		t.Error("the release did not clear the selection")
	}
	rep := findMsg[ClipboardMsg](t, cmd)
	if rep.Chars != 11 {
		t.Errorf("copied %d chars, want the 11 dragged ones", rep.Chars)
	}
}

// A plain click (no drag) on a device-login code copies it — the PS
// click-to-copy (§1.11). A click on ordinary text copies nothing.
func TestClickToCopyDeviceCode(t *testing.T) {
	m := runAt(t, 120, 40)
	noTools(t)
	lay := runLayoutFor(120, m.bodyHeight())
	x0 := lay.listW + 1

	m.run.out.reset("output", 5000)
	m.run.out.resize(lay.outW, m.bodyHeight())
	m.run.out.append("enter the code ABCD1234XY to sign in")

	// click on the code
	col := strings.Index("enter the code ABCD1234XY to sign in", "ABCD1234XY") + 2
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x0 + col, Y: headerRows + 1})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x0 + col, Y: headerRows + 1})
	rep := findMsg[ClipboardMsg](t, cmd)
	if rep.Chars != 10 {
		t.Errorf("copied %d chars, want the 10-character code", rep.Chars)
	}

	// click on an ordinary word copies nothing
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x0 + 2, Y: headerRows + 1})
	if _, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x0 + 2, Y: headerRows + 1}); cmd != nil {
		t.Errorf("a click on ordinary text produced %#v", cmdMsg(cmd))
	}
}

// `y` copies the WHOLE retained buffer out of the REDACTED store — every line,
// not the screenful the pane happens to be showing (Invoke-TuiCopy,
// src/Tui.psm1:913).
//
// DELIBERATE REPLACEMENT of the phase-11 wave-A test that asserted the
// opposite ("the copy is not the VISIBLE window" — it required `line 0` to be
// ABSENT). That assertion locked in a floor cut: PS copies the scrollback, and
// a one-screen copy is a strict subset of it, not a substitute. The visible-
// window copy no longer exists; drag-copy is what bounds a copy to a region.
func TestCopyCopiesTheWholeBuffer(t *testing.T) {
	m := runAt(t, 120, 40)
	noTools(t)
	m.run.out.reset("output", 5000)
	m.run.out.resize(runLayoutFor(120, m.bodyHeight()).outW, 6)
	for i := range 20 {
		m.run.out.append(fmt.Sprintf("line %d", i))
	}

	rep := findMsg[ClipboardMsg](t, press(m, "y"))
	text := m.run.out.allText()
	if rep.Chars != len([]rune(text)) {
		t.Errorf("the toast reported %d chars for %q", rep.Chars, text)
	}
	// every line, including the ones scrolled off the top of a 6-row pane
	for _, want := range []string{"line 0\n", "line 9\n", "line 19"} {
		if !strings.Contains(text, want) {
			t.Errorf("the copy is missing %q — it is not the whole buffer:\n%q", want, text)
		}
	}
	// SOURCE lines, not wrapped rows: the pane's own line breaks must not end
	// up in someone's paste buffer
	if got := strings.Count(text, "\n"); got != 19 {
		t.Errorf("the copy has %d newlines for 20 source lines: %q", got, text)
	}
	send(m, ClipboardMsg{How: "OSC 52", Chars: rep.Chars})
	if !strings.Contains(textkit.StripANSI(m.statusBar()), "copied") {
		t.Errorf("no copy toast: %q", m.statusText)
	}
}

// `c` empties the panel (Clear-TuiOutput, src/Tui.psm1:597): the buffer, the
// scroll, the selection, and back to following the tail.
func TestClearOutputPanel(t *testing.T) {
	m := runAt(t, 120, 40)
	m.run.out.reset("output", 5000)
	m.run.out.resize(runLayoutFor(120, m.bodyHeight()).outW, 6)
	for i := range 20 {
		m.run.out.append(fmt.Sprintf("line %d", i))
	}
	m.run.out.scrollBy(-5)
	m.run.out.beginDrag(0, 0)
	m.run.out.dragTo(1, 4)

	send(m, keyMsg("c"))
	if got := len(m.run.out.buf.Lines); got != 0 {
		t.Errorf("`c` left %d lines in the buffer", got)
	}
	if len(m.run.out.buf.Wrapped) != 0 || len(m.run.out.buf.WrapSrc) != 0 {
		t.Error("`c` left the wrap cache behind")
	}
	if m.run.out.scroll != 0 || !m.run.out.follow {
		t.Errorf("`c` left scroll=%d follow=%v", m.run.out.scroll, m.run.out.follow)
	}
	if m.run.out.selecting() {
		t.Error("`c` left a selection pointing into a buffer that is gone")
	}
	if !strings.Contains(m.statusText, "output cleared") {
		t.Errorf("status = %q", m.statusText)
	}
	if !strings.Contains(plainFrame(m), "─ output") {
		t.Errorf("the pane vanished instead of emptying:\n%s", plainFrame(m))
	}
}

// A live drag is a visual state like any other, so it gets its own frames: the
// reverse-video span, at all three sizes, driven by real mouse messages.
func TestGoldensDragSelection(t *testing.T) {
	goldenFrames(t, "run-selection", func(t *testing.T, env []string, w, h int) *Model {
		m := newFixtureModel(t, env)
		m.mode = modeRun
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.run.selectByName(m, "backup-db")
		m.run.onRunStarted(m, RunStartedMsg{
			Script:    m.scripts[0],
			Handle:    fakeHandle("backup-db"),
			StartedAt: frozen.Add(-3 * time.Second),
		})
		m.run.out.append(
			"connecting to postgres://db.internal:5432",
			"sign in at https://microsoft.com/devicelogin with code F7KQ2M9XZ4",
			"dumping schema public (18 tables)")

		// body rows 7 and 8 are the "connecting…" line and the one after it at
		// every width — the drag crosses a real line break, which is the case
		// the rejoin has to keep as a newline
		x0 := runLayoutFor(w, m.bodyHeight()).listW + 1
		m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x0 + 11, Y: headerRows + 7})
		m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x0 + 24, Y: headerRows + 8})
		return m
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// noTools empties PATH so no test ever reaches a real clipboard helper on the
// machine running the suite.
func noTools(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// findMsg runs a command (flattening one batch level) and returns the first
// message of type T.
func findMsg[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	var zero T
	for _, c := range batchCmds(cmd) {
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, inner := range batch {
				if got, ok := inner().(T); ok {
					return got
				}
			}
			continue
		}
		if got, ok := msg.(T); ok {
			return got
		}
	}
	t.Fatalf("no %T among the command's messages", zero)
	return zero
}
