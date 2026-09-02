package tui

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/runner"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

// The Run view's action keys (inventory §1.3), each with the overlay or task it
// needs behind it. Every one of them is a command: nothing here blocks the
// update loop, and nothing here starts a goroutine that touches the model.

// args is `a`: ask for extra arguments, then run with them. Quote-aware
// splitting is the runner's Split-StoArguments port, so the TUI and the CLI
// interpret a quoted argument identically.
func (r *runModel) args(m *Model) tea.Cmd {
	s := r.selected(m)
	if s == nil {
		return status(StatusWarn, "no script selected")
	}
	name := s.Name
	m.open(newInput(m, inputArgs, "extra args for "+name+" (quotes group words)", "",
		func(m *Model, value string) tea.Cmd {
			// re-resolve by name: the list can have been replaced by a sync
			// while the prompt was open
			target := m.run.byName(m, name)
			if target == nil {
				return status(StatusWarn, "script '"+name+"' no longer exists")
			}
			return m.run.start(m, *target, runner.SplitArguments(value)...)
		}))
	return nil
}

// byName re-resolves a script against the current list.
func (r *runModel) byName(m *Model, name string) *scripts.Script {
	for i := range m.scripts {
		if m.scripts[i].Name == name {
			return &m.scripts[i]
		}
	}
	return nil
}

// editEnv is `e`: the .env editor.
func (r *runModel) editEnv(m *Model) tea.Cmd {
	s := r.selected(m)
	if s == nil {
		return status(StatusWarn, "no script selected")
	}
	m.open(newEnvEditor(m, *s))
	return nil
}

// ---------------------------------------------------------------------------
// Dependencies
// ---------------------------------------------------------------------------

// scanDeps is the dependency check, off the update loop (it shells out to pwsh
// or pip). installOnly marks the `i` path, which never runs the script.
func (r *runModel) scanDeps(m *Model, s scripts.Script, args []string, installOnly bool) tea.Cmd {
	a := m.app
	return func() tea.Msg {
		msg := DepsScannedMsg{Script: s, Args: args, InstallOnly: installOnly}
		if s.Runtime == "python" {
			missing, err := a.Scanner.ScanPython(s.Dir, s.VenvDir, a.Cfg.PythonBin)
			msg.Missing, msg.Err = missing, err
			return msg
		}
		res, err := a.Scanner.ScanPS(s.Entry, s.Dir, s.ModuleDir, s.Loose)
		msg.Missing, msg.Err = res.Missing, err
		msg.Degraded, msg.Warning = res.Degraded, res.Warning
		return msg
	}
}

// onDepsScanned decides what the scan means. A scan that could not run does
// NOT block the script: PS's Get-StoMissingDeps failure yields an empty missing
// list and the run proceeds, and a degraded PowerShell scan cannot install
// anything anyway — the warning is visible, the run is not held hostage to it.
func (r *runModel) onDepsScanned(m *Model, msg DepsScannedMsg) tea.Cmd {
	if msg.InstallOnly {
		r.out.append("declared missing: " + displayList(msg.Missing))
	}
	switch {
	case msg.Err != nil:
		if msg.InstallOnly {
			return status(StatusErr, "dependency scan failed: "+msg.Err.Error())
		}
		return r.launch(m, msg.Script, msg.Args)
	case msg.Degraded:
		note := status(StatusWarn, "dependency scan degraded: "+msg.Warning)
		if msg.InstallOnly {
			return note
		}
		return tea.Batch(note, r.launch(m, msg.Script, msg.Args))
	case len(msg.Missing) == 0:
		if msg.InstallOnly {
			return status(StatusOK, msg.Script.Name+": every dependency is installed")
		}
		return r.launch(m, msg.Script, msg.Args)
	}
	m.open(&depsOverlay{
		script: msg.Script, missing: msg.Missing,
		args: msg.Args, installOnly: msg.InstallOnly,
	})
	return nil
}

func displayList(ds []deps.Dep) string {
	if len(ds) == 0 {
		return "(none)"
	}
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Display
	}
	return strings.Join(names, ", ")
}

// depScan is `i`: scan and report, opening the prompt only if something is
// actually missing.
func (r *runModel) depScan(m *Model) tea.Cmd {
	s := r.selected(m)
	if s == nil {
		return status(StatusWarn, "no script selected")
	}
	r.out.begin("deps: " + s.Name)
	r.out.append("", banner("▶ dependency scan: "+s.Name, r.out.contentWidth()))
	return r.scanDeps(m, *s, nil, true)
}

