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
	if r != nil {
		r.addPluginKey(trigger)
	}
	_, err = call.AllocResults()
	return err
}
