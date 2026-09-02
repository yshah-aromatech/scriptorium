// service.go is the systemd unit generator and --install-mcp-service
// install flow — the port of Get-StoMcpServiceUnit / Install-StoMcpService
// (src/Mcp.psm1), with one deliberate change (ruling 3): ExecStart runs
// THIS Go binary directly with --mcp, not pwsh -File scriptorium.ps1 --mcp
// — this IS the systemd-unit swap P7 deferred to P9.
//
// The Linux-only guard lives in the CLI layer (internal/cli), not here:
// Install itself never inspects runtime.GOOS, so its root/non-root
// file-and-command sequence stays fully testable on any host, including
// darwin CI, via the injected Run/IsRoot/HomeDir/Username hooks and the
// Root path prefix below.
package mcp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ServiceUnit is the port of Get-StoMcpServiceUnit — pure and
// byte-golden except for ExecStart (ruling 3). The two comment lines
// inside [Service] are copied verbatim from the PowerShell here-string:
// they are written into the real unit file, not source comments.
func ServiceUnit(appDir, execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=scriptorium MCP server
After=network.target

[Service]
ExecStart=%s --mcp
WorkingDirectory=%s
# system units without User= don't set HOME, and the app expands ~/.scriptorium
# with it — %%h is the service manager's home (/root for the system manager)
Environment=HOME=%%h
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, execPath, appDir)
}

// Installer performs --install-mcp-service's system-level work. Root is a
// path prefix applied to every /etc and ~/.config write ("" in production;
// a temp dir under test), and Run/IsRoot/HomeDir/Username are hooks that
// default to the real thing — together they let a test exercise BOTH the
// root and the non-root install path, byte-checking the unit file and the
// exact systemctl/loginctl call sequence, without ever touching a real
// systemd or a real filesystem root.
type Installer struct {
	Root     string
	IsRoot   func() bool
	HomeDir  func() (string, error)
	Username func() string
	Run      func(bin string, args ...string) error
	Out      func(string)
}

func (in *Installer) isRootFn() bool {
	if in.IsRoot != nil {
		return in.IsRoot()
	}
	return os.Geteuid() == 0
}

func (in *Installer) homeDirFn() (string, error) {
	if in.HomeDir != nil {
		return in.HomeDir()
	}
	return os.UserHomeDir()
}

func (in *Installer) usernameFn() string {
	if in.Username != nil {
		return in.Username()
	}
	return os.Getenv("USER")
}

func (in *Installer) run(bin string, args ...string) error {
	if in.Run != nil {
		return in.Run(bin, args...)
	}
	return exec.Command(bin, args...).Run()
}

func (in *Installer) println(s string) {
	if in.Out != nil {
		in.Out(s)
	}
}

func (in *Installer) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Install ports Install-StoMcpService: writes the unit (root path:
// /etc/systemd/system; non-root: ~/.config/systemd/user + linger),
// retiring the pre-rename psscripts-mcp unit first if present, then
// daemon-reload/enable/restart (never enable --now, so re-running the
// command after a config change actually applies it).
func (in *Installer) Install(appDir, execPath, token string) error {
	if token == "" {
		return errors.New("MCP_AUTH_TOKEN is not set — add it to .env next to the app first (the service would just crash-loop without it)")
	}
	unit := ServiceUnit(appDir, execPath)
	if in.isRootFn() {
		return in.installSystem(unit)
	}
	return in.installUser(unit)
}

func (in *Installer) installSystem(unit string) error {
	legacyUnit := filepath.Join(in.Root, "/etc/systemd/system/psscripts-mcp.service")
	if in.exists(legacyUnit) {
		_ = in.run("systemctl", "disable", "--now", "psscripts-mcp")
		_ = os.Remove(legacyUnit)
		in.println("removed pre-rename service: psscripts-mcp")
	}

	unitFile := filepath.Join(in.Root, "/etc/systemd/system/scriptorium-mcp.service")
	if err := os.MkdirAll(filepath.Dir(unitFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitFile, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := in.run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := in.run("systemctl", "enable", "scriptorium-mcp"); err != nil {
		return err
	}
	// restart, not enable --now, so re-running the command after e.g. a
	// mcpPort change actually applies it
	if err := in.run("systemctl", "restart", "scriptorium-mcp"); err != nil {
		return err
	}

	in.println("installed + started system service: " + unitFile)
	in.println("check:   systemctl status scriptorium-mcp")
	in.println("logs:    journalctl -u scriptorium-mcp -f")
	return nil
}

func (in *Installer) installUser(unit string) error {
	home, err := in.homeDirFn()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(in.Root, home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}

	legacyUnit := filepath.Join(unitDir, "psscripts-mcp.service")
	if in.exists(legacyUnit) {
		_ = in.run("systemctl", "--user", "disable", "--now", "psscripts-mcp")
		_ = os.Remove(legacyUnit)
		in.println("removed pre-rename user service: psscripts-mcp")
	}

	unitFile := filepath.Join(unitDir, "scriptorium-mcp.service")
	if err := os.WriteFile(unitFile, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := in.run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := in.run("systemctl", "--user", "enable", "scriptorium-mcp"); err != nil {
		return err
	}
	if err := in.run("systemctl", "--user", "restart", "scriptorium-mcp"); err != nil {
		return err
	}
	// keep the user manager (and the service) alive with no session open
	if err := in.run("loginctl", "enable-linger", in.usernameFn()); err != nil {
		return err
	}

	in.println("installed + started user service: " + unitFile)
	in.println("check:   systemctl --user status scriptorium-mcp")
	in.println("logs:    journalctl --user -u scriptorium-mcp -f")
	return nil
}
