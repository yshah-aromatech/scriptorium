// Package subprocess provides cancellation for unsupervised POSIX tool commands.
package subprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// CommandContext stops the entire tool process group on cancellation. WaitDelay
// also bounds output-pipe draining if a descendant escaped the owned group.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	return cmd
}
