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
	"github.com/indiejames/indigo/internal/highlight"
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
			{key: 'b', label: "Open buffer picker", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return OpenBufPickerMsg{} }
			}},
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
			{key: 'm', label: "Go to matching bracket", execute: executeGotoMatchingBracket},
			{
				key:       'i',
				label:     "Select inside object",
				menuTitle: "Match Inside",
				children: []command{
					{key: 'w', label: "Word", execute: executeSelectInsideWord},
					{key: 'm', label: "Closest surrounding pair", execute: executeSelectInsideBrackets},
					{key: '.', label: "Quote/delimiter pair", execute: executeSelectInsideChar},
					{key: 'f', label: "Function", execute: executeSelectInsideFunction},
					{key: 't', label: "Type definition", execute: executeSelectInsideType},
					{key: 'a', label: "Argument/parameter", execute: executeSelectInsideArgument},
					{key: 'c', label: "Comment", execute: executeSelectInsideComment},
				},
			},
			{
				key:       'a',
				label:     "Select around object",
				menuTitle: "Match Around",
				children: []command{
					{key: 'w', label: "Word", execute: executeSelectInsideWord},
					{key: 'm', label: "Closest surrounding pair", execute: executeSelectAroundBrackets},
					{key: '.', label: "Quote/delimiter pair", execute: executeSelectAroundChar},
					{key: 'f', label: "Function", execute: executeSelectAroundFunction},
					{key: 't', label: "Type definition", execute: executeSelectAroundType},
					{key: 'a', label: "Argument/parameter", execute: executeSelectAroundArgument},
					{key: 'c', label: "Comment", execute: executeSelectAroundComment},
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

var openBrackets = map[rune]rune{'(': ')', '[': ']', '{': '}'}
var closeBrackets = map[rune]rune{')': '(', ']': '[', '}': '{'}

// scanMatchingClose scans forward from (line, col+1) for the close bracket that
// balances the open bracket at col, handling nesting.
func scanMatchingClose(m Model, line, col int, open, close rune) (int, int, bool) {
	depth := 1
	for ln := line; ln < m.buf.LineCount(); ln++ {
		runes := []rune(m.buf.Line(ln))
		start := 0
		if ln == line {
			start = col + 1
		}
		for c := start; c < len(runes); c++ {
			switch runes[c] {
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return ln, c, true
				}
			}
		}
	}
	return 0, 0, false
}

// scanMatchingOpen scans backward from (line, col-1) for the open bracket that
// balances the close bracket at col, handling nesting.
func scanMatchingOpen(m Model, line, col int, open, close rune) (int, int, bool) {
	depth := 1
	for ln := line; ln >= 0; ln-- {
		runes := []rune(m.buf.Line(ln))
		end := len(runes) - 1
		if ln == line {
			end = col - 1
		}
		for c := end; c >= 0; c-- {
			switch runes[c] {
			case close:
				depth++
			case open:
				depth--
				if depth == 0 {
					return ln, c, true
				}
			}
		}
	}
	return 0, 0, false
}

// executeGotoMatchingBracket moves the cursor to the bracket matching the one
// at (or nearest ahead of) the cursor position.
func executeGotoMatchingBracket(m Model) (tea.Model, tea.Cmd) {
	ln := m.cursor.Line
	runes := []rune(m.buf.Line(ln))
	if len(runes) == 0 {
		return m, nil
	}
	col := min(m.cursor.Col, len(runes)-1)
	ch := runes[col]

	// Not on a bracket — scan forward on the current line for the first one.
	if _, isOpen := openBrackets[ch]; !isOpen {
		if _, isClose := closeBrackets[ch]; !isClose {
			found := false
			for c := col + 1; c < len(runes); c++ {
				r := runes[c]
				if _, ok := openBrackets[r]; ok {
					col, ch, found = c, r, true
					break
				}
				if _, ok := closeBrackets[r]; ok {
					col, ch, found = c, r, true
					break
				}
			}
			if !found {
				return m, nil
			}
		}
	}

	var matchLn, matchCol int
	var ok bool
	if close, isOpen := openBrackets[ch]; isOpen {
		matchLn, matchCol, ok = scanMatchingClose(m, ln, col, ch, close)
	} else if open, isClose := closeBrackets[ch]; isClose {
		matchLn, matchCol, ok = scanMatchingOpen(m, ln, col, open, ch)
	}
	if ok {
		m.sel = nil
		m.cursor = document.Pos{Line: matchLn, Col: matchCol}
		m.scrollToCursor()
	}
	return m, nil
}

// executeSelectInsideBrackets finds the innermost bracket pair surrounding the
// cursor and selects the content between the brackets.
func executeSelectInsideBrackets(m Model) (tea.Model, tea.Cmd) {
	ln := m.cursor.Line
	col := m.cursor.Col

	// Scan backward to find the nearest unmatched opening bracket.
	nestDepth := map[rune]int{}
	openLn, openCol := -1, -1
	var openCh, closeCh rune
	found := false

searchBack:
	for l := ln; l >= 0; l-- {
		runes := []rune(m.buf.Line(l))
		end := len(runes) - 1
		if l == ln {
			end = min(col, len(runes)-1)
		}
		for c := end; c >= 0; c-- {
			r := runes[c]
			if open, isClose := closeBrackets[r]; isClose {
				nestDepth[open]++
			} else if close, isOpen := openBrackets[r]; isOpen {
				if nestDepth[r] > 0 {
					nestDepth[r]--
				} else {
					openLn, openCol = l, c
					openCh, closeCh = r, close
					found = true
					break searchBack
				}
			}
		}
	}

	if !found {
		return m, nil
	}
	closeLn, closeCol, ok := scanMatchingClose(m, openLn, openCol, openCh, closeCh)
	if !ok {
		return m, nil
	}

	// Select content between the brackets (exclusive of the brackets themselves).
	startLn, startCol := openLn, openCol+1
	endLn, endCol := closeLn, closeCol-1
	// When the closing bracket is at col 0, the selection ends at the last char
	// of the previous line rather than col -1 (which would panic scrollToCursor).
	if endCol < 0 {
		if endLn == 0 {
			return m, nil
		}
		endLn--
		endCol = max(0, len([]rune(m.buf.Line(endLn)))-1)
	}
	if endLn < startLn || (endLn == startLn && endCol < startCol) {
		return m, nil
	}
	m.sel = &Selection{
		Anchor: document.Pos{Line: startLn, Col: startCol},
		Head:   document.Pos{Line: endLn, Col: endCol},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
	return m, nil
}

// executeSelectInsideChar finds the tightest surrounding pair of identical
// delimiter characters (", ', `) on the current line and selects between them.
func executeSelectInsideChar(m Model) (tea.Model, tea.Cmd) {
	ln := m.cursor.Line
	col := m.cursor.Col
	runes := []rune(m.buf.Line(ln))

	bestStart, bestEnd := -1, -1
	for _, ch := range []rune{'"', '\'', '`'} {
		var positions []int
		for i, r := range runes {
			if r == ch {
				positions = append(positions, i)
			}
		}
		for i := 0; i+1 < len(positions); i += 2 {
			s, e := positions[i], positions[i+1]
			if s <= col && col <= e {
				if bestStart == -1 || (e-s) < (bestEnd-bestStart) {
					bestStart, bestEnd = s, e
				}
			}
		}
	}

	if bestStart == -1 || bestEnd <= bestStart+1 {
		return m, nil
	}
	m.sel = &Selection{
		Anchor: document.Pos{Line: ln, Col: bestStart + 1},
		Head:   document.Pos{Line: ln, Col: bestEnd - 1},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
	return m, nil
}

// applyTextObject converts a highlight.TextObject into a selection, clamping EndCol
// to the actual line length when the sentinel math.MaxInt is used.
func applyTextObject(m Model, to highlight.TextObject) (tea.Model, tea.Cmd) {
	endCol := to.EndCol
	if endCol > m.buf.LineLen(to.EndLine) {
		endCol = max(0, m.buf.LineLen(to.EndLine)-1)
	}
	m.sel = &Selection{
		Anchor: document.Pos{Line: to.StartLine, Col: to.StartCol},
		Head:   document.Pos{Line: to.EndLine, Col: endCol},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
	return m, nil
}

func executeSelectInsideFunction(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAt([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "function")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

func executeSelectInsideType(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAt([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "type")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

func executeSelectInsideArgument(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAt([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "argument")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

func executeSelectInsideComment(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAt([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "comment")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

// --- match around ---

func executeSelectAroundBrackets(m Model) (tea.Model, tea.Cmd) {
	ln := m.cursor.Line
	col := m.cursor.Col

	// Scan backward to find the nearest unmatched opening bracket (same as inside).
	nestDepth := map[rune]int{}
	openLn, openCol := -1, -1
	var openCh, closeCh rune
	found := false

searchBackAround:
	for l := ln; l >= 0; l-- {
		runes := []rune(m.buf.Line(l))
		end := len(runes) - 1
		if l == ln {
			end = min(col, len(runes)-1)
		}
		for c := end; c >= 0; c-- {
			r := runes[c]
			if open, isClose := closeBrackets[r]; isClose {
				nestDepth[open]++
			} else if close, isOpen := openBrackets[r]; isOpen {
				if nestDepth[r] > 0 {
					nestDepth[r]--
				} else {
					openLn, openCol = l, c
					openCh, closeCh = r, close
					found = true
					break searchBackAround
				}
			}
		}
	}
	if !found {
		return m, nil
	}
	closeLn, closeCol, ok := scanMatchingClose(m, openLn, openCol, openCh, closeCh)
	if !ok {
		return m, nil
	}
	// Around: include the brackets themselves.
	m.sel = &Selection{
		Anchor: document.Pos{Line: openLn, Col: openCol},
		Head:   document.Pos{Line: closeLn, Col: closeCol},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
	return m, nil
}

func executeSelectAroundChar(m Model) (tea.Model, tea.Cmd) {
	ln := m.cursor.Line
	col := m.cursor.Col
	runes := []rune(m.buf.Line(ln))

	bestStart, bestEnd := -1, -1
	for _, ch := range []rune{'"', '\'', '`'} {
		var positions []int
		for i, r := range runes {
			if r == ch {
				positions = append(positions, i)
			}
		}
		for i := 0; i+1 < len(positions); i += 2 {
			s, e := positions[i], positions[i+1]
			if s <= col && col <= e {
				if bestStart == -1 || (e-s) < (bestEnd-bestStart) {
					bestStart, bestEnd = s, e
				}
			}
		}
	}
	if bestStart == -1 {
		return m, nil
	}
	// Around: include the delimiter characters.
	m.sel = &Selection{
		Anchor: document.Pos{Line: ln, Col: bestStart},
		Head:   document.Pos{Line: ln, Col: bestEnd},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
	return m, nil
}

func executeSelectAroundFunction(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAround([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "function")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

func executeSelectAroundType(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAround([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "type")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

func executeSelectAroundArgument(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAround([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "argument")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

func executeSelectAroundComment(m Model) (tea.Model, tea.Cmd) {
	to, ok := m.hlr.TextObjectAround([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "comment")
	if !ok {
		return m, nil
	}
	return applyTextObject(m, to)
}

// --- comment toggle ---

func leadingWhitespace(runes []rune) int {
	i := 0
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	return i
}

// executeToggleComment comments or uncomments the current line (or selection).
// All lines already commented → uncomment; otherwise → comment all.
func executeToggleComment(m Model) (tea.Model, tea.Cmd) {
	prefix := highlight.LineCommentPrefix(m.filePath)
	prefixRunes := []rune(prefix)

	startLine, endLine := m.cursor.Line, m.cursor.Line
	if m.sel != nil {
		a, h := m.sel.Anchor, m.sel.Head
		if a.Line <= h.Line {
			startLine, endLine = a.Line, h.Line
		} else {
			startLine, endLine = h.Line, a.Line
		}
	}

	// Determine whether every line in range starts with the comment prefix.
	allCommented := true
outer:
	for ln := startLine; ln <= endLine; ln++ {
		runes := []rune(m.buf.Line(ln))
		indent := leadingWhitespace(runes)
		rest := runes[indent:]
		if len(rest) < len(prefixRunes) {
			allCommented = false
			break outer
		}
		for i, r := range prefixRunes {
			if rest[i] != r {
				allCommented = false
				break outer
			}
		}
	}

	ops := make([]document.Op, 0, endLine-startLine+1)
	if allCommented {
		for ln := startLine; ln <= endLine; ln++ {
			runes := []rune(m.buf.Line(ln))
			indent := leadingWhitespace(runes)
			deleteLen := len(prefixRunes)
			// Also remove the single space that was added after the prefix.
			if indent+deleteLen < len(runes) && runes[indent+deleteLen] == ' ' {
				deleteLen++
			}
			ops = append(ops, document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: ln, FromCol: indent,
				ToLine:   ln, ToCol: indent + deleteLen,
			})
		}
	} else {
		for ln := startLine; ln <= endLine; ln++ {
			runes := []rune(m.buf.Line(ln))
			indent := leadingWhitespace(runes)
			ops = append(ops, document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: ln,
				InsertCol:  indent,
				InsertText: prefix + " ",
			})
		}
	}

	m, cmd := applyBatch(m, ops)
	m.sel = nil
	return m, cmd
}

// selectionLineRange returns the first and last line covered by the current
// selection, falling back to the cursor line when there is no selection.
func (m Model) selectionLineRange() (startLine, endLine int) {
	if m.sel == nil {
		return m.cursor.Line, m.cursor.Line
	}
	start, end := m.sel.ordered()
	return start.Line, end.Line
}

// executeIndent adds one tab stop at the start of every selected line (or the
// cursor line). The selection is preserved as a line range so the user can
// press > repeatedly without re-selecting.
func executeIndent(m Model) (tea.Model, tea.Cmd) {
	startLine, endLine := m.selectionLineRange()
	ops := make([]document.Op, 0, endLine-startLine+1)
	for ln := startLine; ln <= endLine; ln++ {
		ops = append(ops, document.Op{
			ClientID:   m.clientID(),
			Type:       document.OpInsert,
			InsertLine: ln,
			InsertCol:  0,
			InsertText: "\t",
		})
	}
	m, cmd := applyBatch(m, ops)
	m.sel = nil
	return m, cmd
}

// executeUnindent removes one tab stop from the start of every selected line
// (or the cursor line): one '\t', or up to four leading spaces.
// The selection is preserved as a line range so repeated < keeps working.
func executeUnindent(m Model) (tea.Model, tea.Cmd) {
	startLine, endLine := m.selectionLineRange()
	ops := make([]document.Op, 0, endLine-startLine+1)
	for ln := startLine; ln <= endLine; ln++ {
		runes := []rune(m.buf.Line(ln))
		if len(runes) == 0 {
			continue
		}
		var remove int
		if runes[0] == '\t' {
			remove = 1
		} else {
			for remove < len(runes) && runes[remove] == ' ' && remove < 4 {
				remove++
			}
		}
		if remove == 0 {
			continue
		}
		ops = append(ops, document.Op{
			ClientID: m.clientID(),
			Type:     document.OpDelete,
			FromLine: ln, FromCol: 0,
			ToLine:   ln, ToCol: remove,
		})
	}
	if len(ops) == 0 {
		return m, nil
	}
	m, cmd := applyBatch(m, ops)
	m.sel = nil
	return m, cmd
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

	// Scroll keys navigate the help popup; q/esc/? dismiss it.
	if m.helpVisible {
		helpLines := helpPopupLines(m.pluginBindings)
		maxPopH := max(6, m.height-5) // matches vis-4 in View() where vis = m.height-1
		contentH := maxPopH - 2
		maxScroll := max(0, len(helpLines)-contentH)
		switch msg.String() {
		case "j", "down":
			m.helpScroll = min(m.helpScroll+1, maxScroll)
			return m, nil
		case "k", "up":
			m.helpScroll = max(0, m.helpScroll-1)
			return m, nil
		case "ctrl+f":
			m.helpScroll = min(m.helpScroll+m.height/4, maxScroll)
			return m, nil
		case "ctrl+b":
			m.helpScroll = max(0, m.helpScroll-m.height/4)
			return m, nil
		case "esc", "q", "?":
			m.helpVisible = false
			m.helpScroll = 0
			return m, nil
		default:
			m.helpVisible = false
			m.helpScroll = 0
			// Don't consume: let the key fall through to normal handling.
		}
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
