package client

import (
	"strings"

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
		newSeq := append(append([]string(nil), m.prefixSeq...), msg.String())
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
		m.prefixSeq = nil
		return m, nil
	}

	// Let plugins handle keys they registered before built-in commands.
	if m.rpc != nil && m.rpc.HasPluginKey(msg.String()) {
		return m.handlePluginKeyRPC(msg.String())
	}

	// A single keypress that matches a root-level command tree entry either
	// fires immediately (leaf) or starts a prefix sequence (branch), taking
	// priority over the switch below so remapped/config-driven bindings and
	// hardcoded ones share one dispatch path.
	if cmd, ok := findIn(prefixCmds, []string{msg.String()}); ok {
		if cmd.execute != nil {
			return cmd.execute(m)
		}
		if len(cmd.children) > 0 {
			m.prefixSeq = []string{msg.String()}
			return m, nil
		}
	}

	return m, nil
}

func executeCancelHint(m Model) (tea.Model, tea.Cmd) {
	m = m.pushStatus("Use :q to quit")
	return m, nil
}

func executeEnterCommandMode(m Model) (tea.Model, tea.Cmd) {
	m.mode = ModeCommand
	m.cmdBuf = ""
	m.cmdCompletionIdx = -1
	return m, nil
}

func executeEnterSearchMode(m Model) (tea.Model, tea.Cmd) {
	m.searchQuery = ""
	m.searchReplace = ""
	m.searchReplacing = false
	m.searchMatches = nil
	m.searchIdx = -1
	m.searchOrigin = m.cursor
	m.mode = ModeSearch
	return m, nil
}

func executeEscNormal(m Model) (tea.Model, tea.Cmd) {
	m.extraCursors = nil
	m.sel = nil
	m.mark = nil
	m = m.withClearedSearch()
	return m, nil
}

func executeAppendAfterCursor(m Model) (tea.Model, tea.Cmd) {
	m = m.withClearedSearch()
	m.sel = nil
	m.currentGroup = []document.Op{}
	m.groupBefore = m.cursorSnap()
	m.insertLineCount = m.buf.LineCount()
	m.mode = ModeInsert
	m.cursor.Col++
	return m, nil
}

func executeAppendLineEnd(m Model) (tea.Model, tea.Cmd) {
	m = m.withClearedSearch()
	m.sel = nil
	m.currentGroup = []document.Op{}
	m.groupBefore = m.cursorSnap()
	m.insertLineCount = m.buf.LineCount()
	m.mode = ModeInsert
	m.cursor.Col = m.buf.LineLen(m.cursor.Line)
	return m, nil
}

func executeOpenLineBelow(m Model) (tea.Model, tea.Cmd) {
	m = m.withClearedSearch()
	m.sel = nil
	m.currentGroup = []document.Op{}
	m.groupBefore = m.cursorSnap()
	m.insertLineCount = m.buf.LineCount()
	m.mode = ModeInsert
	line := m.cursor.Line
	lineEnd := m.buf.LineLen(line)
	indent := m.contextIndent(m.buf.Line(line), line, lineEnd)
	op := document.Op{
		ClientID:   m.rpc.ClientID(),
		Type:       document.OpInsert,
		InsertLine: line,
		InsertCol:  lineEnd,
		InsertText: "\n" + indent,
	}
	m.cursor = document.Pos{Line: line + 1, Col: len(indent)}
	return applyOp(m, op)
}

func executeOpenLineAbove(m Model) (tea.Model, tea.Cmd) {
	m = m.withClearedSearch()
	m.sel = nil
	m.currentGroup = []document.Op{}
	m.groupBefore = m.cursorSnap()
	m.insertLineCount = m.buf.LineCount()
	m.mode = ModeInsert
	prevLineNum := m.cursor.Line - 1
	col := 0
	indent := ""
	if prevLineNum >= 0 {
		col = m.buf.LineLen(prevLineNum)
		indent = m.contextIndent(m.buf.Line(prevLineNum), prevLineNum, col)
	}
	op := document.Op{
		ClientID:   m.rpc.ClientID(),
		Type:       document.OpInsert,
		InsertLine: max(0, prevLineNum),
		InsertCol:  col,
		InsertText: "\n" + indent,
	}
	m.cursor = document.Pos{Line: m.cursor.Line, Col: len(indent)}
	return applyOp(m, op)
}

