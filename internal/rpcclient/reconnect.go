package rpcclient

import (
	"context"
	"fmt"
	"io"
	"time"

	"capnproto.org/go/capnp/v3"

	proto "github.com/indiejames/indigo/internal/proto"
)

// Redialer opens a new stream to a server for the same workspace, and names the
// server build it expects there ("" for none; see DialStreamExpecting).
//
// A dev container window sets one, because the stream to its server is a
// `docker exec` session, and those die for reasons that have nothing to do with
// indigo: the Mac sleeping, Docker Desktop pausing or restarting its VM. The
// server inside is usually fine, or it exited because its last client went and
// will be started again by the redial. Either way quitting the window threw
// away a session that could have carried on.
type Redialer func(ctx context.Context) (io.ReadWriteCloser, string, error)

// ServerReconnectingMsg tells the window its connection dropped and a
// reconnect has started. Nothing it sends reaches a server until
// ServerReconnectedMsg, so it must stop taking edits.
type ServerReconnectingMsg struct{}

// ServerReconnectedMsg tells the window it is connected again. Every buffer it
// has open must then be reattached (RPC.Reattach) before it is used: the server
// may be a new one, and none of the window's buffers are open on it yet.
type ServerReconnectedMsg struct {
	// Seq numbers the reconnect, so results that belong to an earlier one can
	// be recognised and dropped.
	Seq uint64
	// Stale is the new server's serverStale, as at startup.
	Stale bool
}

// Reconnect timing. Vars so a test can shorten them.
var (
	reconnectFirstDelay = 500 * time.Millisecond
	reconnectMaxDelay   = 15 * time.Second
	// reconnectBudget is how long to keep trying before giving up and quitting.
	// Measured with the monotonic clock, which on macOS (mach_absolute_time)
	// does not run while the machine sleeps: a reconnect that starts as the
	// Mac falls asleep picks up where it was on waking, rather than finding
	// its budget spent overnight.
	reconnectBudget = 3 * time.Minute
	// redialTimeout bounds one attempt. Generous, because an attempt can
	// include bringing a stopped dev container back up.
	redialTimeout = 2 * time.Minute
)

// SetRedial makes a dropped connection something to reconnect rather than a
// reason to quit. Set it after SetPushSender: a reconnect reports itself to the
// window, and a drop before the window exists is reported the old way.
func (r *RPC) SetRedial(fn Redialer) {
	r.redialMu.Lock()
	r.redial = fn
	r.redialMu.Unlock()
}

func (r *RPC) redialer() Redialer {
	r.redialMu.Lock()
	defer r.redialMu.Unlock()
	return r.redial
}

// reconnect redials until it has a working connection, or gives up after
// reconnectBudget and reports the server gone, as a drop without a redialer
// does.
func (r *RPC) reconnect(redial Redialer) {
	clientLog("connection lost; reconnecting")
	r.reconnecting.Store(true)
	defer r.reconnecting.Store(false)
	r.cb.dispatch(ServerReconnectingMsg{})
	start := time.Now()
	delay := reconnectFirstDelay
	for attempt := 1; ; attempt++ {
		if r.disconnecting.Load() {
			return
		}
		l, err := r.redialOnce(redial)
		if err == nil {
			if r.disconnecting.Load() {
				l.conn.Close() //nolint:errcheck
				return
			}
			r.link.Store(l)
			seq := r.reconnects.Add(1)
			clientLog("reconnected after %d attempt(s) in %s, clientID=%d",
				attempt, time.Since(start).Round(time.Millisecond), l.clientID)
			r.loadPluginInfo(l)
			// Before watching l: if l drops at once, the window must hear about
			// this reconnect before the next one starts.
			r.cb.dispatch(ServerReconnectedMsg{Seq: seq, Stale: l.serverStale})
			go r.watch(l)
			return
		}
		clientLog("reconnect attempt %d failed: %v", attempt, err)
		if time.Since(start) >= reconnectBudget {
			clientLog("giving up reconnecting after %s", time.Since(start).Round(time.Second))
			r.cb.dispatch(ServerDisconnectedMsg{})
			return
		}
		time.Sleep(delay)
		delay = min(delay*2, reconnectMaxDelay)
	}
}

func (r *RPC) redialOnce(redial Redialer) (*link, error) {
	ctx, cancel := context.WithTimeout(context.Background(), redialTimeout)
	defer cancel()
	rwc, build, err := redial(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.handshake(ctx, rwc, build, true)
	if err != nil {
		rwc.Close() //nolint:errcheck
		return nil, err
	}
	return l, nil
}

// Reconnects reports how many times the connection has been re-established.
func (r *RPC) Reconnects() uint64 { return r.reconnects.Load() }

// Reconnecting reports whether the connection has dropped and is being, or is
// about to be, re-established, given err, the error a call just failed with. A
// buffer whose edit just failed asks, because the edit is typically one typed
// in the instant before the window heard of the drop: reconnecting reconciles
// it, and reporting it as a failed edit would only put an error in front of
// the user (and take their next key to dismiss it).
//
// err counts as well as the connection, because a call can fail with
// "connection closed" before conn.Done() fires, while Alive is still true.
func (r *RPC) Reconnecting(err error) bool {
	if r == nil || r.redialer() == nil {
		return false
	}
	return r.reconnecting.Load() || !r.Alive() || capnp.IsDisconnected(err)
}

// Reattached is a buffer as the server holds it after RPC.Reattach.
type Reattached struct {
	Content      string
	Version      uint64
	Generation   uint64
	FromRecovery bool
	// Shared is true when another client has the buffer open too, so its
	// content may include that client's edits.
	Shared bool
}

// Reattach opens path on the current connection's server as the editor's
// buffer bufID, which was open before the connection was replaced, so that
// calls naming bufID reach it there.
//
// Opening by path rather than by id is the point: a server started afresh has
// never heard of bufID, and one that survived (another window kept it running)
// gave the buffer to this window's previous client id, which it dropped with
// the connection.
func (r *RPC) Reattach(ctx context.Context, bufID uint32, path string) (Reattached, error) {
	l := r.cur()
	fut, rel := l.svc.OpenFile(ctx, func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(l.clientID)
		return p.SetPath(path)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return Reattached{}, err
	}
	content, err := res.Content()
	if err != nil {
		return Reattached{}, err
	}
	remote := res.BufferId()
	l.bind(bufID, remote)
	out := Reattached{
		Content:      content,
		Version:      res.Version(),
		Generation:   res.Generation(),
		FromRecovery: res.FromRecovery(),
	}

	cfut, crel := l.svc.BufferClientCount(ctx, func(p proto.EditorService_bufferClientCount_Params) error {
		p.SetBufferId(remote)
		return nil
	})
	defer crel()
	cres, err := cfut.Struct()
	if err != nil {
		return Reattached{}, fmt.Errorf("count clients: %w", err)
	}
	out.Shared = cres.Count() > 1
	return out, nil
}
