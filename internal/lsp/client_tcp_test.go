package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestNewTCPClientConnectsAndHandshakes is a regression test for the TCP
// transport GDScript/Godot support needs: Godot's language server is only
// reachable as a TCP listener inside a running editor, so NewTCPClient has
// to dial instead of spawning a process, then hand the resulting net.Conn
// into the same jsonrpcConn machinery NewClient already uses for
// stdout/stdin. This drives a real net.Listener test double through both
// NewTCPClient and Initialize() to confirm the whole path works end to end.
func TestNewTCPClientConnectsAndHandshakes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		fakeServer := newJSONRPCConn(conn, conn, nil, func(method string, params json.RawMessage) (any, error) {
			if method == "initialize" {
				gotParams <- params
			}
			return InitializeResult{}, nil
		})
		_ = fakeServer // background readLoop stops when conn is closed above
		<-time.After(2 * time.Second)
	}()

	c, err := NewTCPClient(ln.Addr().String(), "/workspace")
	if err != nil {
		t.Fatalf("NewTCPClient: %v", err)
	}
	defer c.terminate()

	if c.netConn == nil {
		t.Fatal("NewTCPClient did not set netConn")
	}
	if c.cmd != nil {
		t.Fatal("NewTCPClient should not set cmd — it doesn't spawn a process")
	}
	if !c.Alive() {
		t.Fatal("client should be alive immediately after connecting")
	}

	done := make(chan error, 1)
	go func() { done <- c.Initialize() }()

	select {
	case <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the initialize request to reach the fake server")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Initialize() returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Initialize() to return")
	}
}

// TestNewTCPClientRefusedConnectionReturnsError is a regression test: an
// unreachable address (the common case in practice — Godot's editor simply
// isn't running yet) must fail fast with an error, not hang, so
// Manager.startClient's caller isn't blocked and the failedStarts cooldown
// can kick in.
func TestNewTCPClientRefusedConnectionReturnsError(t *testing.T) {
	// Bind and immediately close to get a port nothing is listening on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() //nolint:errcheck

	start := time.Now()
	c, err := NewTCPClient(addr, "/workspace")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewTCPClient against a closed port should return an error")
	}
	if c != nil {
		t.Fatal("NewTCPClient should return a nil client on error")
	}
	if elapsed > tcpDialTimeout+2*time.Second {
		t.Fatalf("NewTCPClient took %v to fail against a refused connection — should fail fast", elapsed)
	}
}

// TestTCPClientShutdownDoesNotSendShutdownOrExit is a regression test:
// Shutdown's original implementation unconditionally sent the "shutdown"
// request + "exit" notification before killing the connection, which is
// correct for a spawned process indigo owns the lifecycle of, but wrong
// for a TCP-attached server like Godot's — indigo didn't launch it, and
// Godot's GDScript language server is shared by the whole running editor
// (and potentially other attached clients), so "exit" would tell that
// shared server to shut down instead of just ending indigo's own session
// with it. A TCP-backed Client must only close its own connection.
func TestTCPClientShutdownDoesNotSendShutdownOrExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	gotMethod := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		fakeServer := newJSONRPCConn(conn, conn, func(method string, _ json.RawMessage) {
			gotMethod <- method
		}, func(method string, _ json.RawMessage) (any, error) {
			gotMethod <- method
			return InitializeResult{}, nil
		})
		_ = fakeServer
		<-time.After(2 * time.Second)
	}()

	c, err := NewTCPClient(ln.Addr().String(), "/workspace")
	if err != nil {
		t.Fatalf("NewTCPClient: %v", err)
	}

	c.Shutdown()

	select {
	case method := <-gotMethod:
		t.Fatalf("Shutdown() sent %q to a TCP-attached server — it must only disconnect, never send shutdown/exit", method)
	case <-time.After(200 * time.Millisecond):
		// Expected: nothing was ever sent.
	}
}

// TestNewTCPClientTerminateClosesSocket is a regression test:
// Client.terminate's existing c.cmd != nil guard no-ops correctly for a
// TCP client (no cmd), but without an added branch it would never close
// netConn either, leaking the socket. Confirms the close is observable
// from the listener side.
func TestNewTCPClientTerminateClosesSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	c, err := NewTCPClient(ln.Addr().String(), "/workspace")
	if err != nil {
		t.Fatalf("NewTCPClient: %v", err)
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted the connection")
	}
	defer serverConn.Close() //nolint:errcheck

	c.terminate()

	// A read on the server side of the pipe should now observe EOF/closed
	// rather than blocking, since terminate() closed the client's end.
	buf := make([]byte, 1)
	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	if _, err := serverConn.Read(buf); err == nil {
		t.Fatal("expected the server-side read to observe the client closing the connection")
	}
}
