package tui

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// The clipboard stack.
//
// EMPIRICAL FINDING (bubbletea v2.0.9, x/ansi v0.11.8): tea.SetClipboard emits
// a bare `ESC ] 52 ; c ; <base64> BEL` and NOTHING in either package wraps it
// for tmux or screen — there is no DCS pass-through anywhere in the runtime.
// The architecture note assumed v2 might do it; it does not. So the
// hand-rolled wrapper is not a fallback here, it is the only path that works
// under a multiplexer, and it is gated on $TMUX/$STY exactly as the PS app
// gates its own (Copy-StoClipboard, src/Core.psm1:702).
//
// Order of mechanisms, and why it is not the PS order:
//
//  1. OSC 52, always. This app is operated over SSH more often than not, and
//     OSC 52 is the only mechanism that reaches the clipboard of the machine
//     the human is sitting at. wl-copy on the server fills the SERVER's
//     clipboard, which nobody can paste from.
//  2. A local clipboard tool, when one is installed — wl-copy → xclip → xsel,
//     in the PS chain's order, stdin verbatim with no trailing newline. This is
//     the path that works when you ARE sitting at the machine and its terminal
//     has OSC 52 turned off, which is a common default.
//
// Both are attempted; the toast says which ones landed. Neither can be
// verified from here (a terminal never answers an OSC 52 write), so reporting
// what was attempted is the honest thing a copy can say.

// clipboardCap is the OSC 52 payload ceiling. Terminals cap the sequence
// (commonly around 100 KB of base64), and a sequence over the cap is dropped
// WHOLE — so the text is truncated to its last 72 KB rather than silently
// vanishing. The exec path has no such limit and gets the full text.
const clipboardCap = 72 * 1024

// clipTools is the exec chain, in Copy-StoClipboard's order.
var clipTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// clipToolTimeout bounds a hung clipboard helper. xclip in particular forks and
// holds the selection; a helper that never exits must not wedge the command.
const clipToolTimeout = 3 * time.Second

// osc52 is the OSC 52 set-clipboard sequence for text, byte for byte what
// bubbletea's own tea.SetClipboard produces.
func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}

// tmuxWrap wraps a sequence in tmux's DCS pass-through, doubling every inner
// ESC. tmux and screen swallow OSC 52 without it (and tmux additionally needs
// `allow-passthrough on`).
func tmuxWrap(seq string) string {
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// underMultiplexer reports whether the OSC 52 write has to survive a tmux or
// screen session. env is passed in so a test never depends on the process's own.
func underMultiplexer(env []string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "STY=") {
			if v := e[strings.IndexByte(e, '=')+1:]; v != "" {
				return true
			}
		}
	}
	return false
}

// capClipboard truncates text to the last clipboardCap bytes, cut forward to a
// rune boundary so the payload is never half a character.
func capClipboard(text string) (string, bool) {
	if len(text) <= clipboardCap {
		return text, false
	}
	cut := text[len(text)-clipboardCap:]
	for len(cut) > 0 && !utf8.RuneStart(cut[0]) {
		cut = cut[1:]
	}
	return cut, true
}

// copyToClipboard is the whole stack as one command. It is a command because
// the exec half runs child processes, which may not happen inside Update.
func (m *Model) copyToClipboard(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return status(StatusWarn, "nothing to copy")
	}
	env := os.Environ()
	return func() tea.Msg {
		payload, capped := capClipboard(text)
		how := []string{"OSC 52"}
		if capped {
			how[0] = "OSC 52 (last 72KB)"
		}
		if tool, ok := execCopy(text); ok {
			how = append(how, tool)
		}
		report := func() tea.Msg {
			return ClipboardMsg{How: strings.Join(how, " + "), Chars: utf8.RuneCountInString(text)}
		}
		if underMultiplexer(env) {
			// bubbletea has no tmux path of its own — write the wrapped bytes
			return tea.Batch(tea.Raw(tmuxWrap(osc52(payload))), report)()
		}
		return tea.Batch(tea.SetClipboard(payload), report)()
	}
}

// execCopy pipes text into the first clipboard helper on PATH that accepts it.
// The text goes in VERBATIM — no trailing newline — which is why this writes to
// a pipe rather than echoing through a shell.
func execCopy(text string) (string, bool) {
	for _, argv := range clipTools {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdin = strings.NewReader(text)
		if err := c.Start(); err != nil {
			continue
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				return argv[0], true
			}
		case <-time.After(clipToolTimeout):
			_ = c.Process.Kill()
			<-done
		}
	}
	return "", false
}

// onClipboard is the toast (§1.11's "copied N chars").
func (m *Model) onClipboard(msg ClipboardMsg) tea.Cmd {
	return status(StatusOK, "copied "+strconv.Itoa(msg.Chars)+" chars · "+msg.How)
}
