package rpcclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/debuglog"
	proto "github.com/indiejames/indigo/internal/proto"
	"github.com/indiejames/indigo/internal/rpcwatch"
)

// rpcLogger implements rpc.Logger, routing capnproto internal messages to our log file.
type rpcLogger struct{}

func (l *rpcLogger) Debug(msg string, args ...any) { clientLog("rpc debug: %s %v", msg, args) }
func (l *rpcLogger) Info(msg string, args ...any)  { clientLog("rpc info: %s %v", msg, args) }
func (l *rpcLogger) Warn(msg string, args ...any)  { clientLog("rpc warn: %s %v", msg, args) }
func (l *rpcLogger) Error(msg string, args ...any) {
	// "context canceled" release errors are normal teardown noise on disconnect.
	if strings.Contains(fmt.Sprint(args...), "context canceled") {
		return
	}
	clientLog("rpc error: %s %v", msg, args)
}

func clientLog(format string, args ...any) {
	debuglog.Write("client", format, args...)
}

// PluginKeyResult is the client-side view of a plugin key handler response.
type PluginKeyResult struct {
	Handled     bool
	CursorLine  uint32
	CursorCol   uint32
	HasCursor   bool
	CaptureKeys uint32 // >0 = client should capture this many more keypresses in "capture" mode
}

// ClientDecorationKind mirrors the server-side enum.
type ClientDecorationKind int

const (
	ClientDecorationGutter     ClientDecorationKind = 0
	ClientDecorationOverlay    ClientDecorationKind = 1
	ClientDecorationStatusBar  ClientDecorationKind = 2
	ClientDecorationUnderline  ClientDecorationKind = 3
	ClientDecorationLeftGutter ClientDecorationKind = 4
	// ClientDecorationRemovedLine becomes a virtual screen row above Line;
	// ClientDecorationLineTint a background range on Line. See
	// virtual_lines.go for how both are consumed.
	ClientDecorationRemovedLine ClientDecorationKind = 5
	ClientDecorationLineTint    ClientDecorationKind = 6
)

// ClientUnderlineStyle mirrors the server-side enum.
type ClientUnderlineStyle int

const (
	ClientUnderlineNone     ClientUnderlineStyle = 0
	ClientUnderlineStraight ClientUnderlineStyle = 1
	ClientUnderlineCurly    ClientUnderlineStyle = 2
)

// ClientFixItem is one item in the F-key popup, from either a decoration fix or an action provider.
type ClientFixItem struct {
	Label   string
	Replace string // non-empty = delete [FromLine:FromCol, ToLine:ToCol) then insert Replace

	// Range for direct replacement (used when Replace != "").
	FromLine, FromCol int
	ToLine, ToCol     int

	// Callback info (used when Replace == "" and LspEdits == nil).
	Plugin    string // plugin name
	FixData   string // opaque token for decoration-based fixes; "" for action provider items
	OrigIndex int    // original index within the plugin's fix/action list
	IsAction  bool   // true = call ApplyPluginAction; false = call ApplyPluginFix

	// LspEdits is set for LSP code actions; applied in reverse order to preserve offsets.
	LspEdits []ClientLspEdit
	// LspKind is the LSP CodeActionKind for LspEdits items (e.g.
	// "refactor.extract.function"), used to detect range-extract refactors
	// that introduce a new, not-yet-named symbol.
	LspKind string
}

// ClientDecoration is one decoration item returned by a plugin provider.
type ClientDecoration struct {
	Line uint32
	Col  uint32
	Text string
	Kind ClientDecorationKind

	// Underline fields (Kind == ClientDecorationUnderline)
	EndCol         uint32
	UnderlineStyle ClientUnderlineStyle
	UnderlineColor string

	// Fix fields
	Fixable    bool
	FixData    string
	PluginName string

	// TextColor is the hex foreground color for gutter/overlay text; empty = default.
	TextColor string

	// OldLine is only set for the removed-line kind: the 1-based line number
	// this content had before the change, for the gutter beside it.
	OldLine uint32
}

