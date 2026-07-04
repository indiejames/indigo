package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// rpcLogger implements rpc.Logger, routing capnproto internal messages to our log file.
type rpcLogger struct{}

func (l *rpcLogger) Debug(msg string, args ...any) { clientLog("rpc debug: %s %v", msg, args) }
func (l *rpcLogger) Info(msg string, args ...any)  { clientLog("rpc info: %s %v", msg, args) }
func (l *rpcLogger) Warn(msg string, args ...any)  { clientLog("rpc warn: %s %v", msg, args) }
func (l *rpcLogger) Error(msg string, args ...any) { clientLog("rpc error: %s %v", msg, args) }

func clientLog(format string, args ...any) {
	path := filepath.Join(os.TempDir(), "indigo-plugins.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
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
	ClientDecorationGutter    ClientDecorationKind = 0
	ClientDecorationOverlay   ClientDecorationKind = 1
	ClientDecorationStatusBar ClientDecorationKind = 2
)

// ClientDecoration is one decoration item returned by a plugin provider.
type ClientDecoration struct {
	Line uint32
	Col  uint32
	Text string
	Kind ClientDecorationKind
}

// RPC wraps a Cap'n Proto connection to the editor server.
type RPC struct {
	conn     *rpc.Conn
	svc      proto.EditorService
	clientID uint64
	cb       *callbackServer

	pluginKeysMu sync.RWMutex
	pluginKeys   map[string]bool
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
		conn:       conn,
		svc:        svc,
		clientID:   res.ClientId(),
		cb:         cb,
		pluginKeys: make(map[string]bool),
	}
	cb.rpc = r
	clientLog("Dial: connected, clientID=%d", r.clientID)

	// Monitor connection lifecycle.
	go func() {
		<-r.conn.Done()
		buf := make([]byte, 64*1024)
		n := runtime.Stack(buf, true)
		clientLog("client conn.Done() fired! goroutines:\n%s", buf[:n])
	}()

	// Fetch keys that plugins registered before this client connected.
	// This handles the race where the plugin starts and registers keys before
	// the first client calls Connect.
	kfut, krel := svc.GetPluginKeys(context.Background(), func(_ proto.EditorService_getPluginKeys_Params) error {
		return nil
	})
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

	return r, nil
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

// HasPluginKey reports whether any plugin registered this key trigger.
func (r *RPC) HasPluginKey(key string) bool {
	r.pluginKeysMu.RLock()
	defer r.pluginKeysMu.RUnlock()
	return r.pluginKeys[key]
}

// HandlePluginKey asks the server to dispatch a keypress to the owning plugin.
func (r *RPC) HandlePluginKey(ctx context.Context, key, mode string, bufID uint32) (PluginKeyResult, error) {
	select {
	case <-r.conn.Done():
		clientLog("HandlePluginKey: conn already done!")
	default:
		clientLog("HandlePluginKey: conn is alive, key=%q", key)
	}
	fut, rel := r.svc.HandlePluginKey(ctx, func(p proto.EditorService_handlePluginKey_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufId(bufID)
		if err := p.SetKey(key); err != nil {
			return err
		}
		return p.SetMode(mode)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return PluginKeyResult{}, err
	}
	result, err := res.Result()
	if err != nil {
		return PluginKeyResult{}, err
	}
	return PluginKeyResult{
		Handled:     result.Handled(),
		CursorLine:  result.CursorLine(),
		CursorCol:   result.CursorCol(),
		HasCursor:   result.HasCursor(),
		CaptureKeys: result.CaptureKeys(),
	}, nil
}

// UpdateViewport informs the server of this client's current scroll position
// and visible height so plugins can use visibleRange accurately.
func (r *RPC) UpdateViewport(ctx context.Context, topLine, height uint32) {
	fut, rel := r.svc.UpdateViewport(ctx, func(p proto.EditorService_updateViewport_Params) error {
		p.SetClientId(r.clientID)
		p.SetTopLine(topLine)
		p.SetHeight(height)
		return nil
	})
	defer rel()
	fut.Struct() //nolint:errcheck
}

// GetDecorations fetches plugin decorations for the current client viewport.
func (r *RPC) GetDecorations(ctx context.Context, bufID uint32) ([]ClientDecoration, error) {
	fut, rel := r.svc.GetPluginDecorations(ctx, func(p proto.EditorService_getPluginDecorations_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	rawList, err := res.Decorations()
	if err != nil {
		return nil, err
	}
	out := make([]ClientDecoration, rawList.Len())
	for i := range out {
		item := rawList.At(i)
		text, err := item.Text()
		if err != nil {
			return nil, err
		}
		out[i] = ClientDecoration{
			Line: item.Line(),
			Col:  item.Col(),
			Text: text,
			Kind: ClientDecorationKind(item.Kind()),
		}
	}
	return out, nil
}

// OpenFile asks the server to open path and returns (bufferID, content, version, fromRecovery).
func (r *RPC) OpenFile(ctx context.Context, path string) (uint32, string, uint64, bool, error) {
	fut, rel := r.svc.OpenFile(ctx, func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(r.clientID)
		return p.SetPath(path)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, "", 0, false, err
	}
	content, err := res.Content()
	if err != nil {
		return 0, "", 0, false, err
	}
	return res.BufferId(), content, res.Version(), res.FromRecovery(), nil
}

// DiscardRecovery tells the server to delete the recovery file and reload the
// original file content into the buffer. Returns the original file content.
func (r *RPC) DiscardRecovery(ctx context.Context, bufID uint32) (string, error) {
	fut, rel := r.svc.DiscardRecovery(ctx, func(p proto.EditorService_discardRecovery_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", err
	}
	content, err := res.Content()
	if err != nil {
		return "", err
	}
	return content, nil
}

// ApplyOp sends an edit operation to the server and returns the new version.
func (r *RPC) ApplyOp(ctx context.Context, bufID uint32, op document.Op) (uint64, error) {
	fut, rel := r.svc.ApplyOp(ctx, func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		protoOp, err := p.NewOp()
		if err != nil {
			return err
		}
		protoOp.SetClientId(op.ClientID)
		protoOp.SetVersion(op.Version)
		switch op.Type {
		case document.OpInsert:
			protoOp.SetType(proto.EditOp_OpType_insert)
			protoOp.SetInsertLine(uint32(op.InsertLine))
			protoOp.SetInsertCol(uint32(op.InsertCol))
			if err := protoOp.SetInsertText(op.InsertText); err != nil {
				return err
			}
		case document.OpDelete:
			protoOp.SetType(proto.EditOp_OpType_delete)
			protoOp.SetFromLine(uint32(op.FromLine))
			protoOp.SetFromCol(uint32(op.FromCol))
			protoOp.SetToLine(uint32(op.ToLine))
			protoOp.SetToCol(uint32(op.ToCol))
		default:
			protoOp.SetType(proto.EditOp_OpType_noop)
		}
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, err
	}
	return res.Version(), nil
}

// GetUpdates polls for ops on bufID that arrived after sinceVersion.
func (r *RPC) GetUpdates(ctx context.Context, bufID uint32, since uint64) ([]document.Op, uint64, error) {
	fut, rel := r.svc.GetUpdates(ctx, func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		p.SetSinceVersion(since)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, 0, err
	}

	opList, err := res.Ops()
	if err != nil {
		return nil, 0, err
	}

	ops := make([]document.Op, opList.Len())
	for i := range ops {
		item := opList.At(i)
		insertText, _ := item.InsertText()
		op := document.Op{
			ClientID:   item.ClientId(),
			Version:    item.Version(),
			InsertLine: int(item.InsertLine()),
			InsertCol:  int(item.InsertCol()),
			InsertText: insertText,
			FromLine:   int(item.FromLine()),
			FromCol:    int(item.FromCol()),
			ToLine:     int(item.ToLine()),
			ToCol:      int(item.ToCol()),
		}
		switch item.Type() {
		case proto.EditOp_OpType_insert:
			op.Type = document.OpInsert
		case proto.EditOp_OpType_delete:
			op.Type = document.OpDelete
		default:
			op.Type = document.OpNoop
		}
		ops[i] = op
	}
	return ops, res.Version(), nil
}

// Save asks the server to flush bufID to disk.
func (r *RPC) Save(ctx context.Context, bufID uint32) error {
	fut, rel := r.svc.Save(ctx, func(p proto.EditorService_save_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// SaveAs writes the buffer to newPath and updates the server's path record.
func (r *RPC) SaveAs(ctx context.Context, bufID uint32, newPath string) error {
	fut, rel := r.svc.SaveAs(ctx, func(p proto.EditorService_saveAs_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return p.SetPath(newPath)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// CloseBuffer signals the server that this client is done with bufID.
func (r *RPC) CloseBuffer(ctx context.Context, bufID uint32) error {
	fut, rel := r.svc.CloseBuffer(ctx, func(p proto.EditorService_closeBuffer_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// BufferClientCount returns how many clients currently have bufID open.
func (r *RPC) BufferClientCount(ctx context.Context, bufID uint32) (uint32, error) {
	fut, rel := r.svc.BufferClientCount(ctx, func(p proto.EditorService_bufferClientCount_Params) error {
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, err
	}
	return res.Count(), nil
}

// ClientDiag is a diagnostic delivered from the language server.
type ClientDiag struct {
	Line, Col, EndLine, EndCol int
	Severity                   uint8
	Message, Source            string
}

// ClientHoverResult is the result of a hover request.
type ClientHoverResult struct {
	Found    bool
	Contents string
}

// ClientSigParam is one parameter in a signature.
type ClientSigParam struct{ Label string }

// ClientSigInfo is one signature returned by signatureHelp.
type ClientSigInfo struct {
	Label         string
	Documentation string
	Params        []ClientSigParam
}

// ClientSigHelp is the result of a signatureHelp request.
type ClientSigHelp struct {
	Signatures      []ClientSigInfo
	ActiveSignature int
	ActiveParameter int
}

// ClientCompletion is one completion item.
type ClientCompletion struct {
	Label, Detail, InsertText string
	Kind                      uint8
}

// GetDiagnostics fetches the current diagnostics for bufID from the server.
// DiagnosticsResult bundles diagnostics with the server's lspReady flag.
type DiagnosticsResult struct {
	Diags    []ClientDiag
	LspReady bool
}

func (r *RPC) GetDiagnostics(ctx context.Context, bufID uint32) (DiagnosticsResult, error) {
	fut, rel := r.svc.GetDiagnostics(ctx, func(p proto.EditorService_getDiagnostics_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return DiagnosticsResult{}, err
	}
	list, err := res.Items()
	if err != nil {
		return DiagnosticsResult{}, err
	}
	out := make([]ClientDiag, list.Len())
	for i := range out {
		it := list.At(i)
		msg, _ := it.Message_()
		src, _ := it.Source()
		out[i] = ClientDiag{
			Line: int(it.Line()), Col: int(it.Col()),
			EndLine: int(it.EndLine()), EndCol: int(it.EndCol()),
			Severity: it.Severity(),
			Message:  msg,
			Source:   src,
		}
	}
	return DiagnosticsResult{Diags: out, LspReady: res.LspReady()}, nil
}

// Hover fetches hover information for bufID at (line, col).
func (r *RPC) Hover(ctx context.Context, bufID uint32, line, col int) (ClientHoverResult, error) {
	fut, rel := r.svc.Hover(ctx, func(p proto.EditorService_hover_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ClientHoverResult{}, err
	}
	result, err := res.Result()
	if err != nil {
		return ClientHoverResult{}, err
	}
	contents, _ := result.Contents()
	return ClientHoverResult{Found: result.Found(), Contents: contents}, nil
}

// SignatureHelp fetches signature help for bufID at (line, col).
func (r *RPC) SignatureHelp(ctx context.Context, bufID uint32, line, col int) (ClientSigHelp, error) {
	fut, rel := r.svc.SignatureHelp(ctx, func(p proto.EditorService_signatureHelp_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ClientSigHelp{}, err
	}
	result, err := res.Result()
	if err != nil {
		return ClientSigHelp{}, err
	}
	if !result.Found() {
		return ClientSigHelp{}, nil
	}
	sh := ClientSigHelp{
		ActiveSignature: int(result.ActiveSignature()),
		ActiveParameter: int(result.ActiveParameter()),
	}
	sigs, err := result.Signatures()
	if err != nil {
		return sh, nil
	}
	sh.Signatures = make([]ClientSigInfo, sigs.Len())
	for i := range sh.Signatures {
		s := sigs.At(i)
		label, _ := s.Label()
		doc, _ := s.Documentation()
		si := ClientSigInfo{Label: label, Documentation: doc}
		params, err := s.Parameters()
		if err == nil {
			si.Params = make([]ClientSigParam, params.Len())
			for j := range si.Params {
				pl, _ := params.At(j).Label()
				si.Params[j] = ClientSigParam{Label: pl}
			}
		}
		sh.Signatures[i] = si
	}
	return sh, nil
}

// Complete fetches completion items for bufID at (line, col).
func (r *RPC) Complete(ctx context.Context, bufID uint32, line, col int) ([]ClientCompletion, error) {
	fut, rel := r.svc.Complete(ctx, func(p proto.EditorService_complete_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Items()
	if err != nil {
		return nil, err
	}
	out := make([]ClientCompletion, list.Len())
	for i := range out {
		it := list.At(i)
		label, _ := it.Label()
		detail, _ := it.Detail()
		insert, _ := it.InsertText()
		out[i] = ClientCompletion{Label: label, Detail: detail, InsertText: insert, Kind: it.Kind()}
	}
	return out, nil
}

// ClientLocation is a resolved definition location.
type ClientLocation struct {
	Path string
	Line int
	Col  int
}

// Definition fetches the definition location for the symbol at (line, col) in bufID.
func (r *RPC) Definition(ctx context.Context, bufID uint32, line, col int) (ClientLocation, bool, error) {
	fut, rel := r.svc.Definition(ctx, func(p proto.EditorService_definition_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ClientLocation{}, false, err
	}
	result, err := res.Result()
	if err != nil {
		return ClientLocation{}, false, err
	}
	if !result.Found() {
		return ClientLocation{}, false, nil
	}
	path, _ := result.Path()
	return ClientLocation{
		Path: path,
		Line: int(result.Line()),
		Col:  int(result.Col()),
	}, true, nil
}

// Disconnect unregisters this client from the server.
func (r *RPC) Format(ctx context.Context, bufID uint32) (content string, changed bool, noFormatter bool, err error) {
	fut, rel := r.svc.Format(ctx, func(p proto.EditorService_format_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", false, false, err
	}
	content, err = res.Content()
	if err != nil {
		return "", false, false, err
	}
	return content, res.Changed(), res.NoFormatter(), nil
}

func (r *RPC) Disconnect(ctx context.Context) error {
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
