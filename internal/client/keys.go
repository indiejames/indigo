package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// command is a node in the prefix-command tree.
// Leaf nodes have execute set; branch nodes have children.
type command struct {
	key       rune
	label     string
	menuTitle string
	children  []command
	execute   func(m Model) (tea.Model, tea.Cmd)
}

// prefixCmds is the root of the prefix-command tree for Normal mode.
var prefixCmds = []command{
	{
		key:       'g',
		label:     "Go",
		menuTitle: "Go",
		children: []command{
			{key: 'g', label: "Go to top of file", execute: executeGoToTop},
			{key: 'e', label: "Go to end of file", execute: executeGoToEnd},
			{key: 'd', label: "Go to definition", execute: executeGoToDefinition},
			{key: 'h', label: "Go to line start", execute: executeGoToLineStart},
			{key: 'l', label: "Go to line end", execute: executeGoToLineEnd},
			{key: 's', label: "Go to first non-whitespace", execute: executeGoToFirstNonWS},
		},
	},
	{
		key:       ']',
		label:     "Next",
		menuTitle: "Next",
		children: []command{
			{key: 'b', label: "Next buffer", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return NextBufferMsg{} }
			}},
		},
	},
	{
		key:       '[',
		label:     "Prev",
		menuTitle: "Prev",
		children: []command{
			{key: 'b', label: "Previous buffer", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return PrevBufferMsg{} }
			}},
		},
	},
	{
		key:       'm',
		label:     "Match",
		menuTitle: "Match",
		children: []command{
			{key: 'm', label: "Go to matching bracket"},
			{
				key:       'i',
				label:     "Select inside object",
				menuTitle: "Match Inside",
				children: []command{
					{key: 'w', label: "Word", execute: executeSelectInsideWord},
					{key: 'm', label: "Closes surrounding pair"},
					{key: '.', label: "... or any character acting as a pair"},
				},
			},
		},
	},
}

// findCommand navigates prefixCmds following seq and returns the final node.
func findCommand(seq []rune) (*command, bool) {
	cmds := prefixCmds
	var found *command
	for _, r := range seq {
		matched := false
		for i := range cmds {
			if cmds[i].key == r {
				found = &cmds[i]
				cmds = cmds[i].children
				matched = true
				break
			}
		}
		if !matched {
			return nil, false
		}
	}
	return found, found != nil
}

func executeGoToTop(m Model) (tea.Model, tea.Cmd) {
	m.sel = nil
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.topLine = 0
	return m, nil
}

func executeGoToEnd(m Model) (tea.Model, tea.Cmd) {
	m.sel = nil
	last := max(0, m.buf.LineCount()-1)
	m.cursor = document.Pos{Line: last, Col: 0}
	m.scrollToCursor()
	return m, nil
}

func executeGoToDefinition(m Model) (tea.Model, tea.Cmd) {
	return m, m.fetchDefinition()
}

func executeGoToLineStart(m Model) (tea.Model, tea.Cmd) {
	m.sel = nil
	m.cursor.Col = 0
	return m, nil
}

func executeGoToLineEnd(m Model) (tea.Model, tea.Cmd) {
	m.sel = nil
	m.cursor.Col = m.buf.LineLen(m.cursor.Line)
	return m, nil
}

func executeGoToFirstNonWS(m Model) (tea.Model, tea.Cmd) {
	m.sel = nil
	runes := []rune(m.buf.Line(m.cursor.Line))
	i := 0
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	m.cursor.Col = i
	return m, nil
}