// RPC wraps a Cap'n Proto connection to the editor server.
type RPC struct {
	// link is the connection calls go out on, replaced when a dropped one is
	// reconnected. See link.go.
	link atomic.Pointer[link]
	// maxLocal is the highest buffer id the editor has been handed. See
	// link.maxLocal.
	maxLocal atomic.Uint32
	cb       *callbackServer

	// redial, when set, makes a dropped connection something to reconnect
	// rather than a reason to quit. See reconnect.go.
	redialMu sync.Mutex
	redial   Redialer
	// reconnects counts successful reconnects, and numbers them for
	// ServerReconnectedMsg. reconnecting is set while one is under way.
	reconnects   atomic.Uint64
	reconnecting atomic.Bool

	pluginKeysMu    sync.RWMutex
	insertHookChars map[string]bool
	menuItems       []ClientMenuItem

	// disconnecting is set at the start of Disconnect(), before it closes
	// conn. The connection-monitor goroutine checks it when conn.Done()
	// fires so a normal, user-initiated quit (:q, :qa, ...) isn't reported
	// as the server having crashed — only a close we didn't initiate is a
	// genuine disconnect worth telling app.App about.
	disconnecting atomic.Bool
}

// ClientMenuItem is one node in the Command-menu (space menu) tree contributed
// by a plugin manifest. A leaf node has a non-empty Command; a group node has
// Children and an empty Command.
type ClientMenuItem struct {
	Label      string
	Key        string
	PluginName string
	Command    string
	Children   []ClientMenuItem
}

// Dial connects to the server at socketPath and registers this client.
func Dial(socketPath string) (*RPC, error) {
	c, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	return DialStream(c)
}

// DialStream is Dial over an already-open stream, for a server this process did
// not reach through a socket.
//
// That is how a dev container is attached: the server runs inside the
// container and the connection arrives over the runtime's exec stdio, which is
// a pipe pair and not a socket. capnp never cared — rpc.NewStreamTransport
// takes an io.ReadWriteCloser — so everything below this point is identical for
// both, and the two cannot drift apart because there is only one of them.
//
// The caller owns rwc until this returns successfully; after that the RPC owns
// it and closing the connection closes the stream.
func DialStream(rwc io.ReadWriteCloser) (*RPC, error) {
	return DialStreamExpecting(rwc, "")
}

// DialStreamExpecting is DialStream for a window that knows which server build
// it started (a dev container's, from container.AttachOptions.ServerBuild).
// The server compares it with its own, so a window that joined a server left
// running from an older build is shown the stale-server prompt.
func DialStreamExpecting(rwc io.ReadWriteCloser, expectedBuild string) (*RPC, error) {
	r := &RPC{cb: &callbackServer{}, insertHookChars: make(map[string]bool)}
	r.cb.rpc = r
	l, err := r.handshake(context.Background(), rwc, expectedBuild, false)
	if err != nil {
		return nil, err
	}
	r.link.Store(l)
	clientLog("Dial: connected, clientID=%d", l.clientID)

	// Monitor connection lifecycle. l.conn.Done() fires both when we close
	// it ourselves (Disconnect, a normal quit) and when the server process
	// dies/crashes out from under us — the transport's read fails either way
	// and the capnp rpc layer closes Done() (see rpc.Conn.shutdown) — so
	// handleConnClosed is what tells the two apart.
	go r.watch(l)

	r.loadPluginInfo(l)
	return r, nil
}

