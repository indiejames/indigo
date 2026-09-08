package agenttools

import "syscall"

// detachedSysProcAttr puts a spawned indigo server in its own process group so
// it survives this process exiting, and so a signal sent to our group (the
// agent session being interrupted) doesn't take the server down with it.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