// executeSelectInsideWord selects the full word enclosing the cursor.
func executeSelectInsideWord(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		runes := []rune(m.buf.Line(m.cursor.Line))
		if len(runes) == 0 {
			return
		}
		col := min(m.cursor.Col, len(runes)-1)
		if !isWordChar(runes[col]) {
			return
		}
		start := col
		for start > 0 && isWordChar(runes[start-1]) {
			start--
		}
		end := col
		for end+1 < len(runes) && isWordChar(runes[end+1]) {
			end++
		}
		lineNum := m.cursor.Line
		m.sel = &Selection{
			Anchor: document.Pos{Line: lineNum, Col: start},
			Head:   document.Pos{Line: lineNum, Col: end},
		}
		m.cursor = m.sel.Head
		m.scrollToCursor()
	})
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.recoveryPrompt {
		return m.handleRecoveryPrompt(msg)
	}
	if m.warnQuit {
		return m.handleWarnQuit(msg)
	}
	// Escape dismisses the diagnostic popup and suppresses re-show until cursor leaves the range.
	// Always falls through so the mode transition (insert→normal) also happens.
	if m.diagPopup && msg.String() == "esc" {
		m.diagPopup = false
		m.diagPopupSuppressed = true
	}

	// Scroll keys navigate the hover popup; all other keys dismiss it.
	if m.hoverContent != nil {
		// contentH mirrors the calculation in renderHoverPopup.
		maxPopH := max(6, m.height-4)
		contentH := maxPopH - 2
		maxScroll := max(0, m.hoverTotalLines-contentH)
		switch msg.String() {
		case "j", "down":
			m.hoverScroll = min(m.hoverScroll+1, maxScroll)
			return m, nil
		case "k", "up":
			m.hoverScroll = max(0, m.hoverScroll-1)
			return m, nil
		case "ctrl+f":
			m.hoverScroll = min(m.hoverScroll+m.height/4, maxScroll)
			return m, nil
		case "ctrl+b":
			m.hoverScroll = max(0, m.hoverScroll-m.height/4)
			return m, nil
		case "esc":
			m.hoverContent = nil
			m.hoverScroll = 0
			return m, nil
		default:
			m.hoverContent = nil
			m.hoverScroll = 0
			// Don't consume: let the key fall through to normal handling.
		}
	}
	if m.saveAsInput != nil {
		return m.handleSaveAsDialog(msg)
	}
	// Clear transient error on any key.
	m.status = ""
	switch m.mode {
	case ModeNormal:
		return m.handleNormal(msg)
	case ModeInsert:
		return m.handleInsert(msg)
	case ModeCommand:
		return m.handleCommand(msg)
	case ModeSearch:
		return m.handleSearch(msg)
	}
	return m, nil
}

func (m Model) handleSaveAsDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.saveAsInput = nil
		m.saveAsThenClose = false
		return m, nil
	case "enter":
		input := strings.TrimSpace(*m.saveAsInput)
		m.saveAsInput = nil
		if input == "" {
			return m, nil
		}
		newPath, err := filepath.Abs(input)
		if err != nil {
			m.status = fmt.Sprintf("E: bad path: %v", err)
			m.saveAsThenClose = false
			return m, nil
		}
		return m, m.doSaveAsNow(newPath, m.saveAsThenClose)
	case "backspace", "ctrl+h":
		runes := []rune(*m.saveAsInput)
		if len(runes) > 0 {
			s := string(runes[:len(runes)-1])
			m.saveAsInput = &s
		}
		return m, nil
	default:
		// Append printable characters.
		if msg.Type == tea.KeyRunes {
			s := *m.saveAsInput + string(msg.Runes)
			m.saveAsInput = &s
		}
		return m, nil
	}
}

func (m Model) handleRecoveryPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		m.recoveryPrompt = false
	case "n", "N", "esc":
		m.recoveryPrompt = false
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			content, err := m.rpc.DiscardRecovery(ctx, m.bufID)
			if err != nil {
				return errorMsg{err}
			}
			return discardRecoveryMsg{content}
		}
	}
	return m, nil
}

func (m Model) handleWarnQuit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s", "ctrl+s":
		m.warnQuit = false
		return m, m.doSaveAndClose()
	case "q", "Q":
		m.warnQuit = false
		return m, m.doCloseBuffer()
	case "esc", "n", "N":
		m.warnQuit = false
	}
	return m, nil
}

