package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestClientForPathDialsTCPWhenAddressSet is a regression test:
// Manager.startClient must branch on ServerConfig.Address (Godot's
// GDScript server, reachable only over TCP, is the motivating case) and
// call NewTCPClient instead of spawning a process via NewClient.
func TestClientForPathDialsTCPWhenAddressSet(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		fakeServer := newJSONRPCConn(conn, conn, nil, func(method string, params json.RawMessage) (any, error) {
			return InitializeResult{}, nil
		})
		_ = fakeServer
		<-time.After(2 * time.Second)
	}()

	dir := t.TempDir()
	servers := []ServerConfig{{Extensions: []string{"gd"}, Address: ln.Addr().String()}}
	m := NewManager(dir, servers)

	c := m.clientForPath("/tmp/test.gd")
	if c == nil {
		t.Fatal("clientForPath returned nil for a reachable TCP server")
	}
	if c.netConn == nil {
		t.Fatal("clientForPath's returned client should be TCP-backed (netConn set)")
	}
	if c.cmd != nil {
		t.Fatal("clientForPath's returned client should not have spawned a process")
	}
}

// TestClientForPathTCPConnectFailureUsesCooldown is a regression test: a
// TCP connect failure (Godot's editor simply isn't running — the common
// case) must feed recordFailedStart exactly like a spawn failure does, so
// a closed port doesn't get redialed on every keystroke.
func TestClientForPathTCPConnectFailureUsesCooldown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() //nolint:errcheck // nothing listens at addr from here on

	dir := t.TempDir()
	servers := []ServerConfig{{Extensions: []string{"gd"}, Address: addr}}
	m := NewManager(dir, servers)

	if c := m.clientForPath("/tmp/test.gd"); c != nil {
		t.Fatal("clientForPath returned a client for an unreachable TCP address")
	}

	langID := languageIDForExt("gd")
	m.mu.Lock()
	last, failed := m.failedStarts[langID]
	m.mu.Unlock()
	if !failed {
		t.Fatal("failedStarts has no entry after a failed TCP connect")
	}
	if time.Since(last) > startRetryCooldown {
		t.Fatal("recorded failure timestamp is already outside the cooldown window")
	}

	// A second call within the cooldown must not attempt a fresh dial —
	// clientForPath should short-circuit on the cooldown check and return
	// nil immediately rather than trying (and failing) again.
	if c := m.clientForPath("/tmp/test.gd"); c != nil {
		t.Fatal("clientForPath returned a client on the second (should-be-cooled-down) attempt")
	}
}