// ---------------------------------------------------------------------------
// Tool tasks
// ---------------------------------------------------------------------------

// lint is `l` (inventory §9.8).
func (r *runModel) lint(m *Model) tea.Cmd {
	s := r.selected(m)
	if s == nil {
		return status(StatusWarn, "no script selected")
	}
	cmd := deps.LintCommand(deps.LintTarget{
		Runtime:  s.Runtime,
		Entry:    s.Entry,
		VenvDir:  s.VenvDir,
		ToolsDir: filepath.Join(m.app.Paths.DataDir, "tools"),
	}, m.app.Cfg.PythonBin)
	return r.pwshTask(m, "lint: "+s.Name, cmd, nil)
}

// systemUpdate is `u`: apt (passwordless sudo only), then the installed
// PowerShell modules, then every python venv — Invoke-TuiUpdate's three
// stages (Tui.psm1:881-911). Wave A shipped only the last two, deliberately:
// a TUI that silently does nothing because sudo wanted a password on a pipe
// it does not have is worse than one that upgrades modules and venvs and
// says so. This restores the apt stage as the FIRST streamed task — the
// probe below decides whether it runs at all, and either way the module and
// venv stages that follow are unconditional.
func (r *runModel) systemUpdate(m *Model) tea.Cmd {
	a := m.app
	modules := deps.ModuleUpgradeCommand(a.Paths.ModulesDir)
	venvs := deps.VenvUpgradeCommand(a.Paths.VenvsDir, a.Cfg.PythonBin)
	moduleStage := func(m *Model, _ bool) tea.Cmd {
		return m.run.pwshTask(m, "upgrade script modules", modules, func(m *Model, _ bool) tea.Cmd {
			return m.run.pwshTask(m, "upgrade python venvs", venvs, func(m *Model, ok bool) tea.Cmd {
				if ok {
					return status(StatusOK, "modules and venvs upgraded")
				}
				return status(StatusWarn, "the venv upgrade reported a problem — see the output pane")
			})
		})
	}
	return r.startTask(m, "system update (apt)", func(ctx context.Context, emit func(string)) bool {
		return aptUpgrade(ctx, a.Sec, emit)
	}, moduleStage)
}

// aptUpgrade probes for passwordless sudo and runs deps.AptUpgradeScript only
// if it answers — the exact PS gate (`sudo -n true`). A failed probe is not a
// task failure: it just skips the stage with the manual command, and the
// caller's after-hook (the module/venv stages) still runs either way.
func aptUpgrade(ctx context.Context, reg *secret.Registry, emit func(string)) bool {
	if exec.CommandContext(ctx, "sudo", "-n", "true").Run() != nil {
		emit(deps.AptSkipNote)
		return true
	}
	return streamCmd(ctx, reg, emit, "bash", "-c", deps.AptUpgradeScript)
}

// selfUpdate is `U`: git pull --ff-only in the app directory, streamed — the
// same command assembly as the MCP update_app op (deps.GitPullFFOnlyArgs),
// not a second copy of it.
func (r *runModel) selfUpdate(m *Model) tea.Cmd {
	appDir := m.app.Paths.AppDir
	return r.startTask(m, "self-update (git pull --ff-only)", func(ctx context.Context, emit func(string)) bool {
		return streamCmd(ctx, m.app.Sec, emit, "git", deps.GitPullFFOnlyArgs(appDir)...)
	}, func(m *Model, ok bool) tea.Cmd {
		if ok {
			return status(StatusOK, "app updated — restart scriptorium to apply")
		}
		return status(StatusErr, "app update failed — see the output pane")
	})
}

// webhookTest is `t`: Send-StoWebhookTest, off the update loop (it is a
// network call) — PS's own status text, verbatim.
func (r *runModel) webhookTest(m *Model) tea.Cmd {
	hook := m.app.Hook
	return func() tea.Msg {
		if hook.SendTest() {
			return StatusMsg{Kind: StatusOK, Text: "webhook test event sent"}
		}
		return StatusMsg{Kind: StatusErr, Text: "webhook test FAILED — check n8nWebhookUrl"}
	}
}

// startFilter is `/`: the live script-list filter (floor item). Typed
// characters narrow the list on every keystroke; esc restores what was there
// (newInput's live-filter contract).
func (r *runModel) startFilter(m *Model) tea.Cmd {
	m.open(newInput(m, inputFilter, "filter the script list (live, esc restores)", r.filter,
		func(m *Model, value string) tea.Cmd {
			if value == "" {
				return status(StatusInfo, "filter cleared")
			}
			return status(StatusInfo, "filtered: '"+value+"'")
		}).live(func(m *Model, value string) tea.Cmd {
		r.filter = value
		r.reload(m)
		return nil
	}))
	return nil
}

