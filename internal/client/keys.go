package client

import (
	"context"
	"fmt"
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

// executeSelectInsideWord selects the full word enclosing the cursor.
func executeSelectInsideWord(m Model) (tea.Model, tea.Cmd) {
	runes := []rune(m.buf.Line(m.cursor.Line))
	if len(runes) == 0 {
		return m, nil
	}
	col := min(m.cursor.Col, len(runes)-1)
	if !isWordChar(runes[col]) {
		return m, nil
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
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.recoveryPrompt {
		return m.handleRecoveryPrompt(msg)
	}
	if m.warnQuit {
		return m.handleWarnQuit(msg)
	}
	// Any key dismisses hover popup.
	if m.hoverContent != nil {
		m.hoverContent = nil
		// Don't consume: let the key fall through to normal handling.
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
	}
	return m, nil
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
// Esc always cancels capture as a safety valve. Each key decrements the remaining
// count; when it hits zero, capture mode ends (unless the plugin's response requests more).
func (m Model) handleCapturedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		m.captureMode = false
		m.captureRemaining = 0
		m.decorations = nil
		return m, nil
	}
	if m.captureRemaining > 0 {
		m.captureRemaining--
	}
	if m.captureRemaining == 0 {
		m.captureMode = false
	}
	key := msg.String()
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.HandlePluginKey(ctx, key, "capture")
		if err != nil {
			return errorMsg{err}
		}
		return pluginKeyResultMsg{result: result}
	}
}

// handlePluginKeyRPC dispatches a keypress to the owning plugin and returns a
// cmd that will deliver the result as a pluginKeyResultMsg.
func (m Model) handlePluginKeyRPC(key string) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.HandlePluginKey(ctx, key, "normal")
		if err != nil {
			return errorMsg{err}
		}
		return pluginKeyResultMsg{result: result}
	}
}

func (m Model) handleNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

	case "esc":
		m.sel = nil

	// Enter insert mode — clear any selection and start an undo group.
	case "i":
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.mode = ModeInsert

	case "A":
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.mode = ModeInsert
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	case "o":
		m.sel = nil
		m.currentGroup = []document.Op{}
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
		m.sel = nil
		m.currentGroup = []document.Op{}
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

	case "u":
		if len(m.undoStack) > 0 {
			group := m.undoStack[len(m.undoStack)-1]
			m.undoStack = m.undoStack[:len(m.undoStack)-1]
			m.sel = nil
			var cmds []tea.Cmd
			var redoGroup []document.Op
			for i := len(group) - 1; i >= 0; i-- {
				inv := group[i]
				inv.ClientID = m.rpc.ClientID()
				reInv := inverseOp(m, inv) // compute re-inverse before applying
				m.buf.Apply(inv)
				cmds = append(cmds, m.sendToServer(inv))
				redoGroup = append(redoGroup, reInv)
			}
			m.redoStack = append(m.redoStack, redoGroup)
			if len(group) > 0 {
				first := group[0]
				if first.Type == document.OpDelete {
					m.cursor = document.Pos{Line: first.FromLine, Col: first.FromCol}
				} else {
					m.cursor = document.Pos{Line: first.InsertLine, Col: first.InsertCol}
				}
				m.scrollToCursor()
			}
			if len(m.undoStack) == m.savedUndoDepth {
				m.buf.SetClean()
			}
			return m, tea.Sequence(cmds...)
		}

	case "U":
		if len(m.redoStack) > 0 {
			group := m.redoStack[len(m.redoStack)-1]
			m.redoStack = m.redoStack[:len(m.redoStack)-1]
			m.sel = nil
			var cmds []tea.Cmd
			var newUndoGroup []document.Op
			for i := len(group) - 1; i >= 0; i-- {
				op := group[i]
				op.ClientID = m.rpc.ClientID()
				inv := inverseOp(m, op) // compute inverse before applying
				m.buf.Apply(op)
				cmds = append(cmds, m.sendToServer(op))
				newUndoGroup = append(newUndoGroup, inv)
			}
			m.undoStack = append(m.undoStack, newUndoGroup)
			if len(group) > 0 {
				first := group[len(group)-1] // first applied during redo
				if first.Type == document.OpInsert {
					m.cursor = document.Pos{Line: first.InsertLine, Col: first.InsertCol}
				} else {
					m.cursor = document.Pos{Line: first.FromLine, Col: first.FromCol}
				}
				m.scrollToCursor()
			}
			if len(m.undoStack) == m.savedUndoDepth {
				m.buf.SetClean()
			}
			return m, tea.Sequence(cmds...)
		}

	// Selection: w = next word, x = current line.
	case "w":
		m.selectWord()

	case "x":
		m.selectLine()

	// Operators: act on selection (no-op if nothing selected).
	case "d":
		m2, cmd := m.deleteSelection()
		return m2, cmd

	case "c":
		m.currentGroup = []document.Op{}
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

	// Movement — clears selection.
	case "h", "left":
		m.sel = nil
		m.moveCursor(0, -1)
	case "l", "right":
		m.sel = nil
		m.moveCursor(0, 1)
	case "j", "down":
		m.sel = nil
		m.moveCursor(1, 0)
	case "k", "up":
		m.sel = nil
		m.moveCursor(-1, 0)
	case "ctrl+f", "pgdown":
		m.sel = nil
		m.moveCursor(m.visibleLines(), 0)
	case "ctrl+b", "pgup":
		m.sel = nil
		m.moveCursor(-m.visibleLines(), 0)
	case "G":
		m.sel = nil
		last := max(0, m.buf.LineCount()-1)
		m.cursor = document.Pos{Line: last, Col: 0}
		m.scrollToCursor()
	case "0", "^", "home":
		m.sel = nil
		m.cursor.Col = 0
	case "$", "end":
		m.sel = nil
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

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
			m.undoStack = append(m.undoStack, m.currentGroup)
		}
		m.currentGroup = nil
		return m, nil

	case "ctrl+c":
		return m, m.doCloseBuffer()

	case "ctrl+s":
		return m, m.doSave()

	case "backspace":
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
	case "enter":
		return m.executeCommand()
	case "backspace":
		runes := []rune(m.cmdBuf)
		if len(runes) > 0 {
			m.cmdBuf = string(runes[:len(runes)-1])
		} else {
			m.mode = ModeNormal
		}
	default:
		if len(msg.Runes) > 0 {
			m.cmdBuf += string(msg.Runes)
		}
	}
	return m, nil
}

func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	cmd := strings.TrimSpace(m.cmdBuf)
	m.mode = ModeNormal
	m.cmdBuf = ""

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
		m.status = fmt.Sprintf("E: unknown command: %s", cmd)
	}
	return m, nil
}
