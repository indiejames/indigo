package server

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSecondServerForWorkspaceIsRefused is the regression for two servers
// running on one workspace: New used to remove whatever was at the socket path
// and listen, so a second New for the same directory succeeded and took the
// socket over from the first, leaving any client already connected to the first
// editing a private copy of every buffer.
func TestSecondServerForWorkspaceIsRefused(t *testing.T) {
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.triggerShutdown(); first.Wait() })

	second, err := New(dir)
	if err == nil {
		second.triggerShutdown()
		second.Wait()
		t.Fatal("second New for the same workspace succeeded; want ErrAlreadyRunning")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second New: %v, want ErrAlreadyRunning", err)
	}
	if !IsRunning(SocketPath(dir)) {
		t.Fatal("first server's socket no longer answers after the refused second New")
	}
}

// TestServerCanStartAfterPreviousExits checks the lock does not outlive its
// server: once one has shut down, the next New for the workspace must succeed,
// and the old server's teardown must not have removed the new one's socket.
func TestServerCanStartAfterPreviousExits(t *testing.T) {
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.triggerShutdown()
	first.Wait()

	second, err := New(dir)
	if err != nil {
		t.Fatalf("New after previous server exited: %v", err)
	}
	t.Cleanup(func() { second.triggerShutdown(); second.Wait() })
	if !IsRunning(SocketPath(dir)) {
		t.Fatal("new server's socket does not answer")
	}
}

// TestLockHeldByUnresponsiveServerIsNotAlreadyRunning: a lock held with nothing
// answering on the socket (a predecessor wedged in shutdown) must not be
// reported as ErrAlreadyRunning, which both entry points treat as success and
// exit quietly on — the new server would silently not start.
func TestLockHeldByUnresponsiveServerIsNotAlreadyRunning(t *testing.T) {
	old := lockWaitTimeout
	lockWaitTimeout = 100 * time.Millisecond
	t.Cleanup(func() { lockWaitTimeout = old })

	sockPath := filepath.Join(t.TempDir(), "s.sock") // nothing listens here
	holder, err := os.OpenFile(sockPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close() //nolint:errcheck
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	f, err := acquireWorkspaceLock(sockPath)
	if err == nil {
		f.Close() //nolint:errcheck
		t.Fatal("acquired a lock another open file description holds")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got ErrAlreadyRunning for an unanswered socket: %v", err)
	}
	if !errors.Is(err, errLockTimeout) {
		t.Fatalf("got %v, want errLockTimeout", err)
	}
}
