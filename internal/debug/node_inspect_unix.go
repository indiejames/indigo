//go:build unix

package debug

import "syscall"

// signalInspector asks Node process pid to open its inspector.
func signalInspector(pid int) error { return syscall.Kill(pid, syscall.SIGUSR1) }