// handleCapturedKey forwards a keypress to the plugin that requested capture mode.
// Esc always exits capture mode locally; it is still forwarded to the plugin so
// the plugin can clean up its own state. Each non-Esc key decrements the remaining
// count; when it hits zero, capture mode ends (unless the plugin's response re-enables it).
func (m Model) handleCapturedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	isEsc := key == "esc"

	if isEsc {
		m.captureMode = false
		m.captureRemaining = 0
	} else {
		if m.captureRemaining > 0 {
			m.captureRemaining--
		}
		if m.captureRemaining == 0 {
			m.captureMode = false
		}
	}

	bufID := m.bufID
	curLine := uint32(m.cursor.Line)
	curCol := uint32(m.cursor.Col)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.HandlePluginKey(ctx, key, "capture", bufID, curLine, curCol)
		if err != nil {
			if isEsc {
				return nil // suppress error on esc-cancel
			}
			return errorMsg{err}
		}
		return pluginKeyResultMsg{result: result}
	}
}

// handlePluginKeyRPC dispatches a keypress to the owning plugin and returns a
// cmd that will deliver the result as a pluginKeyResultMsg.
func (m Model) handlePluginKeyRPC(key string) (tea.Model, tea.Cmd) {
	bufID := m.bufID
	curLine := uint32(m.cursor.Line)
	curCol := uint32(m.cursor.Col)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.HandlePluginKey(ctx, key, "normal", bufID, curLine, curCol)
		if err != nil {
			return errorMsg{err}
		}
		return pluginKeyResultMsg{result: result}
	}
}

// handleFixPopup handles keyboard input while the fix popup is visible.
func (m Model) handleFixPopup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.fixItems = nil
		m.fixDecor = nil
		return m, nil
	case "j", "down":
		if m.fixIdx < len(m.fixItems)-1 {
			m.fixIdx++
		}
		return m, nil
	case "k", "up":
		if m.fixIdx > 0 {
			m.fixIdx--
		}
		return m, nil
	case "enter":
		idx := m.fixIdx
		cmd := m.applyFixCmd(idx)
		m.fixItems = nil
		m.fixDecor = nil
		return m, cmd
	}
	return m, nil
}

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
			if cmd, ok := findCommand(newSeq); ok {
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
		if m.buf.Dirty() && !m.checkingQuit {
			m.checkingQuit = true
			return m, m.fetchClientCount()
		}
		if !m.checkingQuit {
			return m, m.doCloseBuffer()
		}
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
		m = m.withClearedSearch()

	// Enter insert mode — clear any selection and start an undo group.
	case "i":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.mode = ModeInsert

	case "a":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.mode = ModeInsert
		m.cursor.Col++

	case "A":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.mode = ModeInsert
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	case "o":
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
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

	case "E":
		if len(m.diagsAtPos(m.cursor.Line, m.cursor.Col)) > 0 {
			m.diagPopup = !m.diagPopup
		} else {
			m.status = "No diagnostics on this line"
		}

	case "F":
		return m, m.fetchFixes()

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
			return m, tea.Sequence(append(cmds, m.reparseHighlight())...)
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
	case "w":
		m.applyToAllCursors(func(m *Model) {
			m.selectWord()
		})

	case "W":
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
	case "0", "^", "home":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.cursor.Col = 0
		})
	case "$", "end":
		m.applyToAllCursors(func(m *Model) {
			m.sel = nil
			m.cursor.Col = m.buf.LineLen(m.cursor.Line)
		})

	// Word/line navigation.
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
	case "B":
		m.extendWordBackward()

	// Multi-cursor.
	case "ctrl+d":
		selectNextOccurrence(&m)

	case "C":
		m = m.withClearedSearch()
		addCursorBelow(&m)

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
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: m.groupBefore})
		}
		m.currentGroup = nil
		return m, nil

	case "ctrl+c":
		return m, m.doCloseBuffer()

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

