package client

import (
	"context"
	"net"
	"testing"
	"time"

	"capnproto.org/go/capnp/v3/rpc"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCallbackServerLatchesServerDisconnectedBeforeSendRegistered is a
// regression test: Dial's connection-monitor goroutine can observe
// r.conn.Done() and call cb.dispatch(ServerDisconnectedMsg{}) before
// SetPushSender has wired up a sender (SetPushSender only runs once
// tea.NewProgram exists in main.go, strictly after Dial returns). dispatch
// used to just drop the message when send was nil, silently defeating the
// entire point of ServerDisconnectedMsg — the client would sit on a dead
// connection forever instead of quitting. setSend must now replay a latched
// ServerDisconnectedMsg the moment a sender becomes available.
func TestCallbackServerLatchesServerDisconnectedBeforeSendRegistered(t *testing.T) {
	cb := &callbackServer{}

	// Dispatched before any sender is registered — must not be lost.
	cb.dispatch(ServerDisconnectedMsg{})

	var got []tea.Msg
	cb.setSend(func(msg tea.Msg) { got = append(got, msg) })

	if len(got) != 1 {
		t.Fatalf("got %d messages after setSend, want 1 (the latched ServerDisconnectedMsg)", len(got))
	}
	if _, ok := got[0].(ServerDisconnectedMsg); !ok {
		t.Errorf("replayed message = %T, want ServerDisconnectedMsg", got[0])
	}

	// A second setSend call must not replay it again.
	cb.setSend(func(msg tea.Msg) { got = append(got, msg) })
	if len(got) != 1 {
		t.Errorf("got %d messages after second setSend, want still 1 (latch must not replay twice)", len(got))
	}
}

// TestCallbackServerDispatchesImmediatelyWhenSendAlreadySet confirms the
// ordinary path (send already registered) still delivers synchronously,
// with no latching involved.
func TestCallbackServerDispatchesImmediatelyWhenSendAlreadySet(t *testing.T) {
	cb := &callbackServer{}
	var got []tea.Msg
	cb.setSend(func(msg tea.Msg) { got = append(got, msg) })

	cb.dispatch(ServerDisconnectedMsg{})

	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
}

// TestHandleConnClosedSuppressesForIntentionalDisconnect and
// TestHandleConnClosedDispatchesForGenuineDisconnect are regression tests
// for the other half of the same bug: before RPC.disconnecting existed,
// Disconnect()'s own r.conn.Close() (the normal :q/:qa quit path) made the
// monitor goroutine fire ServerDisconnectedMsg exactly like a real server
// crash would, so app.App would report "connection to server lost" on every
// ordinary quit.
func TestHandleConnClosedSuppressesForIntentionalDisconnect(t *testing.T) {
	cb := &callbackServer{}
	r := &RPC{cb: cb}
	r.disconnecting.Store(true)

	r.handleConnClosed()

	var got []tea.Msg
	cb.setSend(func(msg tea.Msg) { got = append(got, msg) })
	if len(got) != 0 {
		t.Errorf("got %d messages, want 0: an intentional Disconnect must not latch/dispatch ServerDisconnectedMsg", len(got))
	}
}

func TestHandleConnClosedDispatchesForGenuineDisconnect(t *testing.T) {
	cb := &callbackServer{}
	r := &RPC{cb: cb}
	// r.disconnecting left false: this is an unexpected close, e.g. the
	// server process crashing.

	r.handleConnClosed()

	var got []tea.Msg
	cb.setSend(func(msg tea.Msg) { got = append(got, msg) })
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (latched ServerDisconnectedMsg replayed on setSend)", len(got))
	}
	if _, ok := got[0].(ServerDisconnectedMsg); !ok {
		t.Errorf("message = %T, want ServerDisconnectedMsg", got[0])
	}
}

// TestDisconnectSetsFlagBeforeClosingConn drives the real RPC.Disconnect
// path end-to-end (real *rpc.Conn over an in-memory pipe, no server on the
// other end needed since we only care that Disconnect sets r.disconnecting
// before/while closing conn — not that the Disconnect RPC call itself
// succeeds) and confirms handleConnClosed treats the resulting conn.Done()
// as expected, not as a crash.
func TestDisconnectSetsFlagBeforeClosingConn(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close() //nolint:errcheck

	transport := rpc.NewStreamTransport(c1)
	conn := rpc.NewConn(transport, &rpc.Options{})

	cb := &callbackServer{}
	r := &RPC{conn: conn, cb: cb}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.Disconnect(ctx) // r.svc is a null capability, so this call fails fast; we only care about the side effects below

	if !r.disconnecting.Load() {
		t.Fatal("r.disconnecting was not set by Disconnect")
	}

	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("conn.Done() did not fire after Disconnect closed the connection")
	}

	r.handleConnClosed()
	var got []tea.Msg
	cb.setSend(func(msg tea.Msg) { got = append(got, msg) })
	if len(got) != 0 {
		t.Errorf("got %d messages, want 0: Disconnect's own close must not be reported as a server disconnect", len(got))
	}
}
