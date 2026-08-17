package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"

	tea "github.com/charmbracelet/bubbletea"

	proto "github.com/indiejames/indigo/internal/proto"
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
	path := filepath.Join(os.TempDir(), "indigo-plugins.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()                                  //nolint:errcheck
	fmt.Fprintf(f, "[client] "+format+"\n", args...) //nolint:errcheck
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
}

// RPC wraps a Cap'n Proto connection to the editor server.
type RPC struct {
	conn     *rpc.Conn
	svc      proto.EditorService
	clientID uint64
	cb       *callbackServer

	pluginKeysMu    sync.RWMutex
	pluginKeys      map[string]bool
	insertHookChars map[string]bool
	pluginBindings  []ClientPluginBinding
	menuItems       []ClientMenuItem

	// disconnecting is set at the start of Disconnect(), before it closes
	// conn. The connection-monitor goroutine checks it when conn.Done()
	// fires so a normal, user-initiated quit (:q, :qa, ...) isn't reported
	// as the server having crashed — only a close we didn't initiate is a
	// genuine disconnect worth telling app.App about.
	disconnecting atomic.Bool
}

// ClientPluginBinding is a key binding contributed by a plugin, for the help popup.
type ClientPluginBinding struct {
	PluginName  string
	Key         string
	Description string
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

	cb := &callbackServer{}
	cbCap := proto.ClientCallback_ServerToClient(cb)
	defer cbCap.Release()

	transport := rpc.NewStreamTransport(c)
	conn := rpc.NewConn(transport, &rpc.Options{
		BootstrapClient: capnp.Client(cbCap).AddRef(),
		Logger:          &rpcLogger{},
	})
	svc := proto.EditorService(conn.Bootstrap(context.Background()))

	// Register with server, passing our callback capability.
	fut, rel := svc.Connect(context.Background(), func(p proto.EditorService_connect_Params) error {
		return p.SetCallback(proto.ClientCallback(capnp.Client(cbCap).AddRef()))
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("connect: %w", err)
	}

	r := &RPC{
		conn:            conn,
		svc:             svc,
		clientID:        res.ClientId(),
		cb:              cb,
		pluginKeys:      make(map[string]bool),
		insertHookChars: make(map[string]bool),
	}
	cb.mu.Lock()
	cb.rpc = r
	cb.mu.Unlock()
	clientLog("Dial: connected, clientID=%d", r.clientID)

	// Monitor connection lifecycle. r.conn.Done() fires both when we close
	// it ourselves (Disconnect, a normal quit) and when the server process
	// dies/crashes out from under us — the transport's read fails either way
	// and the capnp rpc layer closes Done() (see rpc.Conn.shutdown) — so
	// handleConnClosed is what tells the two apart.
	go func() {
		<-r.conn.Done()
		clientLog("server connection closed")
		r.handleConnClosed()
	}()

	// Fetch plugin keys, insert characters, bindings, and menu items in one
	// round trip's worth of latency instead of four: issue all four calls
	// before blocking on any of their results, so the server processes them
	// concurrently. The bindings/menu-items handlers each wait for plugin
	// startup to finish (see pluginReadyTimeout server-side); firing them
	// together means that wait is paid once, not stacked four times.
	kfut, krel := svc.GetPluginKeys(context.Background(), func(_ proto.EditorService_getPluginKeys_Params) error {
		return nil
	})
	bfut, brel := svc.GetPluginBindings(context.Background(), func(_ proto.EditorService_getPluginBindings_Params) error {
		return nil
	})
	mfut, mrel := svc.GetMenuItems(context.Background(), func(_ proto.EditorService_getMenuItems_Params) error {
		return nil
	})
	ifut, irel := svc.GetPluginInsertChars(context.Background(), func(_ proto.EditorService_getPluginInsertChars_Params) error {
		return nil
	})

	// Keys that plugins registered before this client connected. This handles
	// the race where the plugin starts and registers keys before the first
	// client calls Connect.
	if kres, err := kfut.Struct(); err != nil {
		clientLog("GetPluginKeys RPC error: %v", err)
	} else if keys, err := kres.Keys(); err != nil {
		clientLog("GetPluginKeys Keys() error: %v", err)
	} else {
		clientLog("GetPluginKeys returned %d keys", keys.Len())
		for i := 0; i < keys.Len(); i++ {
			if k, err := keys.At(i); err == nil {
				clientLog("GetPluginKeys: adding key %q", k)
				r.addPluginKey(k)
			}
		}
	}
	krel()

	// Plugin bindings for the help popup.
	if bres, err := bfut.Struct(); err == nil {
		if rawList, err := bres.Bindings(); err == nil {
			r.pluginBindings = make([]ClientPluginBinding, rawList.Len())
			for i := range r.pluginBindings {
				item := rawList.At(i)
				name, _ := item.PluginName()
				key, _ := item.Key()
				desc, _ := item.Description()
				r.pluginBindings[i] = ClientPluginBinding{PluginName: name, Key: key, Description: desc}
			}
		}
	}
	brel()

	// Plugin-contributed Command (space) menu items.
	if mres, err := mfut.Struct(); err == nil {
		if rawList, err := mres.Items(); err == nil {
			r.menuItems = decodeMenuItems(rawList)
		}
	}
	mrel()

	// Chars with a registered insert hook. Same race-handling rationale as
	// GetPluginKeys above; insertHookRegistered covers late registration.
	if ires, err := ifut.Struct(); err == nil {
		if chars, err := ires.Chars(); err == nil {
			for i := 0; i < chars.Len(); i++ {
				if c, err := chars.At(i); err == nil {
					r.addInsertHookChar(c)
				}
			}
		}
	}
	irel()

	return r, nil
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

func (r *RPC) ClientID() uint64 { return r.clientID }

// SetPushSender wires a Bubble Tea send function so that server push
// notifications (plugin effects) are routed into the running program.
// Call this after tea.NewProgram is created, before p.Run().
func (r *RPC) SetPushSender(send func(tea.Msg)) {
	r.cb.setSend(send)
}

// addPluginKey records that a plugin owns this key trigger.
func (r *RPC) addPluginKey(trigger string) {
	clientLog("addPluginKey: %q", trigger)
	r.pluginKeysMu.Lock()
	r.pluginKeys[trigger] = true
	r.pluginKeysMu.Unlock()
}

// addInsertHookChar records that a plugin owns this OnInsert char.
func (r *RPC) addInsertHookChar(char string) {
	r.pluginKeysMu.Lock()
	r.insertHookChars[char] = true
	r.pluginKeysMu.Unlock()
}

// PluginBindings returns the cached key bindings snapshot from startup.
func (r *RPC) PluginBindings() []ClientPluginBinding {
	r.pluginKeysMu.RLock()
	defer r.pluginKeysMu.RUnlock()
	return r.pluginBindings
}

// handleConnClosed runs once r.conn.Done() fires, from the monitor goroutine
// started in Dial. It suppresses ServerDisconnectedMsg when the close was
// caused by our own Disconnect() — a normal quit — and dispatches it
// otherwise (the server process died or was killed out from under us).
func (r *RPC) handleConnClosed() {
	if r.disconnecting.Load() {
		clientLog("server connection closed: expected (local Disconnect)")
		return
	}
	r.cb.dispatch(ServerDisconnectedMsg{})
}

// Disconnect unregisters this client from the server.
func (r *RPC) Disconnect(ctx context.Context) error {
	// Set before doing anything else: conn.Done() firing as a result of the
	// r.conn.Close() below must see this already true, so the monitor
	// goroutine in Dial doesn't mistake this intentional close for the
	// server crashing.
	r.disconnecting.Store(true)
	fut, rel := r.svc.Disconnect(ctx, func(p proto.EditorService_disconnect_Params) error {
		p.SetClientId(r.clientID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	r.svc.Release()
	r.conn.Close() //nolint:errcheck
	return err
}