func (m Model) handleCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.cmdBuf = ""
		m.cmdCompletionIdx = -1

	case "tab", "down":
		matches := filteredCmds(m.cmdBuf)
		if len(matches) > 0 {
			if m.cmdCompletionIdx < 0 {
				m.cmdCompletionIdx = 0
			} else {
				m.cmdCompletionIdx = (m.cmdCompletionIdx + 1) % len(matches)
			}
		}

	case "shift+tab", "up":
		matches := filteredCmds(m.cmdBuf)
		if len(matches) > 0 {
			if m.cmdCompletionIdx < 0 {
				m.cmdCompletionIdx = len(matches) - 1
			} else {
				m.cmdCompletionIdx = (m.cmdCompletionIdx - 1 + len(matches)) % len(matches)
			}
		}

	case "enter":
		// If a completion item is selected, act on it.
		if m.cmdCompletionIdx >= 0 {
			matches := filteredCmds(m.cmdBuf)
			if m.cmdCompletionIdx < len(matches) {
				sel := matches[m.cmdCompletionIdx]
				m.cmdCompletionIdx = -1
				if sel.needsArgs {
					// Fill command name and stay in command mode for argument input.
					m.cmdBuf = sel.name + " "
					return m, nil
				}
				m.cmdBuf = sel.name
				return m.executeCommand()
			}
		}
		return m.executeCommand()

	case "backspace":
		runes := []rune(m.cmdBuf)
		if len(runes) > 0 {
			m.cmdBuf = string(runes[:len(runes)-1])
			m.cmdCompletionIdx = -1
		} else {
			m.mode = ModeNormal
			m.cmdCompletionIdx = -1
		}

	default:
		if len(msg.Runes) > 0 {
			m.cmdBuf += string(msg.Runes)
			m.cmdCompletionIdx = -1 // reset selection whenever the filter changes
		}
	}
	return m, nil
}

func (m Model) handleSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.cursor = m.searchOrigin
		m.scrollToCursor()
		m.searchQuery = ""
		m.searchMatches = nil
		m.searchIdx = -1
		m.searchErr = ""
	case "enter":
		m.mode = ModeNormal
		m.searchErr = ""
		if len(m.searchMatches) > 0 && m.searchIdx >= 0 {
			sm := m.searchMatches[m.searchIdx]
			m.cursor = document.Pos{Line: sm.line, Col: sm.col}
			m.scrollToCursor()
		}
	case "backspace":
		runes := []rune(m.searchQuery)
		if len(runes) > 0 {
			m.searchQuery = string(runes[:len(runes)-1])
			m.updateSearch()
		} else {
			m.mode = ModeNormal
			m.cursor = m.searchOrigin
			m.scrollToCursor()
			m.searchMatches = nil
			m.searchIdx = -1
			m.searchErr = ""
		}
	default:
		if len(msg.Runes) > 0 {
			m.searchQuery += string(msg.Runes)
			m.updateSearch()
		}
	}
	return m, nil
}

// updateSearch recomputes matches for the current searchQuery and advances the
// cursor to the first match at or after searchOrigin.
func (m *Model) updateSearch() {
	matches, err := findMatches(m.buf, m.searchQuery)
	if err != nil {
		m.searchErr = err.Error()
		m.searchMatches = nil
		m.searchIdx = -1
		return
	}
	m.searchErr = ""
	m.searchMatches = matches
	m.searchIdx = matchIdxAtOrAfter(matches, m.searchOrigin.Line, m.searchOrigin.Col)
	if m.searchIdx >= 0 {
		sm := matches[m.searchIdx]
		m.cursor = document.Pos{Line: sm.line, Col: sm.col}
		m.scrollToCursor()
	}
}

