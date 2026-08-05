package client

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	proto "github.com/indiejames/indigo/internal/proto"
)

// PluginShowMsgMsg is sent by the server when a plugin calls showMessage.
type PluginShowMsgMsg struct{ Text string }

// PluginMoveCursorMsg is sent by the server when a plugin calls moveCursor.
type PluginMoveCursorMsg struct {
	BufID uint32
	Line  uint32
	Col   uint32
}

// PluginDecorationsChangedMsg is sent by the server when a plugin calls
// RefreshDecorations, so the buffer viewing BufID can refetch immediately
// instead of waiting for its next poll tick.
type PluginDecorationsChangedMsg struct{ BufID uint32 }

// ClientPopupItem is one entry in a plugin-driven list popup.
type ClientPopupItem struct {
	Label    string
	Sublabel string
	Data     string // opaque token; sent back to server on selection via index
}

// ShowPluginPopupMsg is sent when a plugin requests an interactive list popup.
type ShowPluginPopupMsg struct {
	Title string
	Items []ClientPopupItem
}

// HidePluginPopupMsg is sent when the server dismisses the plugin popup.
type HidePluginPopupMsg struct{}

// ShowInputPromptMsg is sent when a plugin requests a text-input dialog.
type ShowInputPromptMsg struct {
	Title       string
	Placeholder string
}

// HideInputPromptMsg is sent when the server dismisses the input prompt.
type HideInputPromptMsg struct{}

// callbackServer implements proto.ClientCallback_Server.
// It is created in Dial and holds a send function that routes
// server push calls into the Bubble Tea program.
type callbackServer struct {
	mu   sync.RWMutex
	send func(tea.Msg)
	rpc  *RPC // set after RPC is created; used for direct state updates
}

func (s *callbackServer) setSend(fn func(tea.Msg)) {
	s.mu.Lock()
	s.send = fn
	s.mu.Unlock()
}

func (s *callbackServer) dispatch(msg tea.Msg) {
	s.mu.RLock()
	fn := s.send
	s.mu.RUnlock()
	if fn != nil {
		fn(msg)
	}
}

func (s *callbackServer) ShowMessage(_ context.Context, call proto.ClientCallback_showMessage) error {
	text, err := call.Args().Text()
	if err != nil {
		return err
	}
	s.dispatch(PluginShowMsgMsg{Text: text})
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) MoveCursor(_ context.Context, call proto.ClientCallback_moveCursor) error {
	args := call.Args()
	s.dispatch(PluginMoveCursorMsg{
		BufID: args.BufId(),
		Line:  args.Line(),
		Col:   args.Col(),
	})
	_, err := call.AllocResults()
	return err
}

func (s *callbackServer) OpenFile(_ context.Context, call proto.ClientCallback_openFile) error {
	args := call.Args()
	path, err := args.Path()
	if err != nil {
		return err
	}
	s.dispatch(OpenFileAtMsg{Path: path, Line: int(args.Line()), Col: -1})
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) KeyRegistered(_ context.Context, call proto.ClientCallback_keyRegistered) error {
	trigger, err := call.Args().Trigger()
	if err != nil {
		return err
	}
	s.mu.RLock()
	r := s.rpc
	s.mu.RUnlock()
	clientLog("KeyRegistered called: trigger=%q, rpc_set=%v", trigger, r != nil)
	if r != nil {
		r.addPluginKey(trigger)
	}
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) DecorationsChanged(_ context.Context, call proto.ClientCallback_decorationsChanged) error {
	s.dispatch(PluginDecorationsChangedMsg{BufID: call.Args().BufId()})
	_, err := call.AllocResults()
	return err
}

func (s *callbackServer) InsertHookRegistered(_ context.Context, call proto.ClientCallback_insertHookRegistered) error {
	char, err := call.Args().Char()
	if err != nil {
		return err
	}
	s.mu.RLock()
	r := s.rpc
	s.mu.RUnlock()
	if r != nil {
		r.addInsertHookChar(char)
	}
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) ShowPluginPopup(_ context.Context, call proto.ClientCallback_showPluginPopup) error {
	args := call.Args()
	title, err := args.Title()
	if err != nil {
		return err
	}
	rawItems, err := args.Items()
	if err != nil {
		return err
	}
	items := make([]ClientPopupItem, rawItems.Len())
	for i := range items {
		it := rawItems.At(i)
		label, _ := it.Label()
		sublabel, _ := it.Sublabel()
		data, _ := it.Data()
		items[i] = ClientPopupItem{Label: label, Sublabel: sublabel, Data: data}
	}
	s.dispatch(ShowPluginPopupMsg{Title: title, Items: items})
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) HidePluginPopup(_ context.Context, call proto.ClientCallback_hidePluginPopup) error {
	s.dispatch(HidePluginPopupMsg{})
	_, err := call.AllocResults()
	return err
}

func (s *callbackServer) ShowInputPrompt(_ context.Context, call proto.ClientCallback_showInputPrompt) error {
	args := call.Args()
	title, err := args.Title()
	if err != nil {
		return err
	}
	placeholder, err := args.Placeholder()
	if err != nil {
		return err
	}
	s.dispatch(ShowInputPromptMsg{Title: title, Placeholder: placeholder})
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) HideInputPrompt(_ context.Context, call proto.ClientCallback_hideInputPrompt) error {
	s.dispatch(HideInputPromptMsg{})
	_, err := call.AllocResults()
	return err
}

// FileChangedMsg is sent when a file open in a buffer was modified externally.
type FileChangedMsg struct {
	BufID uint32
	Dirty bool // true if the buffer has unsaved local edits
}

func (s *callbackServer) FileChanged(_ context.Context, call proto.ClientCallback_fileChanged) error {
	args := call.Args()
	msg := FileChangedMsg{BufID: args.BufId(), Dirty: args.Dirty()}
	clientLog("FileChanged callback: bufID=%d dirty=%v", msg.BufID, msg.Dirty)
	s.dispatch(msg)
	_, err := call.AllocResults()
	return err
}