// startSearch is ctrl+f: set (or clear) the output pane's search term —
// Move-TuiSearch's own prompt, pre-filled with the current term.
func (r *runModel) startSearch(m *Model) tea.Cmd {
	m.open(newInput(m, inputSearch, "search output (empty = clear)", r.searchTerm,
		func(m *Model, value string) tea.Cmd {
			r.searchTerm = strings.TrimSpace(value)
			r.out.search = r.searchTerm
			if r.searchTerm == "" {
				return status(StatusInfo, "search cleared")
			}
			return r.searchJump(1)
		}))
	return nil
}

// ---------------------------------------------------------------------------
// Logs and history
// ---------------------------------------------------------------------------

// viewLog is `v`: read the selected script's most recent run log back into the
// output pane. The log on disk is the redacted one the runner wrote, so this
// cannot resurrect a secret the pane would otherwise never have seen.
func (r *runModel) viewLog(m *Model) tea.Cmd {
	s := r.selected(m)
	if s == nil {
		return status(StatusWarn, "no script selected")
	}
	// m.recent is the loaded history, oldest first — the newest row for this
	// script that actually kept a log is the one to open
	for i := len(m.recent) - 1; i >= 0; i-- {
		row := m.recent[i]
		if row.Script == s.Name && row.LogFile != nil && *row.LogFile != "" {
			return tailLog(*row.LogFile, m.app.Cfg.MaxOutputLines)
		}
	}
	return status(StatusWarn, "no log for "+s.Name+" yet")
}

// tailLog reads the last n lines of a log file, off the update loop.
func tailLog(path string, n int) tea.Cmd {
	return func() tea.Msg {
		f, err := os.Open(path)
		if err != nil {
			return LogLoadedMsg{Path: path, Err: err}
		}
		defer f.Close()

		// A circular buffer of the last n lines. A run log is unbounded — a
		// chatty script can leave megabytes — and the pane will only ever show
		// n of them, so this keeps the cost one assignment per line rather
		// than shifting a slice down by one for every line past the cap.
		n = max(n, 1)
		ring := make([]string, 0, n)
		at := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			if len(ring) < n {
				ring = append(ring, sc.Text())
				continue
			}
			ring[at] = sc.Text()
			at = (at + 1) % n
		}
		if len(ring) == n && at > 0 {
			// unwrap: the oldest kept line is at `at`
			out := make([]string, 0, n)
			ring = append(append(out, ring[at:]...), ring[:at]...)
		}
		return LogLoadedMsg{Path: path, Lines: ring, Err: sc.Err()}
	}
}

func (r *runModel) onLogLoaded(m *Model, msg LogLoadedMsg) tea.Cmd {
	if msg.Err != nil {
		return status(StatusErr, "reading "+msg.Path+": "+msg.Err.Error())
	}
	r.out.begin("log: " + filepath.Base(msg.Path))
	r.out.append("", banner("▶ log: "+msg.Path+" · last "+strconv.Itoa(len(msg.Lines))+" lines",
		r.out.contentWidth()))
	r.out.append(msg.Lines...)
	r.out.toBottom()
	return nil
}

// openHistory is `h`: the History view, scoped to this script. The scope is
// read by the History view itself (phase 11 wave B) — it is kept on the root
// so a deep-link and the view's own `f` scope toggle share one field.
func (r *runModel) openHistory(m *Model) tea.Cmd {
	if s := r.selected(m); s != nil {
		m.historyScope = s.Name
	}
	return m.switchTo(modeHistory)
}

// ---------------------------------------------------------------------------

// kill is `x`: whichever of the two things that can be running is running.
func (r *runModel) kill(m *Model) tea.Cmd {
	if r.handle == nil && r.task != nil {
		return r.killTask()
	}
	h := r.handle
	if h == nil {
		return status(StatusWarn, "nothing is running")
	}
	// Handle.Kill blocks for up to the 3s kill grace (SIGTERM, wait, SIGKILL to
	// the group and to every sampled pid), which is exactly why it may not run
	// inside Update.
	return func() tea.Msg {
		h.Kill("killed")
		return StatusMsg{Text: "killed " + h.Name, Kind: StatusWarn}
	}
}
