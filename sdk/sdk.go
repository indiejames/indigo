// Package sdk provides a clean Go interface for writing indigo plugins.
//
// Plugin authors implement the [Plugin] interface and call [Run] from main.
// The SDK handles all Cap'n Proto plumbing; plugin code never touches capnp directly.
//
// Basic usage:
//
//	type MyPlugin struct{}
//
//	func (p *MyPlugin) Init(api *sdk.Api) sdk.Info {
//	    api.OnKey("f", func(key string, ctx sdk.KeyContext) sdk.KeyResponse {
//	        return sdk.KeyResponse{Handled: true, CaptureKeys: 2}
//	    })
//	    return sdk.Info{Name: "myplugin", Version: "0.1.0"}
//	}
//
//	func main() { sdk.Run(&MyPlugin{}) }
package sdk

import (
	"context"
	"fmt"
	"net"
	"os"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// Plugin is the interface plugin authors implement.
type Plugin interface {
	Init(api *Api) Info
}

// Info describes the plugin, returned from Init.
type Info struct {
	Name    string
	Version string
}

// KeyContext is the context passed to key and insert handlers.
type KeyContext struct {
	BufID    uint32
	Mode     string // "normal", "insert", or "capture"
	ClientID uint64
}

// KeyResponse is returned by key and insert handlers.
type KeyResponse struct {
	Handled     bool
	Edits       []TextEdit
	CursorLine  uint32
	CursorCol   uint32
	HasCursor   bool
	CaptureKeys uint32 // >0: capture this many more keypresses in "capture" mode
}

// CommandContext is passed to command handlers.
type CommandContext struct {
	BufID uint32
}

// TextEdit describes a single text replacement in a buffer.
type TextEdit struct {
	From    Position
	To      Position
	NewText string
}

// Position is a (line, col) pair in buffer coordinates (0-based).
type Position struct {
	Line uint32
	Col  uint32
}

// Range is an inclusive [Start, End] range in buffer coordinates.
type Range struct {
	Start Position
	End   Position
}

// DecorationKind controls where a decoration is rendered.
type DecorationKind int

const (
	DecorationGutter    DecorationKind = 0 // rendered in the line number gutter
	DecorationOverlay   DecorationKind = 1 // rendered over text at (Line, Col)
	DecorationStatusBar DecorationKind = 2 // rendered in the status bar center
	DecorationUnderline DecorationKind = 3 // underlines the span [Col, EndCol) on Line
)

// UnderlineStyle selects the underline rendering mode.
type UnderlineStyle int

const (
	UnderlineNone     UnderlineStyle = 0
	UnderlineStraight UnderlineStyle = 1
	UnderlineCurly    UnderlineStyle = 2 // undercurl/wavy; terminals that don't support it fall back to straight
)

// Decoration is one visual annotation returned by a decoration provider.
type Decoration struct {
	Line uint32
	Col  uint32 // start column; for DecorationOverlay this is where the text appears
	Text string
	Kind DecorationKind

	// Underline fields — only used when Kind == DecorationUnderline.
	EndCol         uint32         // exclusive end column of the underlined span
	UnderlineStyle UnderlineStyle // straight or curly
	UnderlineColor string         // hex color e.g. "#FF8C00"; empty = terminal default

	// Fix fields — when Fixable is true, Shift+F at this decoration calls GetFixes.
	Fixable bool
	FixData string // opaque token passed back to GetFixes / ApplyFix
}

// FixItem is one option presented in the fix popup.
// If Replace is non-empty the editor applies it as a text replacement directly.
// If Replace is empty the editor calls ApplyFix with the item's index.
type FixItem struct {
	Label   string
	Replace string
}

// BufferHandlers groups the four buffer lifecycle callbacks.
// Any nil field is silently ignored.
type BufferHandlers struct {
	OnOpen   func(bufID uint32, path string)
	OnChange func(bufID uint32, path string)
	OnSave   func(bufID uint32, path string)
	OnClose  func(bufID uint32, path string)
}

// Api is the editor capability given to a plugin during Init.
// Its methods make synchronous RPC calls back to the editor server.
// The Api may be stored and used after Init returns.
type Api struct {
	api pluginproto.EditorApi
}

// -- Registration --

// OnKey registers fn as the handler for trigger in normal mode.
// trigger is the key string as reported by Bubble Tea (e.g. "f", "ctrl+j", "space").
func (a *Api) OnKey(trigger string, fn func(key string, ctx KeyContext) KeyResponse) error {
	h := pluginproto.KeyHandler_ServerToClient(&keyHandlerServer{fn: fn})
	fut, rel := a.api.RegisterKeyBinding(context.Background(), func(p pluginproto.EditorApi_registerKeyBinding_Params) error {
		if err := p.SetTrigger(trigger); err != nil {
			return err
		}
		return p.SetHandler(h)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// OnInsert registers fn as the handler when char is typed in insert mode.
func (a *Api) OnInsert(char string, fn func(char string, ctx KeyContext) KeyResponse) error {
	h := pluginproto.KeyHandler_ServerToClient(&keyHandlerServer{fn: func(key string, ctx KeyContext) KeyResponse {
		return fn(char, ctx)
	}})
	fut, rel := a.api.RegisterInsertHook(context.Background(), func(p pluginproto.EditorApi_registerInsertHook_Params) error {
		if err := p.SetChar(char); err != nil {
			return err
		}
		return p.SetHandler(h)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// OnCommand registers fn as the handler for the `:name` ex command.
func (a *Api) OnCommand(name string, fn func(args []string, ctx CommandContext)) error {
	h := pluginproto.CommandHandler_ServerToClient(&commandHandlerServer{fn: fn})
	fut, rel := a.api.RegisterCommand(context.Background(), func(p pluginproto.EditorApi_registerCommand_Params) error {
		if err := p.SetName(name); err != nil {
			return err
		}
		return p.SetHandler(h)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// HandleBufferEvents registers handlers for buffer open/change/save/close events.
func (a *Api) HandleBufferEvents(h BufferHandlers) error {
	srv := pluginproto.BufferEventHandler_ServerToClient(&bufferHandlerServer{h: h})
	fut, rel := a.api.RegisterBufferHandler(context.Background(), func(p pluginproto.EditorApi_registerBufferHandler_Params) error {
		return p.SetHandler(srv)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// DecorationHandlers groups the callbacks for a decoration provider.
// GetDecorations is required. GetFixes and ApplyFix are optional (nil = not supported).
type DecorationHandlers struct {
	// GetDecorations returns decorations for the given buffer and visible range.
	GetDecorations func(bufID uint32, clientID uint64, r Range) []Decoration
	// GetFixes returns fix options for the decoration identified by fixData.
	// Called when the user presses Shift+F on a fixable decoration.
	GetFixes func(fixData string) []FixItem
	// ApplyFix handles a FixItem whose Replace field is empty (custom actions).
	ApplyFix func(fixData string, index uint32)
}

// Decorations registers fn as the decoration provider.
// fn is called whenever a client polls for decorations for a buffer/viewport.
// clientID identifies which client is requesting decorations; plugins can use
// this to return decorations only for the client that activated them.
func (a *Api) Decorations(fn func(bufID uint32, clientID uint64, r Range) []Decoration) error {
	return a.DecorationsFull(DecorationHandlers{GetDecorations: fn})
}

// DecorationsFull registers a full decoration provider including optional fix support.
func (a *Api) DecorationsFull(h DecorationHandlers) error {
	srv := pluginproto.DecorationProvider_ServerToClient(&decorProviderServer{h: h})
	fut, rel := a.api.RegisterDecorations(context.Background(), func(p pluginproto.EditorApi_registerDecorations_Params) error {
		return p.SetProvider(srv)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// -- Editor effects --

// ApplyEdit applies a sequence of text edits to a buffer.
func (a *Api) ApplyEdit(bufID uint32, edits []TextEdit) error {
	fut, rel := a.api.ApplyEdit(context.Background(), func(p pluginproto.EditorApi_applyEdit_Params) error {
		p.SetBufId(bufID)
		list, err := p.NewEdits(int32(len(edits)))
		if err != nil {
			return err
		}
		for i, e := range edits {
			item := list.At(i)
			from, err := item.NewFrom()
			if err != nil {
				return err
			}
			from.SetLine(e.From.Line)
			from.SetCol(e.From.Col)
			to, err := item.NewTo()
			if err != nil {
				return err
			}
			to.SetLine(e.To.Line)
			to.SetCol(e.To.Col)
			if err := item.SetNewText(e.NewText); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// MoveCursor moves the cursor to (line, col) in a buffer.
func (a *Api) MoveCursor(bufID uint32, line, col uint32) error {
	fut, rel := a.api.MoveCursor(context.Background(), func(p pluginproto.EditorApi_moveCursor_Params) error {
		p.SetBufId(bufID)
		pos, err := p.NewPos()
		if err != nil {
			return err
		}
		pos.SetLine(line)
		pos.SetCol(col)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// OpenFile tells the editor to open path, scrolled to line (0-based).
func (a *Api) OpenFile(path string, line uint32) error {
	fut, rel := a.api.OpenFile(context.Background(), func(p pluginproto.EditorApi_openFile_Params) error {
		p.SetLine(line)
		return p.SetPath(path)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// ShowMessage displays text in the editor status bar.
func (a *Api) ShowMessage(text string) error {
	fut, rel := a.api.ShowMessage(context.Background(), func(p pluginproto.EditorApi_showMessage_Params) error {
		return p.SetText(text)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// RunProcess runs an external command and returns its output.
func (a *Api) RunProcess(cmd string, args ...string) (stdout, stderr string, exitCode int32, err error) {
	fut, rel := a.api.RunProcess(context.Background(), func(p pluginproto.EditorApi_runProcess_Params) error {
		if err := p.SetCmd(cmd); err != nil {
			return err
		}
		list, err := p.NewArgs(int32(len(args)))
		if err != nil {
			return err
		}
		for i, arg := range args {
			if err := list.Set(i, arg); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", "", 0, err
	}
	stdout, _ = res.Stdout()
	stderr, _ = res.Stderr()
	exitCode = res.ExitCode()
	return stdout, stderr, exitCode, nil
}

// -- Document queries --

// ReadBuffer returns the full text content of bufID.
func (a *Api) ReadBuffer(bufID uint32) (string, error) {
	fut, rel := a.api.ReadBuffer(context.Background(), func(p pluginproto.EditorApi_readBuffer_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", err
	}
	return res.Content()
}

// ReadLines returns lines [startLine, endLine) of bufID (0-based, exclusive end).
func (a *Api) ReadLines(bufID, startLine, endLine uint32) ([]string, error) {
	fut, rel := a.api.ReadLines(context.Background(), func(p pluginproto.EditorApi_readLines_Params) error {
		p.SetBufId(bufID)
		p.SetStartLine(startLine)
		p.SetEndLine(endLine)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	raw, err := res.Lines()
	if err != nil {
		return nil, err
	}
	out := make([]string, raw.Len())
	for i := range out {
		if out[i], err = raw.At(i); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ReadRange returns the text in bufID between from and to.
func (a *Api) ReadRange(bufID uint32, from, to Position) (string, error) {
	fut, rel := a.api.ReadRange(context.Background(), func(p pluginproto.EditorApi_readRange_Params) error {
		p.SetBufId(bufID)
		f, err := p.NewFrom()
		if err != nil {
			return err
		}
		f.SetLine(from.Line)
		f.SetCol(from.Col)
		t, err := p.NewTo()
		if err != nil {
			return err
		}
		t.SetLine(to.Line)
		t.SetCol(to.Col)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", err
	}
	return res.Text()
}

// WordAt returns the word boundaries enclosing pos in bufID.
func (a *Api) WordAt(bufID uint32, pos Position) (start, end Position, found bool, err error) {
	fut, rel := a.api.WordAt(context.Background(), func(p pluginproto.EditorApi_wordAt_Params) error {
		p.SetBufId(bufID)
		pp, err := p.NewPos()
		if err != nil {
			return err
		}
		pp.SetLine(pos.Line)
		pp.SetCol(pos.Col)
		return nil
	})
	defer rel()
	res, resErr := fut.Struct()
	if resErr != nil {
		return Position{}, Position{}, false, resErr
	}
	if !res.Found() {
		return Position{}, Position{}, false, nil
	}
	s, _ := res.Start()
	e, _ := res.End()
	return Position{s.Line(), s.Col()}, Position{e.Line(), e.Col()}, true, nil
}

// BufferInfo returns metadata about bufID.
func (a *Api) BufferInfo(bufID uint32) (path, langID string, lineCount uint32, isDirty bool, err error) {
	fut, rel := a.api.BufferInfo(context.Background(), func(p pluginproto.EditorApi_bufferInfo_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", "", 0, false, err
	}
	path, _ = res.Path()
	langID, _ = res.LanguageId()
	lineCount = res.LineCount()
	isDirty = res.IsDirty()
	return path, langID, lineCount, isDirty, nil
}

// VisibleRange returns the [startLine, endLine] currently visible in clientID's viewport.
func (a *Api) VisibleRange(clientID uint64) (startLine, endLine uint32, err error) {
	fut, rel := a.api.VisibleRange(context.Background(), func(p pluginproto.EditorApi_visibleRange_Params) error {
		p.SetClientId(clientID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, 0, err
	}
	return res.StartLine(), res.EndLine(), nil
}

// -- Run --

// Run starts the plugin process. It reads --socket <path> from os.Args,
// listens on the socket, and serves until the editor disconnects.
// Call this from main() — it blocks until the plugin should exit.
func Run(p Plugin) error {
	socketPath := ""
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--socket" {
			socketPath = os.Args[i+1]
			break
		}
	}
	if socketPath == "" {
		return fmt.Errorf("indigo-sdk: --socket <path> argument is required")
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("indigo-sdk: listen %s: %w", socketPath, err)
	}
	defer os.Remove(socketPath) //nolint:errcheck

	conn, err := ln.Accept()
	ln.Close() //nolint:errcheck
	if err != nil {
		return fmt.Errorf("indigo-sdk: accept: %w", err)
	}

	ps := &pluginServer{plugin: p}
	client := pluginproto.Plugin_ServerToClient(ps)

	rpcConn := rpc.NewConn(rpc.NewStreamTransport(conn), &rpc.Options{
		BootstrapClient: capnp.Client(client),
	})
	defer rpcConn.Close() //nolint:errcheck

	<-rpcConn.Done()
	return nil
}

// -- Internal capnp server implementations --

type pluginServer struct {
	plugin Plugin
}

func (s *pluginServer) Initialize(_ context.Context, call pluginproto.Plugin_initialize) error {
	// AddRef keeps the capability alive after this call returns.
	editorApi := pluginproto.EditorApi(call.Args().Api().AddRef())

	api := &Api{api: editorApi}
	info := s.plugin.Init(api)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	pinfo, err := res.NewInfo()
	if err != nil {
		return err
	}
	if err := pinfo.SetName(info.Name); err != nil {
		return err
	}
	return pinfo.SetVersion(info.Version)
}

type keyHandlerServer struct {
	fn func(key string, ctx KeyContext) KeyResponse
}

func (s *keyHandlerServer) Handle(_ context.Context, call pluginproto.KeyHandler_handle) error {
	key, _ := call.Args().Key()
	kctx, _ := call.Args().Ctx()
	mode, _ := kctx.Mode()

	resp := s.fn(key, KeyContext{BufID: kctx.BufId(), Mode: mode, ClientID: kctx.ClientId()})

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	r, err := res.NewResponse()
	if err != nil {
		return err
	}
	r.SetHandled(resp.Handled)
	r.SetHasCursor(resp.HasCursor)
	r.SetCaptureKeys(resp.CaptureKeys)
	if resp.HasCursor {
		pos, err := r.NewCursorPos()
		if err != nil {
			return err
		}
		pos.SetLine(resp.CursorLine)
		pos.SetCol(resp.CursorCol)
	}
	if len(resp.Edits) > 0 {
		edits, err := r.NewEdits(int32(len(resp.Edits)))
		if err != nil {
			return err
		}
		for i, e := range resp.Edits {
			item := edits.At(i)
			from, err := item.NewFrom()
			if err != nil {
				return err
			}
			from.SetLine(e.From.Line)
			from.SetCol(e.From.Col)
			to, err := item.NewTo()
			if err != nil {
				return err
			}
			to.SetLine(e.To.Line)
			to.SetCol(e.To.Col)
			if err := item.SetNewText(e.NewText); err != nil {
				return err
			}
		}
	}
	return nil
}

type commandHandlerServer struct {
	fn func(args []string, ctx CommandContext)
}

func (s *commandHandlerServer) Handle(_ context.Context, call pluginproto.CommandHandler_handle) error {
	rawArgs, _ := call.Args().Args()
	args := make([]string, rawArgs.Len())
	for i := range args {
		args[i], _ = rawArgs.At(i)
	}
	kctx, _ := call.Args().Ctx()
	s.fn(args, CommandContext{BufID: kctx.BufId()})
	_, err := call.AllocResults()
	return err
}

type bufferHandlerServer struct {
	h BufferHandlers
}

func (s *bufferHandlerServer) OnOpen(_ context.Context, call pluginproto.BufferEventHandler_onOpen) error {
	if s.h.OnOpen != nil {
		ev, _ := call.Args().Event()
		path, _ := ev.Path()
		s.h.OnOpen(ev.BufId(), path)
	}
	_, err := call.AllocResults()
	return err
}

func (s *bufferHandlerServer) OnChange(_ context.Context, call pluginproto.BufferEventHandler_onChange) error {
	if s.h.OnChange != nil {
		ev, _ := call.Args().Event()
		path, _ := ev.Path()
		s.h.OnChange(ev.BufId(), path)
	}
	_, err := call.AllocResults()
	return err
}

func (s *bufferHandlerServer) OnSave(_ context.Context, call pluginproto.BufferEventHandler_onSave) error {
	if s.h.OnSave != nil {
		ev, _ := call.Args().Event()
		path, _ := ev.Path()
		s.h.OnSave(ev.BufId(), path)
	}
	_, err := call.AllocResults()
	return err
}

func (s *bufferHandlerServer) OnClose(_ context.Context, call pluginproto.BufferEventHandler_onClose) error {
	if s.h.OnClose != nil {
		ev, _ := call.Args().Event()
		path, _ := ev.Path()
		s.h.OnClose(ev.BufId(), path)
	}
	_, err := call.AllocResults()
	return err
}

type decorProviderServer struct {
	h DecorationHandlers
}

func (s *decorProviderServer) GetDecorations(_ context.Context, call pluginproto.DecorationProvider_getDecorations) error {
	args := call.Args()
	bufID := args.BufId()
	clientID := args.ClientId()
	rng, _ := args.VisibleRange()
	start, _ := rng.Start()
	end, _ := rng.End()

	decorations := s.h.GetDecorations(bufID, clientID, Range{
		Start: Position{Line: start.Line(), Col: start.Col()},
		End:   Position{Line: end.Line(), Col: end.Col()},
	})

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewDecorations(int32(len(decorations)))
	if err != nil {
		return err
	}
	for i, d := range decorations {
		item := list.At(i)
		item.SetLine(d.Line)
		item.SetCol(d.Col)
		item.SetEndCol(d.EndCol)
		if err := item.SetText(d.Text); err != nil {
			return err
		}
		item.SetKind(pluginproto.DecorationKind(d.Kind))
		item.SetUnderlineStyle(pluginproto.UnderlineStyle(d.UnderlineStyle))
		if err := item.SetUnderlineColor(d.UnderlineColor); err != nil {
			return err
		}
		item.SetFixable(d.Fixable)
		if err := item.SetFixData(d.FixData); err != nil {
			return err
		}
	}
	return nil
}

func (s *decorProviderServer) GetFixes(_ context.Context, call pluginproto.DecorationProvider_getFixes) error {
	fixData, _ := call.Args().FixData()
	var items []FixItem
	if s.h.GetFixes != nil {
		items = s.h.GetFixes(fixData)
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewItems(int32(len(items)))
	if err != nil {
		return err
	}
	for i, it := range items {
		fi := list.At(i)
		if err := fi.SetLabel(it.Label); err != nil {
			return err
		}
		if err := fi.SetReplace(it.Replace); err != nil {
			return err
		}
	}
	return nil
}

func (s *decorProviderServer) ApplyFix(_ context.Context, call pluginproto.DecorationProvider_applyFix) error {
	fixData, _ := call.Args().FixData()
	index := call.Args().Index()
	if s.h.ApplyFix != nil {
		s.h.ApplyFix(fixData, index)
	}
	_, err := call.AllocResults()
	return err
}
