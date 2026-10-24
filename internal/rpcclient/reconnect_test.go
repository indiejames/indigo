package rpcclient

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"
	tea "charm.land/bubbletea/v2"

	proto "github.com/indiejames/indigo/internal/proto"
)

// TestAReconnectThatNeverSucceedsGivesUp: a container that is really gone
// (Docker Desktop quit, the container deleted) must end the way a dropped
// connection always did, with the window told the server is gone, rather than
// retrying for ever behind a window that takes no input.
func TestAReconnectThatNeverSucceedsGivesUp(t *testing.T) {
	defer func(first, budget time.Duration) { reconnectFirstDelay, reconnectBudget = first, budget }(reconnectFirstDelay, reconnectBudget)
	reconnectFirstDelay, reconnectBudget = time.Millisecond, 50*time.Millisecond

	r := rpcOver(proto.EditorService{})
	var (
		mu       sync.Mutex
		got      []tea.Msg
		attempts int
	)
	r.cb.setSend(func(m tea.Msg) { mu.Lock(); got = append(got, m); mu.Unlock() })
	r.SetRedial(func(context.Context) (io.ReadWriteCloser, string, error) {
		mu.Lock()
		attempts++
		mu.Unlock()
		return nil, "", errors.New("no such container")
	})

	done := make(chan struct{})
	go func() { r.reconnect(r.redialer()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect is still retrying long after its budget")
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts < 2 {
		t.Errorf("%d attempt(s), want it to have retried", attempts)
	}
	if len(got) != 2 {
		t.Fatalf("window was sent %d message(s) %v, want reconnecting then disconnected", len(got), got)
	}
	if _, ok := got[0].(ServerReconnectingMsg); !ok {
		t.Errorf("first message %T, want ServerReconnectingMsg", got[0])
	}
	if _, ok := got[1].(ServerDisconnectedMsg); !ok {
		t.Errorf("last message %T, want ServerDisconnectedMsg", got[1])
	}
	if r.reconnecting.Load() {
		t.Error("still marked as reconnecting after giving up")
	}
}

// TestQuittingStopsAReconnect: ctrl+c during an outage quits, and the
// reconnect behind it must not go on to connect a window that has gone.
func TestQuittingStopsAReconnect(t *testing.T) {
	defer func(first time.Duration) { reconnectFirstDelay = first }(reconnectFirstDelay)
	reconnectFirstDelay = time.Millisecond

	r := rpcOver(proto.EditorService{})
	r.cb.setSend(func(tea.Msg) {})
	calls := make(chan struct{}, 100)
	r.SetRedial(func(context.Context) (io.ReadWriteCloser, string, error) {
		calls <- struct{}{}
		return nil, "", errors.New("not yet")
	})
	done := make(chan struct{})
	go func() { r.reconnect(r.redialer()); close(done) }()
	<-calls
	r.disconnecting.Store(true) // what Disconnect does first
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect kept going after the window quit")
	}
}

// TestAnEditFailingAsDisconnectedCountsAsReconnecting: a call can fail with
// "connection closed" before the connection's Done fires, so Alive is still
// true. A window that took that for an ordinary failed edit showed an error
// modal, which took the next key (an Esc ending insert mode) to dismiss.
func TestAnEditFailingAsDisconnectedCountsAsReconnecting(t *testing.T) {
	p1, p2 := net.Pipe()
	defer p2.Close() //nolint:errcheck
	conn := rpc.NewConn(rpc.NewStreamTransport(p1), nil)
	defer conn.Close() //nolint:errcheck
	r := rpcOver(proto.EditorService{})
	r.link.Store(&link{conn: conn, maxLocal: &r.maxLocal})
	if !r.Alive() {
		t.Fatal("fixture's connection is not alive")
	}
	closed := capnp.Disconnected("connection closed")

	if r.Reconnecting(closed) {
		t.Error("reconnecting with no redialer set")
	}
	r.SetRedial(func(context.Context) (io.ReadWriteCloser, string, error) { return nil, "", errors.New("unused") })
	if !r.Reconnecting(closed) {
		t.Error("a call that failed as disconnected does not count as reconnecting while the connection still looks alive")
	}
	if r.Reconnecting(errors.New("generation mismatch")) {
		t.Error("an ordinary failure on a live connection counts as reconnecting")
	}
}
