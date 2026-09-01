package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/yshah-aromatech/scriptorium/internal/envfile"
)

// buildCmd assembles the child process: one function and a switch, not a
// runtime interface — there are exactly two runtimes and there will be two.
// Port of Start-StoRun's ProcessStartInfo construction.
//
// Every environment value the run adds is registered as a FORCED secret
// before the process starts: the per-script .env holds exactly the values the
// user chose to keep out of git, and per-run env (MCP run_script) may be
// credentials. Registering before start is what makes the very first output
// line redactable.
func (r *Runner) buildCmd(spec Spec) *exec.Cmd {
	sc := spec.Script
	env := os.Environ()
	add := func(k, v string) {
		r.Sec.Add(k, v, true)
		env = append(env, k+"="+v)
	}
	// a missing or unreadable .env is an empty one, never a failed run
	vals, _ := envfile.Read(sc.EnvFile)
	for k, v := range vals {
		add(k, v)
	}
	// per-run env overrides .env — os/exec keeps the LAST of duplicate keys,
	// so append order is the precedence
	for k, v := range spec.ExtraEnv {
		add(k, v)
	}

	args := make([]string, 0, len(sc.Args)+len(spec.ExtraArgs))
	for _, a := range append(append([]string{}, sc.Args...), spec.ExtraArgs...) {
		if a != "" { // PS drops empty arguments
			args = append(args, a)
		}
	}

	var cmd *exec.Cmd
	if sc.Runtime == "python" {
		venvPy := filepath.Join(sc.VenvDir, "bin", "python")
		if _, err := os.Stat(venvPy); err != nil {
			ensureVenv(r.Cfg.PythonBin, sc.VenvDir, venvPy)
		}
		cmd = exec.Command(venvPy, append([]string{sc.Entry}, args...)...)
		// line streaming depends on unbuffered python output
		env = append(env, "PYTHONUNBUFFERED=1")
	} else {
		cmd = exec.Command(r.Cfg.PwshBin,
			append([]string{"-NoProfile", "-NonInteractive", "-File", sc.Entry}, args...)...)
		// the per-script module dir gets first crack at module resolution.
		// Set last so it wins over any PSModulePath the .env supplied,
		// matching the PS app's ordering.
		env = append(env, "PSModulePath="+sc.ModuleDir+string(os.PathListSeparator)+os.Getenv("PSModulePath"))
	}

	cmd.Dir = sc.Dir
	cmd.Env = env
	// own process group: the whole tree can then be signalled at once
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// ensureVenv creates the script's virtualenv if it is missing. The dep
// install flow normally creates it, but a script with no third-party imports
// never goes through that. Both steps are best-effort with their output
// discarded, exactly like the PS app: a failed pip upgrade must not stop a
// run whose interpreter already exists.
func ensureVenv(pythonBin, venvDir, venvPy string) {
	_ = exec.Command(pythonBin, "-m", "venv", venvDir).Run()
	_ = exec.Command(venvPy, "-m", "pip", "install", "--upgrade", "pip", "--quiet").Run()
}
