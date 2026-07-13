package server

import (
	"context"
	"time"

	proto "github.com/indiejames/indigo/internal/proto"
)

// activeContext holds the most recently reported active buffer from any client.
type activeContext struct {
	clientID  uint64
	bufID     uint32
	filePath  string
	line      uint32
	col       uint32
	updatedAt time.Time
	found     bool
}

func (s *editorService) SetActiveContext(_ context.Context, call proto.EditorService_setActiveContext) error {
	args := call.Args()
	fp, err := args.FilePath()
	if err != nil {
		return err
	}
	s.activeCtxMu.Lock()
	s.activeCtx = activeContext{
		clientID:  args.ClientId(),
		bufID:     args.BufId(),
		filePath:  fp,
		line:      args.Line(),
		col:       args.Col(),
		updatedAt: time.Now(),
		found:     true,
	}
	s.activeCtxMu.Unlock()
	_, err = call.AllocResults()
	return err
}

func (s *editorService) GetActiveContext(_ context.Context, call proto.EditorService_getActiveContext) error {
	s.activeCtxMu.RLock()
	ac := s.activeCtx
	s.activeCtxMu.RUnlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	result.SetFound(ac.found)
	if !ac.found {
		return nil
	}
	result.SetClientId(ac.clientID)
	result.SetBufId(ac.bufID)
	if err := result.SetFilePath(ac.filePath); err != nil {
		return err
	}
	result.SetLine(ac.line)
	result.SetCol(ac.col)
	result.SetUpdatedAt(ac.updatedAt.UnixNano())
	return nil
}
