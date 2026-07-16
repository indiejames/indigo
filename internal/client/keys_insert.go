package client

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

func (m Model) handleInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Completion navigation takes priority when popup is open.
	if m.completionOn {
		switch msg.String() {
		case "tab", "down":
			m.completionIdx = (m.completionIdx + 1) % len(m.completions)
			return m, nil
		case "shift+tab", "up":
			m.completionIdx = (m.completionIdx - 1 + len(m.completions)) % len(m.completions)
			return m, nil
		case "enter":
			return m.applyCompletion()
		case "esc":
			m.completionOn = false
			m.completions = nil
			return m, nil
		}
		// Any other key closes popup and falls through.
		m.completionOn = false
		m.completions = nil
	}

	switch msg.String() {
	case "ctrl+@", "ctrl+space": // ctrl+space sends NUL → "ctrl+@" in most terminals
		m.completionPrefix = m.currentWordPrefix()
		return m, m.fetchCompletions()

	case "esc":
		m.mode = ModeNormal
		m.sigHelp = nil
		if m.cursor.Col > 0 {
			m.cursor.Col--
		}
		// Commit the undo group accumulated during this Insert session.
		var recordCmd tea.Cmd
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: m.groupBefore})
			fp := m.filePath
			atLine := minAffectedLine(m.currentGroup)
			lineDelta := m.buf.LineCount() - m.insertLineCount
			startLine := m.groupBefore.cursor.Line
			startCol := m.groupBefore.cursor.Col
			depth := len(m.undoStack)
			recordCmd = func() tea.Msg {
				return EditRecordMsg{
					FilePath:  fp,
					Line:      startLine,
					Col:       startCol,
					AtLine:    atLine,
					LineDelta: lineDelta,
					UndoDepth: depth,
				}
			}
		}
		m.currentGroup = nil
		return m, recordCmd

	case "ctrl+c":
		// Escape to normal mode (vim convention) — never close from insert mode.
		m.mode = ModeNormal
		m.sigHelp = nil
		if m.cursor.Col > 0 {
			m.cursor.Col--
		}
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: m.groupBefore})
		}
		m.currentGroup = nil
		return m, nil

	case "ctrl+s":
		return m, m.doSave()

	case "backspace":
		if len(m.extraCursors) > 0 {
			return applyBackspaceToAllCursors(m)
		}
		if m.cursor.Col > 0 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col - 1,
				ToLine:   m.cursor.Line,
				ToCol:    m.cursor.Col,
			}
			m.cursor.Col--
			return applyOp(m, op)
		} else if m.cursor.Line > 0 {
			prevLen := m.buf.LineLen(m.cursor.Line - 1)
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line - 1,
				FromCol:  prevLen,
				ToLine:   m.cursor.Line,
				ToCol:    0,
			}
			m.cursor = document.Pos{Line: m.cursor.Line - 1, Col: prevLen}
			return applyOp(m, op)
		}

	case "delete":
		lineLen := m.buf.LineLen(m.cursor.Line)
		if m.cursor.Col < lineLen {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col,
				ToLine:   m.cursor.Line,
				ToCol:    m.cursor.Col + 1,
			}
			return applyOp(m, op)
		} else if m.cursor.Line < m.buf.LineCount()-1 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  lineLen,
				ToLine:   m.cursor.Line + 1,
				ToCol:    0,
			}
			return applyOp(m, op)
		}

	case "enter":
		if len(m.extraCursors) > 0 {
			return applyInsertToAllCursors(m, "\n")
		}
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\n",
		}
		m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: 0}
		return applyOp(m, op)

	case "tab":
		if len(m.extraCursors) > 0 {
			return applyInsertToAllCursors(m, "\t")
		}
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\t",
		}
		m.cursor.Col++
		return applyOp(m, op)

	case "left":
		m.moveCursor(0, -1)
	case "right":
		m.moveCursor(0, 1)
	case "up":
		m.moveCursor(-1, 0)
	case "down":
		m.moveCursor(1, 0)
	case "home":
		m.cursor.Col = 0
	case "end":
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	default:
		if len(msg.Runes) > 0 {
			text := string(msg.Runes)
			if len(m.extraCursors) > 0 {
				m2, cmd := applyInsertToAllCursors(m, text)
				r := msg.Runes[0]
				if r == '(' || r == ',' {
					return m2, tea.Batch(cmd, m2.fetchSignatureHelp())
				}
				if r == ')' {
					m2.sigHelp = nil
				}
				return m2, cmd
			}
			op := document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: m.cursor.Line,
				InsertCol:  m.cursor.Col,
				InsertText: text,
			}
			m.cursor.Col += len(msg.Runes)
			m2, cmd := applyOp(m, op)
			r := msg.Runes[0]
			// Auto-trigger sig help on '(' or ','.
			if r == '(' || r == ',' {
				return m2, tea.Batch(cmd, m2.fetchSignatureHelp())
			}
			// Close sig help on ')'.
			if r == ')' {
				m2.sigHelp = nil
			}
			// Auto-trigger completions on '.'.
			// Delayed so DidChange reaches the LSP server before Complete does.
			if r == '.' {
				m2.completionPrefix = ""
				delayed := tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
					return triggerCompletionMsg{}
				})
				return m2, tea.Batch(cmd, delayed)
			}
			return m2, cmd
		}
	}
	return m, nil
}

// applyCompletion inserts the selected completion item, replacing the typed prefix.
func (m Model) applyCompletion() (tea.Model, tea.Cmd) {
	if !m.completionOn || len(m.completions) == 0 {
		m.completionOn = false
		return m, nil
	}
	item := m.completions[m.completionIdx]
	m.completionOn = false
	m.completions = nil

	insertText := item.InsertText
	if insertText == "" {
		insertText = item.Label
	}

	// Delete the typed prefix before the cursor.
	prefix := m.completionPrefix
	var cmds []tea.Cmd
	if len(prefix) > 0 {
		from := m.cursor.Col - len([]rune(prefix))
		if from < 0 {
			from = 0
		}
		delOp := document.Op{
			ClientID: m.rpc.ClientID(),
			Type:     document.OpDelete,
			FromLine: m.cursor.Line, FromCol: from,
			ToLine: m.cursor.Line, ToCol: m.cursor.Col,
		}
		m.cursor.Col = from
		var delCmd tea.Cmd
		m2, delCmd := applyOp(m, delOp)
		m = m2
		cmds = append(cmds, delCmd)
	}

	// Insert completion text.
	insOp := document.Op{
		ClientID:   m.rpc.ClientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: insertText,
	}
	m.cursor.Col += len([]rune(insertText))
	m2, insCmd := applyOp(m, insOp)
	cmds = append(cmds, insCmd)
	return m2, tea.Sequence(cmds...)
}
