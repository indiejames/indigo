package client

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// ReportActiveContextCmd returns a Cmd that tells the server this client's
// current buffer and cursor position is the active context. Returns nil when
// rpc is nil (e.g. in tests).
func (m Model) ReportActiveContextCmd() tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	clientID := m.rpc.ClientID()
	bufID := m.bufID
	filePath := m.filePath
	line := uint32(m.cursor.Line)
	col := uint32(m.cursor.Col)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.rpc.SetActiveContext(ctx, clientID, bufID, filePath, line, col)
		return nil
	}
}

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

// opLineDelta returns the first affected line and the net line-count change
// produced by applying op. Insert ops that add newlines return a positive delta;
// deletes that span multiple lines return a negative delta.
func opLineDelta(op document.Op) (atLine, delta int) {
	switch op.Type {
	case document.OpInsert:
		return op.InsertLine, strings.Count(op.InsertText, "\n")
	case document.OpDelete:
		return op.FromLine, -(op.ToLine - op.FromLine)
	}
	return 0, 0
}

// minAffectedLine returns the smallest line number referenced by any op in the
// slice (which holds inverse ops from an insert session). Inverse ops reference
// the same line numbers as the corresponding forward ops.
func minAffectedLine(ops []document.Op) int {
	min := -1
	for _, op := range ops {
		l := op.InsertLine
		if op.Type == document.OpDelete {
			l = op.FromLine
		}
		if min < 0 || l < min {
			min = l
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// applyOp records the inverse of op for undo, then applies op locally and
// queues an async send to the server.
//
// If m.currentGroup is non-nil (i.e. we are inside an Insert session), the
// inverse is appended to the group instead of pushed immediately; the group is
// committed to undoStack when the user presses ESC.
// In normal mode (currentGroup == nil) an EditRecordMsg is also emitted so the
// App can adjust existing jump entries and add a new one.
func applyOp(m Model, op document.Op) (Model, tea.Cmd) {
	inv := inverseOp(m, op)
	var recordCmd tea.Cmd
	if m.currentGroup != nil {
		m.currentGroup = append(m.currentGroup, inv)
	} else {
		m.undoStack = append(m.undoStack, undoEntry{ops: []document.Op{inv}, before: m.cursorSnap()})
		atLine, delta := opLineDelta(op)
		fp, line, col := m.filePath, m.cursor.Line, m.cursor.Col
		depth := len(m.undoStack)
		recordCmd = func() tea.Msg {
			return EditRecordMsg{FilePath: fp, Line: line, Col: col, AtLine: atLine, LineDelta: delta, UndoDepth: depth}
		}
	}
	m.redoStack = nil // any new edit invalidates the redo history
	return m, tea.Batch(m.sendOp(op), m.reparseHighlight(), recordCmd)
}

// applyBatch applies a slice of ops as a single undoable action.
// Inverses are computed before each apply, so the ops must not share lines.
// Always emits an EditRecordMsg so the App can update the jump list.
func applyBatch(m Model, ops []document.Op) (Model, tea.Cmd) {
	if len(ops) == 0 {
		return m, nil
	}
	before := m.cursorSnap()
	inverses := make([]document.Op, len(ops))
	cmds := make([]tea.Cmd, 0, len(ops)+2)
	atLine, delta := -1, 0
	for i, op := range ops {
		inverses[i] = inverseOp(m, op) // must be before Apply
		cmds = append(cmds, m.sendOp(op))
		al, d := opLineDelta(op)
		if atLine < 0 || al < atLine {
			atLine = al
		}
		delta += d
	}
	if atLine < 0 {
		atLine = 0
	}
	m.undoStack = append(m.undoStack, undoEntry{ops: inverses, before: before})
	m.redoStack = nil
	fp, line, col := m.filePath, m.cursor.Line, m.cursor.Col
	depth := len(m.undoStack)
	cmds = append(cmds, m.reparseHighlight(), func() tea.Msg {
		return EditRecordMsg{FilePath: fp, Line: line, Col: col, AtLine: atLine, LineDelta: delta, UndoDepth: depth}
	})
	return m, tea.Batch(cmds...)
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

// saveAsPromptMsg triggers the save-as command prompt in Update.
type saveAsPromptMsg struct{ thenClose bool }

// doSave formats first (when format_on_save is enabled) then saves.
// For untitled buffers it triggers the save-as prompt instead.
func (m Model) doSave() tea.Cmd {
	if m.filePath == "" {
		return func() tea.Msg { return saveAsPromptMsg{} }
	}
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

// doSaveAsNow writes the buffer to newPath via the server.
func (m Model) doSaveAsNow(newPath string, thenClose bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.SaveAs(ctx, m.bufID, newPath); err != nil {
			return errorMsg{err}
		}
		return savedAsMsg{newPath: newPath, thenClose: thenClose}
	}
}


func (m Model) reparseHighlight() tea.Cmd {
	if m.hlr == nil {
		return nil
	}
	content := []byte(m.buf.Content())
	hlr := m.hlr
	bracketColors := m.cfg != nil && m.cfg.BracketColors
	return func() tea.Msg {
		start := time.Now()
		spans := hlr.Highlight(content)
		if bracketColors {
			// Prepend bracket spans so they win over punctuation.bracket.
			for ln, bs := range highlight.BracketSpans(content) {
				spans[ln] = append(bs, spans[ln]...)
			}
		}
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
	if m.filePath == "" {
		return func() tea.Msg { return saveAsPromptMsg{thenClose: true} }
	}
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