// handshake opens a connection over rwc and registers this window with the
// server on it. mapped is false for the first connection and true for a
// reconnect, whose server numbers buffers its own way (see link).
//
// It closes the connection, and with it rwc, on failure.
func (r *RPC) handshake(ctx context.Context, rwc io.ReadWriteCloser, expectedBuild string, mapped bool) (*link, error) {
	cbCap := proto.ClientCallback_ServerToClient(r.cb)
	defer cbCap.Release()

	transport := rpc.NewStreamTransport(rwc)
	conn := rpc.NewConn(transport, &rpc.Options{
		BootstrapClient: capnp.Client(cbCap).AddRef(),
		Logger:          &rpcLogger{},
	})
	// Every outgoing call goes through rpcwatch, which records it as in flight
	// for as long as it is outstanding. Two things come of that: the handshake
	// calls below carry no deadline, so a server that never answers them would
	// otherwise leave a window that simply never opens with nothing written
	// down; and when this window's update loop freezes, the report carries
	// what this connection was waiting on at that moment, which is what
	// separates "stuck doing local work" from "stuck waiting on the server".
	//
	// Safe to wrap because this capability is only ever called from this
	// process and never handed back to the server — a wrapper has its own
	// identity, so a peer receiving one would export it afresh.
	svc := proto.EditorService(rpcwatch.WrapOutgoing("client", conn.Bootstrap(context.Background())))

	// Register with server, passing our callback capability.
	fut, rel := svc.Connect(ctx, func(p proto.EditorService_connect_Params) error {
		if expectedBuild != "" {
			if err := p.SetExpectedBuild(expectedBuild); err != nil {
				return err
			}
		}
		return p.SetCallback(proto.ClientCallback(capnp.Client(cbCap).AddRef()))
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("connect: %w", err)
	}
	l := &link{
		conn:        conn,
		svc:         svc,
		clientID:    res.ClientId(),
		serverStale: res.ServerStale(),
		mapped:      mapped,
		maxLocal:    &r.maxLocal,
	}
	if mapped {
		l.toRemote = make(map[uint32]uint32)
		l.toLocal = make(map[uint32]uint32)
	}
	return l, nil
}

// watch waits for l to close and reports it.
func (r *RPC) watch(l *link) {
	<-l.conn.Done()
	clientLog("server connection closed")
	r.handleConnClosed(l)
}

// loadPluginInfo fetches what plugins contribute to the window from the server
// on l. Run again after a reconnect, since a server started afresh may have
// different plugins.
func (r *RPC) loadPluginInfo(l *link) {
	// Fetch insert characters and menu items in one round trip's worth of
	// latency instead of two: issue both calls before blocking on either, so
	// the server processes them concurrently. The menu-items handler waits for
	// plugin startup to finish (see pluginReadyTimeout server-side).
	mfut, mrel := l.svc.GetMenuItems(context.Background(), func(_ proto.EditorService_getMenuItems_Params) error {
		return nil
	})
	ifut, irel := l.svc.GetPluginInsertChars(context.Background(), func(_ proto.EditorService_getPluginInsertChars_Params) error {
		return nil
	})

	// Plugin-contributed Command (space) menu items.
	var items []ClientMenuItem
	if mres, err := mfut.Struct(); err == nil {
		if rawList, err := mres.Items(); err == nil {
			items = decodeMenuItems(rawList)
		}
	}
	mrel()

	// Chars with a registered insert hook, for plugins that registered before
	// this client connected; insertHookRegistered covers later registration.
	chars := make(map[string]bool)
	if ires, err := ifut.Struct(); err == nil {
		if list, err := ires.Chars(); err == nil {
			for i := 0; i < list.Len(); i++ {
				if c, err := list.At(i); err == nil {
					chars[c] = true
				}
			}
		}
	}
	irel()

	// Hooks are merged, not replaced: one a plugin registers while this runs
	// arrives as a push (insertHookRegistered), and clearing the set here could
	// drop it. A hook left over from an earlier server costs a call that
	// nothing handles.
	r.pluginKeysMu.Lock()
	r.menuItems = items
	for c := range chars {
		r.insertHookChars[c] = true
	}
	r.pluginKeysMu.Unlock()
}

// decodeMenuItems recursively converts a capnp MenuItemInfo_List into
// []ClientMenuItem.
func decodeMenuItems(rawList proto.MenuItemInfo_List) []ClientMenuItem {
	out := make([]ClientMenuItem, rawList.Len())
	for i := range out {
		item := rawList.At(i)
		label, _ := item.Label()
		key, _ := item.Key()
		pluginName, _ := item.PluginName()
		command, _ := item.Command()
		var children []ClientMenuItem
		if rawChildren, err := item.Children(); err == nil {
			children = decodeMenuItems(rawChildren)
		}
		out[i] = ClientMenuItem{
			Label:      label,
			Key:        key,
			PluginName: pluginName,
			Command:    command,
			Children:   children,
		}
	}
	return out
}

// MenuItems returns the cached plugin-contributed Command-menu tree from startup.
func (r *RPC) MenuItems() []ClientMenuItem {
	r.pluginKeysMu.RLock()
	defer r.pluginKeysMu.RUnlock()
	return r.menuItems
}

func (r *RPC) ClientID() uint64 { return r.cur().clientID }

// SetPushSender wires a Bubble Tea send function so that server push
// notifications (plugin effects) are routed into the running program.
// Call this after tea.NewProgram is created, before p.Run().
func (r *RPC) SetPushSender(send func(tea.Msg)) {
	r.cb.setSend(send)
}

// addInsertHookChar records that a plugin owns this OnInsert char.
func (r *RPC) addInsertHookChar(char string) {
	r.pluginKeysMu.Lock()
	r.insertHookChars[char] = true
	r.pluginKeysMu.Unlock()
}

// handleConnClosed runs once l's connection closes, from the goroutine watch
// runs on. It suppresses ServerDisconnectedMsg when the close was caused by our
// own Disconnect() — a normal quit. Otherwise the server process died or was
// killed out from under us, or the stream to it broke: with a redialer set
// (a dev container) that starts a reconnect, and without one the window is
// told the server has gone.
func (r *RPC) handleConnClosed(l *link) {
	if r.disconnecting.Load() {
		clientLog("server connection closed: expected (local Disconnect)")
		return
	}
	if r.cur() != l {
		return // an earlier connection, already replaced
	}
	if redial := r.redialer(); redial != nil {
		r.reconnect(redial)
		return
	}
	r.cb.dispatch(ServerDisconnectedMsg{})
}

// Disconnect unregisters this client from the server.
func (r *RPC) Disconnect(ctx context.Context) error {
	// Set before doing anything else: conn.Done() firing as a result of the
	// conn.Close() below must see this already true, so the monitor
	// goroutine in Dial doesn't mistake this intentional close for the
	// server crashing.
	r.disconnecting.Store(true)
	l := r.cur()
	fut, rel := l.svc.Disconnect(ctx, func(p proto.EditorService_disconnect_Params) error {
		p.SetClientId(l.clientID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	l.svc.Release()
	l.conn.Close() //nolint:errcheck
	return err
}

// Alive reports whether the connection to the server is still open.
//
// A long-lived non-interactive client (the MCP server, which outlives any
// editor window) needs this: an indigo server exits when its last client
// disconnects, and a handle to a dead one keeps answering every call with
// "rpc: connection closed" forever. Checking before use is what lets such a
// client redial instead of failing for the rest of its life.
func (r *RPC) Alive() bool {
	if r == nil {
		return false
	}
	l := r.cur()
	if l == nil || l.conn == nil {
		return false
	}
	select {
	case <-l.conn.Done():
		return false
	default:
		return true
	}
}

// ServerStale reports whether the connected server is running code that has
// since been replaced on disk.
//
// Worth surfacing prominently: a stale server looks completely normal — it
// connects, answers, and quietly lacks whatever was just built — so the
// natural conclusion is that the new code is broken rather than absent.
func (r *RPC) ServerStale() bool {
	if r == nil {
		return false
	}
	l := r.cur()
	return l != nil && l.serverStale
}
