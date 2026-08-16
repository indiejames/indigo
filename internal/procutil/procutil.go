// Package procutil provides process-group helpers for killing an external
// command's entire process tree, not just its direct child.
package procutil

import (
	"os/exec"
	"syscall"
)

// SetPgid configures cmd to become its own process group leader once
// started, so KillGroup can later signal its whole process tree (including
// any children it forks itself, e.g. eslint spawning worker processes) at
// once instead of leaving them orphaned when only the direct child is
// killed. Must be called before cmd.Start()/cmd.Run().
func SetPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillGroup sends SIGKILL to cmd's entire process group. cmd must have been
// started with SetPgid applied beforehand, so its process group ID equals
// its own PID. Falls back to killing just the direct process if the group
// signal fails (e.g. SetPgid was never applied, or the group is already
// gone).
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
