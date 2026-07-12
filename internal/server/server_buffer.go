package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

func (s *editorService) OpenFile(_ context.Context, call proto.EditorService_openFile) error {
	args := call.Args()
	clientID := args.ClientId()
	path, err := args.Path()
	if err != nil {
		return err
	}
	serverLog("OpenFile: clientID=%d path=%q", clientID, path)

	content, fromRecovery := s.loadContent(path)

	s.mu.Lock()
	// Check if file is already open (skip dedup for untitled buffers).
	if path != "" {
		for id, e := range s.buffers {
			if e.buf.Path() == path {
				e.clients[clientID] = struct{}{}
				ver := e.buf.Version()
				s.mu.Unlock()

				res, err := call.AllocResults()
				if err != nil {
					return err
				}
				res.SetBufferId(id)
				if err := res.SetContent(e.buf.Content()); err != nil {
					return err
				}
				res.SetVersion(ver)
				return nil
			}
		}
	}

	s.nextBuf++
	bufID := s.nextBuf
	buf := document.New(path, content)
	if fromRecovery {
		buf.MarkDirty()
	}
	s.buffers[bufID] = &bufferEntry{
		buf:     buf,
		clients: map[uint64]struct{}{clientID: {}},
	}
	ver := buf.Version()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetBufferId(bufID)
	if err := res.SetContent(content); err != nil {
		return err
	}
	res.SetVersion(ver)
	res.SetFromRecovery(fromRecovery)
	if path != "" {
		go s.lspMgr.DidOpen(path, content)
		go s.pluginMgr.DispatchBufferOpen(context.Background(), bufID, path)
		if s.watcher != nil {
			s.watcher.Add(path) //nolint:errcheck
		}
	}
	return nil
}

func (s *editorService) DiscardRecovery(_ context.Context, call proto.EditorService_discardRecovery) error {
	args := call.Args()
	bufID := args.BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	os.Remove(recoveryFilePath(s.recDir, path)) //nolint:errcheck

	content := ""
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}

	s.mu.Lock()
	if e, ok := s.buffers[bufID]; ok {
		e.buf = document.New(path, content)
	}
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	return res.SetContent(content)
}

// loadContent reads a file's content, preferring a newer recovery file if one exists.
func (s *editorService) loadContent(path string) (content string, fromRecovery bool) {
	if path == "" {
		return "", false // untitled buffer: no content, no recovery
	}
	var origModTime time.Time
	if info, err := os.Stat(path); err == nil {
		origModTime = info.ModTime()
	}
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}
	rp := recoveryFilePath(s.recDir, path)
	if recInfo, err := os.Stat(rp); err == nil && recInfo.ModTime().After(origModTime) {
		if recData, err := os.ReadFile(rp); err == nil {
			return string(recData), true
		}
	}
	return content, false
}

func (s *editorService) GetUpdates(_ context.Context, call proto.EditorService_getUpdates) error {
	args := call.Args()
	bufID := args.BufferId()
	since := args.SinceVersion()
	callerID := args.ClientId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	ops := entry.buf.OpsSince(since)
	// Filter out ops that originated from the caller.
	filtered := ops[:0:0]
	for _, op := range ops {
		if op.ClientID != callerID {
			filtered = append(filtered, op)
		}
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(entry.buf.Version())

	if len(filtered) == 0 {
		return nil
	}

	list, err := res.NewOps(int32(len(filtered)))
	if err != nil {
		return err
	}
	for i, op := range filtered {
		item := list.At(i)
		item.SetClientId(op.ClientID)
		item.SetVersion(op.Version)
		switch op.Type {
		case document.OpInsert:
			item.SetType(proto.EditOp_OpType_insert)
			item.SetInsertLine(uint32(op.InsertLine))
			item.SetInsertCol(uint32(op.InsertCol))
			if err := item.SetInsertText(op.InsertText); err != nil {
				return err
			}
		case document.OpDelete:
			item.SetType(proto.EditOp_OpType_delete)
			item.SetFromLine(uint32(op.FromLine))
			item.SetFromCol(uint32(op.FromCol))
			item.SetToLine(uint32(op.ToLine))
			item.SetToCol(uint32(op.ToCol))
		}
	}
	return nil
}

func (s *editorService) ApplyOp(_ context.Context, call proto.EditorService_applyOp) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()
	protoOp, err := args.Op()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	insertText, _ := protoOp.InsertText()
	op := document.Op{
		ClientID:   clientID,
		InsertLine: int(protoOp.InsertLine()),
		InsertCol:  int(protoOp.InsertCol()),
		InsertText: insertText,
		FromLine:   int(protoOp.FromLine()),
		FromCol:    int(protoOp.FromCol()),
		ToLine:     int(protoOp.ToLine()),
		ToCol:      int(protoOp.ToCol()),
	}
	switch protoOp.Type() {
	case proto.EditOp_OpType_insert:
		op.Type = document.OpInsert
	case proto.EditOp_OpType_delete:
		op.Type = document.OpDelete
	default:
		op.Type = document.OpNoop
	}

	newVersion := entry.buf.Apply(op)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(newVersion)

	path := entry.buf.Path()
	content := entry.buf.Content()
	go s.lspMgr.DidChange(path, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)

	// Notify edit-event handlers when the line count changed.
	var lineDelta int32
	var atLine uint32
	switch op.Type {
	case document.OpInsert:
		delta := int32(strings.Count(op.InsertText, "\n"))
		if delta != 0 {
			lineDelta = delta
			atLine = uint32(op.InsertLine)
		}
	case document.OpDelete:
		delta := int32(op.FromLine - op.ToLine) // negative: lines removed
		if delta != 0 {
			lineDelta = delta
			atLine = uint32(op.FromLine)
		}
	}
	if lineDelta != 0 {
		go s.pluginMgr.DispatchEditEvent(context.Background(), bufID, path, atLine, lineDelta)
	}

	return nil
}

