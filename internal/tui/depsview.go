package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// depsOverlay is the missing-dependency prompt (inventory §1.5): y installs
// (and then runs, unless this scan was a plain `i` check), n skips the install,
// Esc cancels.
type depsOverlay struct {
	script      scripts.Script
	missing     []deps.Dep
	args        []string
	installOnly bool
}

func (d *depsOverlay) kind() overlayKind { return overlayDeps }

func (d *depsOverlay) title() string { return "missing dependencies · " + d.script.Name }

func (d *depsOverlay) height(*Model, int, int) int { return 2 }

func (d *depsOverlay) rows(m *Model, w, _ int) []string {
	th := m.th
	names := make([]string, len(d.missing))
	for i, dep := range d.missing {
		names[i] = dep.Display
	}
	verb, alt := "install & run", "run anyway"
	if d.installOnly {
		verb, alt = "install", "skip"
	}
	return []string{
		// the module list is elided rather than allowed to push the keys off
		// the row — the keys are the part you cannot guess (§1.12)
		th.S.Warning.Render("▲ ") + textkit.Truncate(th.S.Base.Render(strings.Join(names, ", ")), max(w-2, 8)),
		th.S.Success.Render("y") + th.S.Desc.Render(" "+verb+" · ") +
			th.S.Warning.Render("n") + th.S.Desc.Render(" "+alt+" · ") +
			th.S.Muted.Render("esc cancel"),
	}
}

func (d *depsOverlay) hints(m *Model) []key.Binding {
	return []key.Binding{m.keys.Accept, m.keys.Deny, m.keys.Close}
}

func (d *depsOverlay) key(m *Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return status(StatusInfo, "cancelled"), true
	case msg.String() == "y":
		return m.run.installDeps(m, d), true
	case key.Matches(msg, m.keys.Deny):
		if d.installOnly {
			return status(StatusInfo, "skipped the install"), true
		}
		return m.run.launch(m, d.script, d.args), true
	}
	return nil, false
}

// installDeps runs the generated install command as a streamed task and, when
// it is done, invalidates the scan cache before starting the run.
//
// The Invalidate is unconditional. The (size, mtime) cache key only watches the
// entry FILE, which an install never touches — a half-finished install has
// still changed the module dir, and serving the pre-install missing list after
// it would be wrong in both directions.
func (r *runModel) installDeps(m *Model, d *depsOverlay) tea.Cmd {
	s, args, only := d.script, d.args, d.installOnly
	cmd := deps.InstallCommand(deps.InstallTarget{
		Runtime: s.Runtime, Dir: s.Dir, ModuleDir: s.ModuleDir, VenvDir: s.VenvDir,
	}, d.missing, m.app.Cfg.PythonBin)

	return r.pwshTask(m, "install deps: "+s.Name, cmd, func(m *Model, ok bool) tea.Cmd {
		m.app.Scanner.Invalidate(s.Entry)
		if !ok {
			return status(StatusErr, "installing deps for "+s.Name+" failed — it was not started")
		}
		if only {
			return status(StatusOK, "installed the missing deps for "+s.Name)
		}
		// launch, not start: the dependency question has just been answered,
		// so asking it again would reopen this very overlay.
		return r.launch(m, s, args)
	})
}
