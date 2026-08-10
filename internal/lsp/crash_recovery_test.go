package lsp

import (
	"net"
	"os/exec"
	"testing"
	"time"
)

// TestClientAliveReflectsConnectionClose is a regression test for the
// underlying mechanism a crashed language server needs: previously a
// Client had no way to know its own connection had died, so Alive() (used
// by Manager.clientForPath below) didn't exist and every call against a
// dead Client just hung until its own timeout instead of failing fast.
func TestClientAliveReflectsConnectionClose(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer serverEnd.Close() //nolint:errcheck

	c := &Client{}
	done := make(chan struct{})
	c.conn = newJSONRPCConnWithClose(clientEnd, clientEnd, nil, nil, func() {
		c.closed.Store(true)
		close(done)
	})

	if !c.Alive() {
		t.Fatal("client should be alive before the connection closes")
	}

	clientEnd.Close() //nolint:errcheck // simulates the server process dying

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onClose was never invoked after the connection closed")
	}

	if c.Alive() {
		t.Error("client should report not-alive once its connection has closed")
	}
}

// TestClientForPathRespawnsAfterCrash is a regression test: once a Client's
// process crashed after a previously successful start, Manager.clients kept
// the dead entry forever — clientForPath returned it unconditionally, so
// every future request for that language just errored/hung with no
// recovery. clientForPath must now notice Alive()==false, drop the stale
// entry, and attempt a fresh start instead.
func TestClientForPathRespawnsAfterCrash(t *testing.T) {
	dir := t.TempDir()
	langID := languageIDForExt("zzz")

	// A real, still-running child process stands in for the crashed
	// language server, so the cleanup path (Client.terminate reaping the
	// process via Kill+Wait) is exercised against an actual *exec.Cmd
	// rather than a nil one.
	cmd := exec.Command("sleep", "100")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start test child process: %v", err)
	}

	dead := &Client{cmd: cmd}
	closedCh := make(chan struct{})
	dead.conn = newJSONRPCConnWithClose(stdout, stdin, nil, nil, func() {
		dead.closed.Store(true)
		close(closedCh)
	})

	// Simulate a crash: sever the read side without killing the process
	// itself, so Client.terminate's Kill() call — not an already-dead
	// process's Wait() — is what's actually under test below.
	stdout.Close() //nolint:errcheck
	select {
	case <-closedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("onClose never fired after severing the connection")
	}
	if dead.Alive() {
		t.Fatal("test setup: dead client should report not-alive")
	}

	// A command that will fail to spawn, so the respawn attempt below fails
	// fast (via recordFailedStart) rather than needing a real, working fake
	// language server process — what's under test is that a respawn is
	// *attempted* at all, not that it succeeds.
	servers := []ServerConfig{{Extensions: []string{"zzz"}, Command: "/nonexistent/indigo-test-lsp-binary"}}
	m := NewManager(dir, servers)
	m.clients[langID] = dead

	got := m.clientForPath("/tmp/test.zzz")
	if got == dead {
		t.Fatal("clientForPath returned the dead client instead of dropping it and respawning")
	}
	if got != nil {
		t.Fatalf("clientForPath = %v, want nil (the respawn attempt should fail against a nonexistent binary)", got)
	}

	m.mu.Lock()
	_, stillCached := m.clients[langID]
	_, respawnAttempted := m.failedStarts[langID]
	m.mu.Unlock()
	if stillCached {
		t.Error("dead client entry should have been removed from m.clients")
	}
	if !respawnAttempted {
		t.Error("clientForPath should have attempted (and recorded a failed) respawn — dead-client detection never reached startClient")
	}

	if cmd.ProcessState == nil {
		t.Error("dead client's process was never reaped (Client.terminate's Kill+Wait never ran)")
	}
}
