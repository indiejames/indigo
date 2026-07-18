package server

import (
	"context"
	"os"
	"sort"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// workspaceEditItem is the decoded form of a proto.WorkspaceEdit, tagged with
// its index in the original request so results can be reported back in terms
// the caller understands.
type workspaceEditItem struct {
	origIdx int
	line    int
	col     int
	oldText string
	newText string
}

// applyWorkspaceEditsToBuffer applies items (sorted by line, then col) to buf,
// verifying immediately before each edit that the text at its (shift-adjusted)
// position still equals oldText — a concurrent edit since the caller queued
// this edit skips it rather than corrupting unrelated text. colShift accounts
// for earlier same-line edits changing the length of the line.
func applyWorkspaceEditsToBuffer(buf *document.Buffer, clientID uint64, items []workspaceEditItem) (applied int, skippedIdx []int) {
	colShift := make(map[int]int)
	for i, it := range items {
		lineRunes := []rune(buf.Line(it.line))
		col := it.col + colShift[it.line]
		oldRunes := []rune(it.oldText)
		end := col + len(oldRunes)
		if col < 0 || end > len(lineRunes) || string(lineRunes[col:end]) != it.oldText {
			skippedIdx = append(skippedIdx, i)
			continue
		}
		buf.Apply(document.Op{
			ClientID: clientID,
			Type:     document.OpDelete,
			FromLine: it.line, FromCol: col,
			ToLine: it.line, ToCol: end,
		})
		buf.Apply(document.Op{
			ClientID:   clientID,
			Type:       document.OpInsert,
			InsertLine: it.line, InsertCol: col,
			InsertText: it.newText,
		})
		colShift[it.line] += len([]rune(it.newText)) - len(oldRunes)
		applied++
	}
	return applied, skippedIdx
}

// ApplyWorkspaceEdits applies a batch of search-and-replace edits, one file at
// a time. A path that already has a shared open buffer is edited in place and
// left dirty (not saved) so it stays consistent with whatever other client has
// it open; a path with no open buffer is patched directly on disk.
func (s *editorService) ApplyWorkspaceEdits(_ context.Context, call proto.EditorService_applyWorkspaceEdits) error {
	args := call.Args()
	clientID := args.ClientId()
	protoEdits, err := args.Edits()
	if err != nil {
		return err
	}

	byPath := make(map[string][]workspaceEditItem)
	var order []string
	for i := 0; i < protoEdits.Len(); i++ {
		e := protoEdits.At(i)
		path, err := e.Path()
		if err != nil {
			return err
		}
		oldText, err := e.OldText()
		if err != nil {
			return err
		}
		newText, err := e.NewText()
		if err != nil {
			return err
		}
		if _, ok := byPath[path]; !ok {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], workspaceEditItem{
			origIdx: i,
			line:    int(e.Line()),
			col:     int(e.Col()),
			oldText: oldText,
			newText: newText,
		})
	}

	var appliedCount uint32
	var skipped []uint32

	for _, path := range order {
		items := byPath[path]
		sort.Slice(items, func(a, b int) bool {
			if items[a].line != items[b].line {
				return items[a].line < items[b].line
			}
			return items[a].col < items[b].col
		})

		s.mu.Lock()
		var bufID uint32
		var entry *bufferEntry
		for id, e := range s.buffers {
			if e.buf.Path() == path {
				bufID, entry = id, e
				break
			}
		}
		s.mu.Unlock()

		var applied int
		var pathSkipped []int
		if entry != nil {
			applied, pathSkipped = applyWorkspaceEditsToBuffer(entry.buf, clientID, items)
			content := entry.buf.Content()
			go s.lspMgr.DidChange(path, content)
			go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)
		} else {
			applied, pathSkipped, err = s.applyWorkspaceEditsOnDisk(path, clientID, items)
			if err != nil {
				for _, it := range items {
					skipped = append(skipped, uint32(it.origIdx))
				}
				continue
			}
		}
		appliedCount += uint32(applied)
		for _, idx := range pathSkipped {
			skipped = append(skipped, uint32(items[idx].origIdx))
		}
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetAppliedCount(appliedCount)
	if len(skipped) > 0 {
		list, err := res.NewSkippedIdx(int32(len(skipped)))
		if err != nil {
			return err
		}
		for i, v := range skipped {
			list.Set(i, v)
		}
	}
	return nil
}

// applyWorkspaceEditsOnDisk reads path fresh from disk, applies items via a
// throwaway document.Buffer (reusing the same verify-then-apply logic as the
// live-buffer path), and atomically writes the result back — using the same
// markSaving/rewatch bookkeeping as a normal save so indigo's own write
// doesn't re-trigger the fsnotify watcher.
func (s *editorService) applyWorkspaceEditsOnDisk(path string, clientID uint64, items []workspaceEditItem) (applied int, skippedIdx []int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}

	buf := document.New(path, string(data))
	applied, skippedIdx = applyWorkspaceEditsToBuffer(buf, clientID, items)
	if applied == 0 {
		return applied, skippedIdx, nil
	}

	s.markSaving(path)
	defer s.unmarkSaving(path)
	if err := atomicWriteFile(path, []byte(buf.Content()), 0644); err != nil {
		return 0, nil, err
	}
	s.rewatch(path)
	return applied, skippedIdx, nil
}
