package client

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// sendOp applies the op locally and sends it to the server.
func (m Model) sendOp(op document.Op) tea.Cmd {
	m.buf.Apply(op)
	return m.sendToServer(op)
}

// sendToServer sends op to the server without applying it locally.
// Used by undo when local apply is handled separately.
func (m Model) sendToServer(op document.Op) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ver, err := m.rpc.ApplyOp(ctx, m.bufID, op)
		if err != nil {
			return errorMsg{err}
		}
		m.version = ver
		return nil
	}
}

// applyOp records the inverse of op for undo, then applies op locally and
// queues an async send to the server.
//
// If m.currentGroup is non-nil (i.e. we are inside an Insert session), the
// inverse is appended to the group instead of pushed immediately; the group is
// committed to undoStack when the user presses ESC.
func applyOp(m Model, op document.Op) (Model, tea.Cmd) {
	inv := inverseOp(m, op)
	if m.currentGroup != nil {
		m.currentGroup = append(m.currentGroup, inv)
	} else {
		m.undoStack = append(m.undoStack, []document.Op{inv})
	}
	m.redoStack = nil // any new edit invalidates the redo history
	return m, tea.Batch(m.sendOp(op), m.reparseHighlight())
}

// inverseOp returns the op that reverses op.
// For OpInsert the inverse is an OpDelete of the same span.
// For OpDelete the inverse is an OpInsert of the text that was there.
// The buffer must NOT yet have op applied when this is called.
func inverseOp(m Model, op document.Op) document.Op {
	switch op.Type {
	case document.OpInsert:
		toLine, toCol := insertEndPos(op.InsertLine, op.InsertCol, op.InsertText)
		return document.Op{
			Type:     document.OpDelete,
			FromLine: op.InsertLine,
			FromCol:  op.InsertCol,
			ToLine:   toLine,
			ToCol:    toCol,
		}
	case document.OpDelete:
		return document.Op{
			Type:       document.OpInsert,
			InsertLine: op.FromLine,
			InsertCol:  op.FromCol,
			InsertText: bufText(m, op.FromLine, op.FromCol, op.ToLine, op.ToCol),
		}
	}
	return document.Op{Type: document.OpNoop}
}

// insertEndPos returns the buffer position immediately after inserting text
// starting at (fromLine, fromCol).
func insertEndPos(fromLine, fromCol int, text string) (toLine, toCol int) {
	toLine, toCol = fromLine, fromCol
	for _, r := range text {
		if r == '\n' {
			toLine++
			toCol = 0
		} else {
			toCol++
		}
	}
	return
}

// bufText extracts the text in [fromLine:fromCol, toLine:toCol) from the buffer.
func bufText(m Model, fromLine, fromCol, toLine, toCol int) string {
	if fromLine == toLine {
		runes := []rune(m.buf.Line(fromLine))
		end := min(toCol, len(runes))
		start := min(fromCol, end)
		return string(runes[start:end])
	}
	var sb strings.Builder
	first := []rune(m.buf.Line(fromLine))
	if fromCol <= len(first) {
		sb.WriteString(string(first[fromCol:]))
	}
	sb.WriteByte('\n')
	for l := fromLine + 1; l < toLine; l++ {
		sb.WriteString(m.buf.Line(l))
		sb.WriteByte('\n')
	}
	last := []rune(m.buf.Line(toLine))
	sb.WriteString(string(last[:min(toCol, len(last))]))
	return sb.String()
}

func (m Model) fetchUpdates() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ops, ver, err := m.rpc.GetUpdates(ctx, m.bufID, m.version)
		if err != nil || len(ops) == 0 {
			return nil
		}
		return updatesMsg{ops: ops, version: ver}
	}
}

// doSave formats first (when format_on_save is enabled) then saves.
func (m Model) doSave() tea.Cmd {
	if m.cfg != nil && m.cfg.FormatOnSave {
		return m.fetchFormat(true)
	}
	return m.doSaveNow()
}

// doSaveNow writes the buffer to disk unconditionally.
func (m Model) doSaveNow() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.Save(ctx, m.bufID); err != nil {
			return errorMsg{err}
		}
		return savedMsg{}
	}
}

func (m Model) fetchClientCount() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		count, err := m.rpc.BufferClientCount(ctx, m.bufID)
		if err != nil {
			// On error, assume we're alone and warn.
			return clientCountMsg{count: 1}
		}
		return clientCountMsg{count: count}
	}
}

func (m Model) reparseHighlight() tea.Cmd {
	if m.hlr == nil {
		return nil
	}
	content := []byte(m.buf.Content())
	hlr := m.hlr
	return func() tea.Msg {
		start := time.Now()
		spans := hlr.Highlight(content)
		return highlightMsg{spans: spans, duration: time.Since(start)}
	}
}

// doCloseBuffer tells the server this client is done with this buffer,
// then signals the App to remove it from the buffer list.
func (m Model) doCloseBuffer() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.CloseBuffer(ctx, m.bufID) //nolint:errcheck
		return CloseBufferMsg{}
	}
}

// doSaveAndClose saves the buffer, then closes it.
func (m Model) doSaveAndClose() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.Save(ctx, m.bufID); err != nil {
			return errorMsg{err}
		}
		m.rpc.CloseBuffer(ctx, m.bufID) //nolint:errcheck
		return CloseBufferMsg{}
	}
}
