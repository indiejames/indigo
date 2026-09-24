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

// activeSelection holds the most recently reported editor selection. Guarded
// by activeCtxMu alongside activeContext.
type activeSelection struct {
	clientID  uint64
	bufID     uint32
	startLine uint32
	startCol  uint32
	endLine   uint32
	endCol    uint32 // inclusive
	isLine    bool
	active    bool
}

func (s *editorService) SetActiveSelection(_ context.Context, call proto.EditorService_setActiveSelection) error {
	args := call.Args()
	s.activeCtxMu.Lock()
	s.activeSel = activeSelection{
		clientID:  args.ClientId(),
		bufID:     args.BufId(),
		startLine: args.StartLine(),
		startCol:  args.StartCol(),
		endLine:   args.EndLine(),
		endCol:    args.EndCol(),
		isLine:    args.IsLine(),
		active:    args.Active(),
	}
	s.activeCtxMu.Unlock()
	_, err := call.AllocResults()
	return err
}

func (s *editorService) GetActiveSelection(_ context.Context, call proto.EditorService_getActiveSelection) error {
	s.activeCtxMu.RLock()
	sel := s.activeSel
	s.activeCtxMu.RUnlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetFound(sel.active)
	if !sel.active {
		return nil
	}
	res.SetBufId(sel.bufID)
	res.SetStartLine(sel.startLine)
	res.SetStartCol(sel.startCol)
	res.SetEndLine(sel.endLine)
	res.SetEndCol(sel.endCol)
	res.SetIsLine(sel.isLine)
	return nil
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
	// Both read under one lock, so the selection returned is the context's own
	// and not a report that landed in between.
	s.activeCtxMu.RLock()
	ac := s.activeCtx
	sel := s.activeSel
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
	// Only the same window's selection, in the same buffer. activeSel is
	// whichever client reported a selection last; with two windows open that
	// can be a different window from the one this context describes, and
	// reporting it here would attach one window's selection to another's
	// cursor.
	if sel.active && sel.clientID == ac.clientID && sel.bufID == ac.bufID {
		result.SetHasSelection(true)
		result.SetSelStartLine(sel.startLine)
		result.SetSelStartCol(sel.startCol)
		result.SetSelEndLine(sel.endLine)
		result.SetSelEndCol(sel.endCol)
		result.SetSelIsLine(sel.isLine)
	}
	return nil
}
