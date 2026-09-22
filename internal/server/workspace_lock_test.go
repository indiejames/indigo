package server

import (
	"errors"
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

	start := time.Now()
	second, err := New(dir)
	if err != nil {
		t.Fatalf("New after previous server exited: %v", err)
	}
	t.Cleanup(func() { second.triggerShutdown(); second.Wait() })
	if d := time.Since(start); d > lockWaitTimeout/2 {
		t.Errorf("New took %v after a clean shutdown; the lock was not released promptly", d)
	}
	if !IsRunning(SocketPath(dir)) {
		t.Fatal("new server's socket does not answer")
	}
}