func executeSave(m Model) (tea.Model, tea.Cmd) {
	return m, m.doSave()
}

func executeHover(m Model) (tea.Model, tea.Cmd) {
	return m, m.fetchHover()
}

func executeToggleDiagPopup(m Model) (tea.Model, tea.Cmd) {
	if len(m.diagsAtPos(m.cursor.Line, m.cursor.Col)) > 0 {
		m.diagPopup = !m.diagPopup
	} else {
		m = m.pushStatus("No diagnostics on this line")
	}
	return m, nil
}

func executeUndo(m Model) (tea.Model, tea.Cmd) {
	if len(m.undoStack) == 0 {
		return m, nil
	}
	entry := m.undoStack[len(m.undoStack)-1]
	m.undoStack = m.undoStack[:len(m.undoStack)-1]
	// Snapshot current (post-edit) state so redo can restore it.
	redoEntry := undoEntry{before: m.cursorSnap()}
	var cmds []tea.Cmd
	for i := len(entry.ops) - 1; i >= 0; i-- {
		inv := entry.ops[i]
		inv.ClientID = m.rpc.ClientID()
		reInv := inverseOp(m, inv) // compute re-inverse before applying
		al, d := opLineDelta(inv)
		m = m.shiftLSPOverlayLines(al, d)
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
	var refreshCmd tea.Cmd
	m, refreshCmd = m.scheduleLSPOverlayRefresh()
	cmds = append(cmds, refreshCmd)
	return m, tea.Sequence(append(cmds, m.reparseHighlight(), undoCmd)...)
}

func executeRedo(m Model) (tea.Model, tea.Cmd) {
	if len(m.redoStack) == 0 {
		return m, nil
	}
	entry := m.redoStack[len(m.redoStack)-1]
	m.redoStack = m.redoStack[:len(m.redoStack)-1]
	// Snapshot current (pre-edit) state so undo can restore it.
	newUndoEntry := undoEntry{before: m.cursorSnap()}
	var cmds []tea.Cmd
	for i := len(entry.ops) - 1; i >= 0; i-- {
		op := entry.ops[i]
		op.ClientID = m.rpc.ClientID()
		inv := inverseOp(m, op) // compute inverse before applying
		al, d := opLineDelta(op)
		m = m.shiftLSPOverlayLines(al, d)
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
	var refreshCmd tea.Cmd
	m, refreshCmd = m.scheduleLSPOverlayRefresh()
	cmds = append(cmds, refreshCmd)
	return m, tea.Sequence(append(cmds, m.reparseHighlight())...)
}

func executeExtendNextWordStart(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.extendToNextWordStart()
	})
	return m, nil
}

func executeExtendWordForward(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.extendWordForward()
	})
	return m, nil
}

func executeSelectLine(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.selectLine()
	})
	return m, nil
}

func executeExtendLineBackward(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.extendLineBackward()
	})
	return m, nil
}

func executeSelectAll(m Model) (tea.Model, tea.Cmd) {
	m.selectAll() // selects whole buffer; primary only makes sense here
	return m, nil
}

func executeClearSelections(m Model) (tea.Model, tea.Cmd) {
	m.sel = nil
	for i := range m.extraCursors {
		m.extraCursors[i].sel = nil
	}
	return m, nil
}

func executeFlipSelection(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.flipSelection()
	})
	return m, nil
}

func executeDeleteSelection(m Model) (tea.Model, tea.Cmd) {
	m = m.withClearedSearch()
	if len(m.extraCursors) > 0 {
		return deleteAllCursorSelections(m)
	}
	m2, cmd := m.deleteSelection()
	return m2, cmd
}

