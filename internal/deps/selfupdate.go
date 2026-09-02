package deps

// The self-update and system-package commands: two more strings this package
// already owns for tool tasks, shared verbatim between the MCP update_app/
// update_packages ops and the TUI's U/u keys — a single source instead of two
// hand-typed copies that could drift.

// GitPullFFOnlyArgs is the self-update command (Invoke-TuiSelfUpdate,
// MCP's update_app op): git -C <appDir> pull --ff-only.
func GitPullFFOnlyArgs(appDir string) []string {
	return []string{"-C", appDir, "pull", "--ff-only"}
}

// AptUpgradeScript is the apt-get half of the system update — Tui.psm1:882-899
// verbatim: upgrade the installed PowerShell and Python packages, passwordless
// sudo only. The probe deciding whether it is safe to run at all (`sudo -n
// true`) belongs to each caller, not here — it has nothing to do with the
// command itself.
const AptUpgradeScript = "sudo -n apt-get update && sudo -n apt-get install -y --only-upgrade powershell python3 python3-pip python3-venv"

// AptSkipNote is what a caller reports when the probe says apt cannot run
// (MCP's update_packages op, and now the TUI's u).
const AptSkipNote = "apt stage skipped: passwordless sudo unavailable — run manually: sudo apt-get update && sudo apt-get install -y --only-upgrade powershell python3 python3-pip python3-venv"