func (s *editorService) Save(_ context.Context, call proto.EditorService_save) error {
	args := call.Args()
	bufID := args.BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	path := entry.buf.Path()
	content := entry.buf.Content()

	if s.cfg.FormatOnSave {
		if formatted, changed, err := s.fmtMgr.Format(path, content); err == nil && changed {
			content = formatted
			entry.buf = document.New(path, content)
			s.mu.Lock()
			s.buffers[bufID] = entry
			s.mu.Unlock()
			go s.lspMgr.DidChange(path, content)
		}
	}

	s.markSaving(path)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		s.unmarkSaving(path)
		return err
	}
	s.unmarkSaving(path)
	entry.buf.SetClean()
	os.Remove(recoveryFilePath(s.recDir, path)) //nolint:errcheck
	go s.lspMgr.DidSave(path)
	go s.pluginMgr.DispatchBufferSave(context.Background(), bufID, path)

	_, err := call.AllocResults()
	return err
}

func (s *editorService) SaveAs(_ context.Context, call proto.EditorService_saveAs) error {
	args := call.Args()
	bufID := args.BufferId()
	newPath, err := args.Path()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	content := entry.buf.Content()
	s.markSaving(newPath)
	if err := os.WriteFile(newPath, []byte(content), 0o644); err != nil {
		s.unmarkSaving(newPath)
		return err
	}
	s.unmarkSaving(newPath)
	oldPath := entry.buf.Path()
	entry.buf = document.New(newPath, content)
	entry.buf.SetClean()
	s.mu.Lock()
	s.buffers[bufID] = entry
	s.mu.Unlock()

	if oldPath != "" {
		os.Remove(recoveryFilePath(s.recDir, oldPath)) //nolint:errcheck
	}
	go s.lspMgr.DidOpen(newPath, content)
	go s.pluginMgr.DispatchBufferOpen(context.Background(), bufID, newPath)

	_, err = call.AllocResults()
	return err
}

func (s *editorService) BufferClientCount(_ context.Context, call proto.EditorService_bufferClientCount) error {
	bufID := call.Args().BufferId()
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	var count uint32
	if ok {
		count = uint32(len(entry.clients))
	}
	s.mu.Unlock()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetCount(count)
	return nil
}

func (s *editorService) CloseBuffer(_ context.Context, call proto.EditorService_closeBuffer) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()
	serverLog("CloseBuffer: clientID=%d bufID=%d", clientID, bufID)

	var removedPath string
	s.mu.Lock()
	if entry, ok := s.buffers[bufID]; ok {
		delete(entry.clients, clientID)
		if len(entry.clients) == 0 {
			removedPath = entry.buf.Path()
			delete(s.buffers, bufID)
		}
	}
	s.mu.Unlock()

	// Always clean up the recovery file; for untitled buffers this removes the
	// sha256("") recovery entry so it isn't replayed on the next invocation.
	os.Remove(recoveryFilePath(s.recDir, removedPath)) //nolint:errcheck
	if removedPath != "" {
		if s.watcher != nil {
			s.watcher.Remove(removedPath) //nolint:errcheck
		}
		go s.lspMgr.DidClose(removedPath)
		go s.pluginMgr.DispatchBufferClose(context.Background(), bufID, removedPath)
	}

	_, err := call.AllocResults()
	return err
}
