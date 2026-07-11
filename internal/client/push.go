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

// SetBookmarkMsg is sent when a plugin sets a bookmark at the given position.
type SetBookmarkMsg struct {
	FilePath string
	Line     int
	Col      int
	Note     string
	Marker   string
}

// ShowBookmarksMsg is sent when a plugin requests the bookmark picker popup.
type ShowBookmarksMsg struct{}

// PromptBookmarkMsg is sent when a plugin requests a name-input prompt before
// creating a bookmark at the given position.
type PromptBookmarkMsg struct {
	FilePath string
	Line     int
	Col      int
	Marker   string
}

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
	s.dispatch(OpenFileAtMsg{Path: path, Line: int(args.Line())})
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

func (s *callbackServer) SetBookmark(_ context.Context, call proto.ClientCallback_setBookmark) error {
	args := call.Args()
	filePath, err := args.FilePath()
	if err != nil {
		return err
	}
	note, err := args.Note()
	if err != nil {
		return err
	}
	marker, err := args.Marker()
	if err != nil {
		return err
	}
	if marker == "" {
		marker = "▶"
	}
	s.dispatch(SetBookmarkMsg{
		FilePath: filePath,
		Line:     int(args.Line()),
		Col:      int(args.Col()),
		Note:     note,
		Marker:   marker,
	})
	_, err = call.AllocResults()
	return err
}

func (s *callbackServer) ShowBookmarks(_ context.Context, call proto.ClientCallback_showBookmarks) error {
	s.dispatch(ShowBookmarksMsg{})
	_, err := call.AllocResults()
	return err
}

func (s *callbackServer) PromptBookmark(_ context.Context, call proto.ClientCallback_promptBookmark) error {
	args := call.Args()
	filePath, err := args.FilePath()
	if err != nil {
		return err
	}
	marker, err := args.Marker()
	if err != nil {
		return err
	}
	if marker == "" {
		marker = "▶"
	}
	s.dispatch(PromptBookmarkMsg{
		FilePath: filePath,
		Line:     int(args.Line()),
		Col:      int(args.Col()),
		Marker:   marker,
	})
	_, err = call.AllocResults()
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