func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	cmd := strings.TrimSpace(m.cmdBuf)
	m.mode = ModeNormal
	m.cmdBuf = ""

	// :grep/:find [pattern] [glob] — workspace search; falls back to current search query.
	// The optional trailing token is treated as a file glob if it contains *, ?, [ or ends with /.
	var searchRest string
	var doSearch bool
	if rest, ok := strings.CutPrefix(cmd, "grep"); ok {
		searchRest, doSearch = rest, true
	} else if rest, ok := strings.CutPrefix(cmd, "find"); ok {
		searchRest, doSearch = rest, true
	}
	if doSearch {
		pattern, glob := parseGrepArgs(strings.TrimSpace(searchRest))
		if pattern == "" {
			if m.searchQuery != "" {
				pattern = m.searchQuery
			} else {
				// e.g. ":grep *.go" with no prior query — treat glob as literal pattern.
				pattern, glob = glob, ""
			}
		}
		return m, func() tea.Msg { return GrepMsg{Pattern: pattern, Glob: glob} }
	}

	// Bare number → go to line.
	if n, err := strconv.Atoi(cmd); err == nil {
		lc := m.displayLineCount()
		if n < 1 || n > lc {
			m.status = fmt.Sprintf("E: line %d out of range (1-%d)", n, lc)
			return m, nil
		}
		m.cursor = document.Pos{Line: n - 1, Col: 0}
		m.scrollToCursor()
		return m, nil
	}

	switch cmd {
	case "fmt", "format":
		return m, m.fetchFormat(false)
	case "w", "write", "s", "save":
		return m, m.doSave()
	case "q", "quit":
		if m.buf.Dirty() {
			m.status = "E: unsaved changes (use :q! to discard)"
			return m, nil
		}
		return m, m.doCloseBuffer()
	case "q!", "quit!":
		return m, m.doCloseBuffer()
	case "wq", "x", "write-quit":
		return m, m.doSaveAndClose()
	case "qa", "quit-all":
		return m, func() tea.Msg { return QuitAllMsg{} }
	case "qa!", "quit-all!":
		return m, func() tea.Msg { return QuitAllMsg{Force: true} }
	case "wqa":
		return m, func() tea.Msg { return QuitAllMsg{SaveAll: true} }
	case "e", "edit":
		return m, func() tea.Msg { return OpenPickerMsg{} }
	case "metrics":
		if m.metrics != nil {
			m.metrics.show = !m.metrics.show
		}
	default:
		if path, ok := strings.CutPrefix(cmd, "w "); ok && strings.TrimSpace(path) != "" {
			newPath, err := filepath.Abs(strings.TrimSpace(path))
			if err != nil {
				m.status = fmt.Sprintf("E: bad path: %v", err)
				return m, nil
			}
			return m, m.doSaveAsNow(newPath, false)
		}
		if path, ok := strings.CutPrefix(cmd, "wq "); ok && strings.TrimSpace(path) != "" {
			newPath, err := filepath.Abs(strings.TrimSpace(path))
			if err != nil {
				m.status = fmt.Sprintf("E: bad path: %v", err)
				return m, nil
			}
			return m, m.doSaveAsNow(newPath, true)
		}
		m.status = fmt.Sprintf("E: unknown command: %s", cmd)
	}
	return m, nil
}

// withClearedSearch resets all within-buffer search state, removing match
// highlights and disabling n/N navigation until the next search.
func (m Model) withClearedSearch() Model {
	m.searchQuery = ""
	m.searchMatches = nil
	m.searchIdx = -1
	m.searchErr = ""
	return m
}

// parseGrepArgs splits a grep command argument into a pattern and an optional
// file glob. The trailing token is treated as a glob when it contains *, ?, [
// or ends with / (directory filter). Everything else is the search pattern.
func parseGrepArgs(s string) (pattern, glob string) {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		last := s[i+1:]
		if strings.ContainsAny(last, "*?[") || strings.HasSuffix(last, "/") {
			return strings.TrimSpace(s[:i]), last
		}
	}
	return s, ""
}
