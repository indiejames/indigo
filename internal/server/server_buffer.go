package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// atomicWriteFile writes data to path via a temp file in the same directory
// followed by a rename, so a crash mid-write can never leave a truncated
// file. The existing file's permissions are preserved when present.
func atomicWriteFile(path string, data []byte, defaultMode os.FileMode) error {
	mode := defaultMode
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".indigo-save-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

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
		canonPath := canonicalPath(path)
		for id, e := range s.buffers {
			if e.canonPath == canonPath {
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
		buf:       buf,
		clients:   map[uint64]struct{}{clientID: {}},
		canonPath: canonicalPath(path),
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
		s.addPathWatch(path)
	}
	return nil
}

// RequestOpenFile pushes an open-file command to every connected client,
// exposing the same broadcast Go-native plugins get via
// ServerBridge.PluginOpenFile (plugin_bridge.go) to any client over the wire.
func (s *editorService) RequestOpenFile(_ context.Context, call proto.EditorService_requestOpenFile) error {
	args := call.Args()
	path, err := args.Path()
	if err != nil {
		return err
	}
	if err := s.PluginOpenFile(path, args.Line()); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
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
	// Saved-content hash lets clients keep their dirty markers accurate when
	// another client (e.g. an agent) saves the buffer.
	h := entry.buf.SavedHash()
	if err := res.SetSavedHash(h[:]); err != nil {
		return err
	}

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

// protoToOp converts a wire EditOp into a document.Op for the given client.
func protoToOp(protoOp proto.EditOp, clientID uint64) document.Op {
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
	return op
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

	op := protoToOp(protoOp, clientID)
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

// ApplyOps applies a batch of ops back-to-back. The atomicity guarantee is at
// the request level: once the call arrives, every op is applied even if the
// client disconnects mid-request — so a delete+insert pair can never be left
// half-done by a client crash.
func (s *editorService) ApplyOps(_ context.Context, call proto.EditorService_applyOps) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()
	protoOps, err := args.Ops()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()

	var newVersion uint64
	for i := 0; i < protoOps.Len(); i++ {
		op := protoToOp(protoOps.At(i), clientID)
		newVersion = entry.buf.Apply(op)

		// Notify edit-event handlers when the line count changed.
		var lineDelta int32
		var atLine uint32
		switch op.Type {
		case document.OpInsert:
			if delta := int32(strings.Count(op.InsertText, "\n")); delta != 0 {
				lineDelta = delta
				atLine = uint32(op.InsertLine)
			}
		case document.OpDelete:
			if delta := int32(op.FromLine - op.ToLine); delta != 0 { // negative: lines removed
				lineDelta = delta
				atLine = uint32(op.FromLine)
			}
		}
		if lineDelta != 0 {
			go s.pluginMgr.DispatchEditEvent(context.Background(), bufID, path, atLine, lineDelta)
		}
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(newVersion)

	content := entry.buf.Content()
	go s.lspMgr.DidChange(path, content)
	s.lintMgr.RunOnEdit(path, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)
	return nil
}

func (s *editorService) Save(_ context.Context, call proto.EditorService_save) error {
	// Save can block on a synchronous format call (up to 10s — see Format
	// above) plus a disk write; call.Go() so a slow save doesn't freeze
	// every other RPC on this connection (typing, etc.) behind it.
	call.Go()

	args := call.Args()
	bufID := args.BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	baseBuf := entry.buf
	baseVersion := baseBuf.Version()
	content := baseBuf.Content()
	s.mu.Unlock()

	if s.cfg.FormatOnSave {
		if formatted, changed, err := s.fmtMgr.Format(path, content); err == nil && changed {
			s.mu.Lock()
			entry, ok = s.buffers[bufID]
			// Same compare-and-swap Format() uses: only apply the formatted
			// result if the buffer is still the exact object/version we
			// formatted. Otherwise a concurrent edit landed while the
			// formatter (an external process, up to 10s) was running.
			if ok && entry.buf == baseBuf && entry.buf.Version() == baseVersion {
				newBuf := document.New(path, formatted)
				newBuf.MarkDirty()
				entry.buf = newBuf
				baseBuf = newBuf
				baseVersion = newBuf.Version()
				content = formatted
				s.mu.Unlock()
				go s.lspMgr.DidChange(path, formatted)
			} else if ok {
				// Discard the stale formatted result rather than clobbering
				// the newer edit — save the buffer's actual current state.
				baseBuf = entry.buf
				baseVersion = baseBuf.Version()
				content = baseBuf.Content()
				s.mu.Unlock()
			} else {
				s.mu.Unlock()
				return fmt.Errorf("unknown buffer %d", bufID)
			}
		}
	}

	s.markSaving(path)
	if err := atomicWriteFile(path, []byte(content), 0644); err != nil {
		s.unmarkSaving(path)
		return err
	}
	s.unmarkSaving(path)

	recoveryPath := recoveryFilePath(s.recDir, path)
	s.mu.Lock()
	entry, ok = s.buffers[bufID]
	switch {
	case ok && entry.buf == baseBuf && entry.buf.Version() == baseVersion:
		// What's on disk matches the buffer's content — safe to mark clean
		// and drop the recovery file.
		entry.buf.SetClean()
		s.mu.Unlock()
		os.Remove(recoveryPath) //nolint:errcheck
	case ok:
		// The buffer changed again during the disk write (or a concurrent
		// format-on-save race) — leave it dirty, since what's on disk no
		// longer matches its current content, and refresh the recovery
		// file immediately with the current content rather than leaving a
		// stale-or-absent one until the next periodic flushDirtyBuffers
		// tick, so a crash right now doesn't lose the newer edit.
		current := entry.buf.Content()
		within := int64(entry.buf.ByteLen()) <= s.cfg.RecoveryMaxBytes
		s.mu.Unlock()
		if within {
			os.WriteFile(recoveryPath, []byte(current), 0600) //nolint:errcheck
		} else {
			os.Remove(recoveryPath) //nolint:errcheck
		}
	default:
		// Buffer was closed entirely during the write.
		s.mu.Unlock()
		os.Remove(recoveryPath) //nolint:errcheck
	}

	go s.lspMgr.DidSave(path)
	s.lintMgr.RunAsync(path, content)
	go s.pluginMgr.DispatchBufferSave(context.Background(), bufID, path)

	_, err := call.AllocResults()
	return err
}

func (s *editorService) SaveAs(_ context.Context, call proto.EditorService_saveAs) error {
	// Disk I/O to a new path can stall briefly; call.Go() so it doesn't
	// block other RPCs on this connection behind it, matching Save/Format.
	call.Go()

	args := call.Args()
	bufID := args.BufferId()
	newPath, err := args.Path()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	baseBuf := entry.buf
	baseVersion := baseBuf.Version()
	content := baseBuf.Content()
	oldPath := baseBuf.Path()
	s.mu.Unlock()

	s.markSaving(newPath)
	if err := atomicWriteFile(newPath, []byte(content), 0o644); err != nil {
		s.unmarkSaving(newPath)
		return err
	}
	s.unmarkSaving(newPath)

	s.mu.Lock()
	entry, ok = s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	if entry.buf != baseBuf || entry.buf.Version() != baseVersion {
		// A concurrent edit landed on the buffer while the write to newPath
		// was in flight, so newPath now holds a stale snapshot of the
		// buffer's content. Don't repoint the live (newer) buffer at it —
		// that would silently discard the concurrent edit. The file at
		// newPath is left as-is; the caller should retry SaveAs to pick up
		// the buffer's current content.
		s.mu.Unlock()
		return fmt.Errorf("buffer changed while saving to %s; try again", newPath)
	}
	if oldPath != newPath {
		s.removePathWatch(oldPath)
		s.addPathWatch(newPath)
	}
	entry.buf = document.New(newPath, content)
	entry.buf.SetClean()
	entry.canonPath = canonicalPath(newPath)
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
		s.removePathWatch(removedPath)
		go s.lspMgr.DidClose(removedPath)
		go s.pluginMgr.DispatchBufferClose(context.Background(), bufID, removedPath)
	}

	_, err := call.AllocResults()
	return err
}