func executeChangeSelection(m Model) (tea.Model, tea.Cmd) {
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
}

func executeYank(m Model) (tea.Model, tea.Cmd) {
	if text := m.selectedText(); text != "" {
		if err := writeClipboard(text); err != nil {
			m = m.pushStatus("clipboard: " + err.Error())
		} else {
			m = m.pushStatus("copied")
		}
		m.sel = nil
	}
	return m, nil
}

func executeCursorLeft(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveCursorChar(-1)
	})
	return m, nil
}

func executeCursorRight(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveCursorChar(1)
	})
	return m, nil
}

func executeCursorDown(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveCursor(1, 0)
	})
	return m, nil
}

func executeCursorUp(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveCursor(-1, 0)
	})
	return m, nil
}

func executePageDown(m Model) (tea.Model, tea.Cmd) {
	vis := m.visibleLines()
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveCursor(vis, 0)
	})
	return m, nil
}

func executePageUp(m Model) (tea.Model, tea.Cmd) {
	vis := m.visibleLines()
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveCursor(-vis, 0)
	})
	return m, nil
}

func executeGoToLastLine(m Model) (tea.Model, tea.Cmd) {
	last := max(0, m.buf.LineCount()-1)
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.cursor = document.Pos{Line: last, Col: 0}
		m.scrollToCursor()
		m.scrollToShowLineTail(last)
	})
	return m, nil
}

func executeFirstNonBlank(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		runes := []rune(m.buf.Line(m.cursor.Line))
		i := 0
		for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
			i++
		}
		m.cursor.Col = i
	})
	return m, nil
}

func executeNextWordStart(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveToNextWordStart()
	})
	return m, nil
}

func executePrevWordStart(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveToPrevWordStart()
	})
	return m, nil
}

func executeWordEnd(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.moveToWordEnd()
	})
	return m, nil
}

func executePaste(m Model) (tea.Model, tea.Cmd) {
	text, err := readClipboard()
	if err != nil {
		m = m.pushStatus("clipboard: " + err.Error())
		return m, nil
	}
	if strings.Contains(text, "\n") {
		return m.insertPastedText(text)
	}
	if text != "" {
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: text,
		}
		return applyOp(m, op)
	}
	return m, nil
}

func executeExtendWordBackward(m Model) (tea.Model, tea.Cmd) {
	m.extendWordBackward()
	return m, nil
}

func executeSelectNextOccurrence(m Model) (tea.Model, tea.Cmd) {
	selectNextOccurrence(&m)
	return m, nil
}

func executeAddCursorBelow(m Model) (tea.Model, tea.Cmd) {
	m = m.withClearedSearch()
	addCursorBelow(&m)
	return m, nil
}

func executeFetchPluginBindings(m Model) (tea.Model, tea.Cmd) {
	return m, m.fetchPluginBindings()
}

func executeSplitSelectionIntoCursors(m Model) (tea.Model, tea.Cmd) {
	if m.sel == nil {
		m = m.pushStatus("alt+s: select multiple lines first (x to select, X to extend)")
		return m, nil
	}
	if start, end := m.sel.ordered(); start.Line == end.Line {
		m = m.pushStatus("alt+s: selection must span multiple lines")
		return m, nil
	}
	splitSelectionIntoCursors(&m)
	return m, nil
}

func executeSetMark(m Model) (tea.Model, tea.Cmd) {
	pos := m.cursor
	m.mark = &pos
	m = m.pushStatus("mark set")
	return m, nil
}

func executeSelectToMark(m Model) (tea.Model, tea.Cmd) {
	if m.mark == nil {
		m = m.pushStatus("no mark set (press z to set mark)")
		return m, nil
	}
	m.sel = &Selection{Anchor: *m.mark, Head: m.cursor}
	m = m.pushStatus("")
	return m, nil
}

func executeJumpBack(m Model) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return JumpBackMsg{} }
}

func executeJumpForward(m Model) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return JumpForwardMsg{} }
}
