//go:build unix

package debug

import "syscall"

// signalInspector asks Node process pid to open its inspector.
func signalInspector(pid int) error { return syscall.Kill(pid, syscall.SIGUSR1) }

// continueProcess sends SIGCONT, resuming a stopped process.
func continueProcess(pid int) error { return syscall.Kill(pid, syscall.SIGCONT) }
