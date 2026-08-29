package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// MoveTextToFile deletes [fromLine,fromCol, toLine,toCol) (end exclusive)
// from bufId and appends it to destPath. This is the primitive behind
// "move function to file": the client identifies the range (e.g. via a
// tree-sitter function text-object) and this handler does the cross-file
// move server-side in one round trip, reusing the same open-buffer-vs-disk
// semantics as ApplyWorkspaceEdits/LspRename. No attempt is made to fix up
// imports or other cross-file references.
func (s *editorService) MoveTextToFile(_ context.Context, call proto.EditorService_moveTextToFile) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufId()
	fromLine, fromCol := int(args.FromLine()), int(args.FromCol())
	toLine, toCol := int(args.ToLine()), int(args.ToCol())
	destPath, err := args.DestPath()
	if err != nil {
		return err
	}

	// Find, extract, and apply the delete in one continuous lock hold: entry.buf
	// is a plain field that Save/SaveAs/Format/DiscardRecovery all wholesale-swap
	// under s.mu.Lock() (e.g. a format-on-save landing between a find-then-unlock
	// and a later Apply call). Extracting the range and deleting it must happen
	// against the exact same buf object, or the delete's coordinates — computed
	// from content that may no longer be entry.buf's content — get applied at
	// the wrong position in whatever buf now is. extractRange/Apply are both
	// pure in-memory work, so holding the lock across them is cheap.
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	srcPath := entry.buf.Path()
	text, err := extractRange(entry.buf, fromLine, fromCol, toLine, toCol)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	entry.buf.Apply(document.Op{
		ClientID: clientID,
		Type:     document.OpDelete,
		FromLine: fromLine, FromCol: fromCol,
		ToLine: toLine, ToCol: toCol,
	})
	content := entry.buf.Content()
	s.mu.Unlock()

	go s.lspMgr.DidChange(srcPath, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, srcPath)

	if err := s.appendTextToFile(clientID, destPath, text); err != nil {
		return err
	}

	_, err = call.AllocResults()
	return err
}

// extractRange returns the text of [fromLine,fromCol, toLine,toCol) (end
// exclusive) in buf. Bounds are checked against buf's actual line/column
// extents first, since Buffer.RuneOffset silently clamps out-of-range
// input instead of erroring.
func extractRange(buf *document.Buffer, fromLine, fromCol, toLine, toCol int) (string, error) {
	lc := buf.LineCount()
	if fromLine < 0 || fromLine >= lc || toLine < 0 || toLine >= lc {
		return "", fmt.Errorf("line out of range in (%d,%d)-(%d,%d)", fromLine, fromCol, toLine, toCol)
	}
	if fromCol < 0 || fromCol > buf.LineLen(fromLine) || toCol < 0 || toCol > buf.LineLen(toLine) {
		return "", fmt.Errorf("column out of range in (%d,%d)-(%d,%d)", fromLine, fromCol, toLine, toCol)
	}
	if fromLine > toLine || (fromLine == toLine && fromCol > toCol) {
		return "", fmt.Errorf("inverted range (%d,%d)-(%d,%d)", fromLine, fromCol, toLine, toCol)
	}
	fromOff := buf.RuneOffset(fromLine, fromCol)
	toOff := buf.RuneOffset(toLine, toCol)
	content := []rune(buf.Content())
	return string(content[fromOff:toOff]), nil
}

// appendOpsForBuffer returns the Op(s) that append text to buf, trimming
// any pre-existing trailing newline(s) first so the result always has
// exactly one blank line between the old content and text (or none if buf
// is empty) — never more, however many trailing newlines buf started with.
func appendOpsForBuffer(buf *document.Buffer, clientID uint64, text string) []document.Op {
	content := buf.Content()
	trimmed := strings.TrimRight(content, "\n")
	trimmedLen := len([]rune(trimmed))
	contentLen := len([]rune(content))
	pos := buf.PosFromOffset(trimmedLen)

	var ops []document.Op
	if trimmedLen < contentLen {
		end := buf.PosFromOffset(contentLen)
		ops = append(ops, document.Op{
			ClientID: clientID,
			Type:     document.OpDelete,
			FromLine: pos.Line, FromCol: pos.Col,
			ToLine: end.Line, ToCol: end.Col,
		})
	}

	insertText := text
	if trimmed != "" {
		insertText = "\n\n" + text
	}
	ops = append(ops, document.Op{
		ClientID:   clientID,
		Type:       document.OpInsert,
		InsertLine: pos.Line,
		InsertCol:  pos.Col,
		InsertText: insertText,
	})
	return ops
}

// appendedContent returns existing with text appended: separated by exactly
// one blank line (none if existing is empty, ignoring trailing newlines),
// and always ending in exactly one trailing newline.
func appendedContent(existing, text string) string {
	trimmed := strings.TrimRight(existing, "\n")
	if trimmed == "" {
		return text + "\n"
	}
	return trimmed + "\n\n" + text + "\n"
}

// appendTextToFile appends text to path, separated from any existing content
// by exactly one blank line. path with a shared open buffer is edited in
// place and left dirty; a path with no open buffer (which may not exist yet)
// is patched directly on disk.
func (s *editorService) appendTextToFile(clientID uint64, path, text string) error {
	canonPath := canonicalPath(path)
	// Find and apply under one continuous lock hold — same reasoning as
	// MoveTextToFile above: appendOpsForBuffer/Apply are pure in-memory work,
	// so there's no reason to unlock in between and risk a concurrent
	// wholesale swap of entry.buf landing before Apply runs.
	s.mu.Lock()
	var bufID uint32
	var entry *bufferEntry
	for id, e := range s.buffers {
		if e.canonPath == canonPath {
			bufID, entry = id, e
			break
		}
	}
	if entry != nil {
		for _, op := range appendOpsForBuffer(entry.buf, clientID, text) {
			entry.buf.Apply(op)
		}
		content := entry.buf.Content()
		s.mu.Unlock()
		go s.lspMgr.DidChange(path, content)
		go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)
		return nil
	}
	s.mu.Unlock()

	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}

	s.markSaving(path)
	defer s.unmarkSaving(path)
	if err := atomicWriteFile(path, []byte(appendedContent(existing, text)), 0644); err != nil {
		return err
	}
	s.addPathWatch(path)
	return nil
}
