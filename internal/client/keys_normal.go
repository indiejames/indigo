package client

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

func (m Model) handleNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Fix popup navigation takes priority.
	if len(m.fixItems) > 0 {
		return m.handleFixPopup(msg)
	}

	// Capture mode: plugin owns keypresses until it signals done.
	if m.captureMode {
		return m.handleCapturedKey(msg)
	}

	// Handle active prefix sequence before the main switch.
	if len(m.prefixSeq) > 0 {
		if msg.String() == "esc" {
			m.prefixSeq = nil
			return m, nil
		}
		if len(msg.Runes) > 0 {
			newSeq := append(append([]rune(nil), m.prefixSeq...), msg.Runes[0])
			if cmd, ok := m.resolveCommand(newSeq); ok {
				if cmd.execute != nil {
					m.prefixSeq = nil
					return cmd.execute(m)
				}
				if len(cmd.children) > 0 {
					m.prefixSeq = newSeq
					return m, nil
				}
			}
		}
		m.prefixSeq = nil
		return m, nil
	}

	// Let plugins handle keys they registered before built-in commands.
	if m.rpc != nil && m.rpc.HasPluginKey(msg.String()) {
		return m.handlePluginKeyRPC(msg.String())
	}

	switch msg.String() {
	case "ctrl+c":
		m.status = "Use :q to quit"
	case "ctrl+p":
		return m, func() tea.Msg { return OpenPickerMsg{} }

	case ":":
		m.mode = ModeCommand
		m.cmdBuf = ""
		m.cmdCompletionIdx = -1

	case "/":
		m.searchQuery = ""
		m.searchMatches = nil
		m.searchIdx = -1
		m.searchOrigin = m.cursor
		m.mode = ModeSearch

	case "n":
		if len(m.searchMatches) > 0 {
			m.searchIdx = (m.searchIdx + 1) % len(m.searchMatches)
			sm := m.searchMatches[m.searchIdx]
			m.cursor = document.Pos{Line: sm.line, Col: sm.col}
			m.scrollToCursor()
		}

	case "N":
		if len(m.searchMatches) > 0 {
			m.searchIdx = (m.searchIdx - 1 + len(m.searchMatches)) % len(m.searchMatches)
			sm := m.searchMatches[m.searchIdx]
			m.cursor = document.Pos{Line: sm.line, Col: sm.col}
			m.scrollToCursor()
		}

	case "esc":
		m.extraCursors = nil
		m.sel = nil
		m.mark = nil
		m = m.withClearedSearch()

	// Enter insert mode — clear any selection and start an undo group.
	case "i":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		m.mode = ModeInsert

	case "a":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		m.mode = ModeInsert
		m.cursor.Col++

	case "A":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		m.mode = ModeInsert
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	case "o":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		m.mode = ModeInsert
		line := m.cursor.Line
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: line,
			InsertCol:  m.buf.LineLen(line),
			InsertText: "\n",
		}
		m.cursor = document.Pos{Line: line + 1, Col: 0}
		return applyOp(m, op)

	case "O":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		m.mode = ModeInsert
		col := 0
		if m.cursor.Line > 0 {
			col = m.buf.LineLen(m.cursor.Line - 1)
		}
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: max(0, m.cursor.Line-1),
			InsertCol:  col,
			InsertText: "\n",
		}
		m.cursor = document.Pos{Line: m.cursor.Line, Col: 0}
		return applyOp(m, op)

	case "ctrl+s":
		return m, m.doSave()

	case "K":
		return m, m.fetchHover()

	case "D":
		if len(m.diagsAtPos(m.cursor.Line, m.cursor.Col)) > 0 {
			m.diagPopup = !m.diagPopup
		} else {
			m.status = "No diagnostics on this line"
		}

	case "u":
		if len(m.undoStack) > 0 {
			entry := m.undoStack[len(m.undoStack)-1]
			m.undoStack = m.undoStack[:len(m.undoStack)-1]
			// Snapshot current (post-edit) state so redo can restore it.
			redoEntry := undoEntry{before: m.cursorSnap()}
			var cmds []tea.Cmd
			for i := len(entry.ops) - 1; i >= 0; i-- {
				inv := entry.ops[i]
				inv.ClientID = m.rpc.ClientID()
				reInv := inverseOp(m, inv) // compute re-inverse before applying
				m.buf.Apply(inv)
				cmds = append(cmds, m.sendToServer(inv))
				redoEntry.ops = append(redoEntry.ops, reInv)
			}
			m.redoStack = append(m.redoStack, redoEntry)
			// Restore pre-edit cursor state (cursor + selection + extra cursors).
			m.cursor = entry.before.cursor
			m.sel = entry.before.sel
			m.extraCursors = entry.before.extras
			m.scrollToCursor()
			if len(m.undoStack) == m.savedUndoDepth {
				m.buf.SetClean()
			}
			fp := m.filePath
			newDepth := len(m.undoStack)
			// Compute the net line delta produced by the inverse ops that were
			// just applied — this lets the App reverse any line-shift it made
			// when those edits were originally recorded.
			atLine, lineDelta := -1, 0
			for _, inv := range entry.ops {
				al, d := opLineDelta(inv)
				if atLine < 0 || al < atLine {
					atLine = al
				}
				lineDelta += d
			}
			if atLine < 0 {
				atLine = 0
			}
			undoCmd := func() tea.Msg {
				return UndoMsg{FilePath: fp, NewDepth: newDepth, AtLine: atLine, LineDelta: lineDelta}
			}
			return m, tea.Sequence(append(cmds, m.reparseHighlight(), undoCmd)...)
		}

	case "U":
		if len(m.redoStack) > 0 {
			entry := m.redoStack[len(m.redoStack)-1]
			m.redoStack = m.redoStack[:len(m.redoStack)-1]
			// Snapshot current (pre-edit) state so undo can restore it.
			newUndoEntry := undoEntry{before: m.cursorSnap()}
			var cmds []tea.Cmd
			for i := len(entry.ops) - 1; i >= 0; i-- {
				op := entry.ops[i]
				op.ClientID = m.rpc.ClientID()
				inv := inverseOp(m, op) // compute inverse before applying
				m.buf.Apply(op)
				cmds = append(cmds, m.sendToServer(op))
				newUndoEntry.ops = append(newUndoEntry.ops, inv)
			}
			m.undoStack = append(m.undoStack, newUndoEntry)
			// Restore post-edit cursor state (cursor + selection + extra cursors).
			m.cursor = entry.before.cursor
			m.sel = entry.before.sel
			m.extraCursors = entry.before.extras
			m.scrollToCursor()
			if len(m.undoStack) == m.savedUndoDepth {
				m.buf.SetClean()
			}
			return m, tea.Sequence(append(cmds, m.reparseHighlight())...)
		}

	// Selection: create or extend. All operations apply to every cursor.
	case "W":
		m.applyToAllCursors(func(m *Model) {
			m.extendToNextWordStart()
		})

	case "E":
		m.applyToAllCursors(func(m *Model) {
			m.extendWordForward()
		})

	case "x":
		m.applyToAllCursors(func(m *Model) {
			m.selectLine()
		})

	case "X":
		m.applyToAllCursors(func(m *Model) {
			m.extendLineBackward()
		})

	case "%":
		m.selectAll() // selects whole buffer; primary only makes sense here

	case ";":
		m.sel = nil
		for i := range m.extraCursors {
			m.extraCursors[i].sel = nil
		}

	case "alt+;":
		m.applyToAllCursors(func(m *Model) {
			m.flipSelection()
		})

	// Operators: act on selection (no-op if nothing selected).
	case "d":
		m = m.withClearedSearch()
		if len(m.extraCursors) > 0 {
			return deleteAllCursorSelections(m)
		}
		m2, cmd := m.deleteSelection()
		return m2, cmd

	case "c":
		m = m.withClearedSearch()
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		if len(m.extraCursors) > 0 {
			m2, cmd := deleteAllCursorSelections(m)
			m2.mode = ModeInsert
			return m2, cmd
		}
		m2, cmd := m.deleteSelection()
		m2.mode = ModeInsert
		return m2, cmd

	case "y":
		if text := m.selectedText(); text != "" {
			if err := writeClipboard(text); err != nil {
				m.status = "clipboard: " + err.Error()
			} else {
				m.status = "copied"
			}
			m.sel = nil
		}

	// Movement — clears selection. All movements apply to every cursor.
	case "h", "left":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveCursor(0, -1)
		})
	case "l", "right":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveCursor(0, 1)
		})
	case "j", "down":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveCursor(1, 0)
		})
	case "k", "up":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveCursor(-1, 0)
		})
	case "ctrl+f", "pgdown":
		vis := m.visibleLines()
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveCursor(vis, 0)
		})
	case "ctrl+b", "pgup":
		vis := m.visibleLines()
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveCursor(-vis, 0)
		})
	case "G":
		last := max(0, m.buf.LineCount()-1)
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.cursor = document.Pos{Line: last, Col: 0}
			m.scrollToCursor()
		})
	case "0", "home":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.cursor.Col = 0
		})
	case "^":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			runes := []rune(m.buf.Line(m.cursor.Line))
			i := 0
			for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
				i++
			}
			m.cursor.Col = i
		})
	case "$", "end":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.cursor.Col = m.buf.LineLen(m.cursor.Line)
		})

	// Word/line navigation.
	case "w":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveToNextWordStart()
		})
	case "b":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveToPrevWordStart()
		})
	case "e":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.moveToWordEnd()
		})

	case "p":
		text, err := readClipboard()
		if err != nil {
			m.status = "clipboard: " + err.Error()
		} else if text != "" {
			op := document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: m.cursor.Line,
				InsertCol:  m.cursor.Col,
				InsertText: text,
			}
			return applyOp(m, op)
		}
	case "B":
		m.extendWordBackward()

	// Multi-cursor.
	case "ctrl+d":
		selectNextOccurrence(&m)

	case "C":
		m = m.withClearedSearch()
		addCursorBelow(&m)

	case "ctrl+/", "ctrl+_": // ctrl+/ and ctrl+_ are the same byte (0x1F) in most terminals
		return executeToggleComment(m)

	case "?":
		return m, m.fetchPluginBindings()

	case "alt+s":
		if m.sel == nil {
			m.status = "alt+s: select multiple lines first (x to select, X to extend)"
		} else if start, end := m.sel.ordered(); start.Line == end.Line {
			m.status = "alt+s: selection must span multiple lines"
		} else {
			_ = start
			_ = end
			splitSelectionIntoCursors(&m)
		}

	// Mark-based selection.
	case "z":
		pos := m.cursor
		m.mark = &pos
		m.status = "mark set"

	case "Z":
		if m.mark == nil {
			m.status = "no mark set (press z to set mark)"
		} else {
			m.sel = &Selection{Anchor: *m.mark, Head: m.cursor}
			m.status = ""
		}

	// Indent / unindent selected lines (or current line).
	case ">":
		return executeIndent(m)

	case "<":
		return executeUnindent(m)

	// Jump list navigation.
	case "-":
		return m, func() tea.Msg { return JumpBackMsg{} }

	case "=", "+":
		return m, func() tea.Msg { return JumpForwardMsg{} }

	default:
		// Check whether this key starts a prefix command sequence.
		if len(msg.Runes) > 0 {
			r := msg.Runes[0]
			for _, c := range prefixCmds {
				if c.key == r {
					m.prefixSeq = []rune{r}
					return m, nil
				}
			}
		}
	}
	return m, nil
}
