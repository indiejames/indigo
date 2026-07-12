package client

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

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
		for end < len(runes)-1 && isWordChar(runes[end+1]) {
			end++
		}
		m.sel = &Selection{
			Anchor: document.Pos{Line: m.cursor.Line, Col: start},
			Head:   document.Pos{Line: m.cursor.Line, Col: end + 1},
		}
		m.cursor = document.Pos{Line: m.cursor.Line, Col: end + 1}
	})
	return m, nil
}

var openBrackets = map[rune]rune{'(': ')', '[': ']', '{': '}'}
var closeBrackets = map[rune]rune{')': '(', ']': '[', '}': '{'}

// scanMatchingClose searches forward from (line, col) for the matching close bracket.
// Returns the position of the matching close bracket, or (-1,-1,false) if not found.
func scanMatchingClose(m Model, line, col int, open, close rune) (int, int, bool) {
	depth := 0
	for l := line; l < m.buf.LineCount(); l++ {
		runes := []rune(m.buf.Line(l))
		startCol := 0
		if l == line {
			startCol = col
		}
		for c := startCol; c < len(runes); c++ {
			switch runes[c] {
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return l, c, true
				}
			}
		}
	}
	return -1, -1, false
}

// scanMatchingOpen searches backward from (line, col) for the matching open bracket.
func scanMatchingOpen(m Model, line, col int, open, close rune) (int, int, bool) {
	depth := 0
	for l := line; l >= 0; l-- {
		runes := []rune(m.buf.Line(l))
		startCol := len(runes) - 1
		if l == line {
			startCol = col
		}
		for c := startCol; c >= 0; c-- {
			switch runes[c] {
			case close:
				depth++
			case open:
				depth--
				if depth == 0 {
					return l, c, true
				}
			}
		}
	}
	return -1, -1, false
}

func executeGotoMatchingBracket(m Model) (tea.Model, tea.Cmd) {
	line := m.cursor.Line
	runes := []rune(m.buf.Line(line))
	if len(runes) == 0 {
		return m, nil
	}
	col := min(m.cursor.Col, len(runes)-1)
	ch := runes[col]

	if close, ok := openBrackets[ch]; ok {
		// Cursor is on an open bracket — scan forward for its close.
		if l, c, found := scanMatchingClose(m, line, col, ch, close); found {
			m.sel = nil
			m.cursor = document.Pos{Line: l, Col: c}
			m.scrollToCursor()
		}
		return m, nil
	}
	if open, ok := closeBrackets[ch]; ok {
		// Cursor is on a close bracket — scan backward for its open.
		if l, c, found := scanMatchingOpen(m, line, col, open, ch); found {
			m.sel = nil
			m.cursor = document.Pos{Line: l, Col: c}
			m.scrollToCursor()
		}
		return m, nil
	}
	// Not on a bracket — scan forward on this line for the first open bracket.
	for c := col; c < len(runes); c++ {
		if close, ok := openBrackets[runes[c]]; ok {
			if l, mc, found := scanMatchingClose(m, line, c, runes[c], close); found {
				m.sel = nil
				m.cursor = document.Pos{Line: l, Col: mc}
				m.scrollToCursor()
			}
			return m, nil
		}
	}
	return m, nil
}

func executeSelectInsideBrackets(m Model) (tea.Model, tea.Cmd) {
	line := m.cursor.Line
	runes := []rune(m.buf.Line(line))
	col := min(m.cursor.Col, max(0, len(runes)-1))

	// Find the nearest enclosing open bracket by scanning backward.
	var open, close rune
	var openL, openC int
	found := false
	for l := line; l >= 0 && !found; l-- {
		lr := []rune(m.buf.Line(l))
		endC := len(lr) - 1
		if l == line {
			endC = col
		}
		for c := endC; c >= 0; c-- {
			if cls, ok := openBrackets[lr[c]]; ok {
				open, close = lr[c], cls
				openL, openC = l, c
				found = true
				break
			}
		}
	}
	if !found {
		return m, nil
	}
	closeL, closeC, ok := scanMatchingClose(m, openL, openC, open, close)
	if !ok {
		return m, nil
	}
	// Select inside: from just after open to just before close.
	anchor := document.Pos{Line: openL, Col: openC + 1}
	head := document.Pos{Line: closeL, Col: closeC}
	m.sel = &Selection{Anchor: anchor, Head: head}
	m.cursor = head
	m.scrollToCursor()
	return m, nil
}

func executeSelectInsideChar(m Model) (tea.Model, tea.Cmd) {
	line := m.cursor.Line
	runes := []rune(m.buf.Line(line))
	if len(runes) == 0 {
		return m, nil
	}
	col := min(m.cursor.Col, len(runes)-1)

	for _, delim := range []rune{'"', '\'', '`'} {
		// Search left for the delimiter.
		left := -1
		for c := col; c >= 0; c-- {
			if runes[c] == delim {
				left = c
				break
			}
		}
		if left < 0 {
			continue
		}
		// Search right for the matching delimiter.
		right := -1
		for c := col + 1; c < len(runes); c++ {
			if runes[c] == delim {
				right = c
				break
			}
		}
		if right < 0 {
			continue
		}
		// Select inside: from just after left delim to just before right.
		anchor := document.Pos{Line: line, Col: left + 1}
		head := document.Pos{Line: line, Col: right}
		m.sel = &Selection{Anchor: anchor, Head: head}
		m.cursor = head
		return m, nil
	}
	return m, nil
}

func applyTextObject(m Model, to highlight.TextObject) (tea.Model, tea.Cmd) {
	if to.StartLine < 0 || to.EndLine < 0 {
		return m, nil
	}
	anchor := document.Pos{Line: to.StartLine, Col: to.StartCol}
	head := document.Pos{Line: to.EndLine, Col: to.EndCol}
	m.sel = &Selection{Anchor: anchor, Head: head}
	m.cursor = head
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

func executeSelectAroundBrackets(m Model) (tea.Model, tea.Cmd) {
	line := m.cursor.Line
	runes := []rune(m.buf.Line(line))
	col := min(m.cursor.Col, max(0, len(runes)-1))

	var open, close rune
	var openL, openC int
	found := false
	for l := line; l >= 0 && !found; l-- {
		lr := []rune(m.buf.Line(l))
		endC := len(lr) - 1
		if l == line {
			endC = col
		}
		for c := endC; c >= 0; c-- {
			if cls, ok := openBrackets[lr[c]]; ok {
				open, close = lr[c], cls
				openL, openC = l, c
				found = true
				break
			}
		}
	}
	if !found {
		return m, nil
	}
	closeL, closeC, ok := scanMatchingClose(m, openL, openC, open, close)
	if !ok {
		return m, nil
	}
	// Select around: include the brackets themselves.
	anchor := document.Pos{Line: openL, Col: openC}
	head := document.Pos{Line: closeL, Col: closeC + 1}
	m.sel = &Selection{Anchor: anchor, Head: head}
	m.cursor = head
	m.scrollToCursor()
	return m, nil
}

func executeSelectAroundChar(m Model) (tea.Model, tea.Cmd) {
	line := m.cursor.Line
	runes := []rune(m.buf.Line(line))
	if len(runes) == 0 {
		return m, nil
	}
	col := min(m.cursor.Col, len(runes)-1)

	for _, delim := range []rune{'"', '\'', '`'} {
		left := -1
		for c := col; c >= 0; c-- {
			if runes[c] == delim {
				left = c
				break
			}
		}
		if left < 0 {
			continue
		}
		right := -1
		for c := col + 1; c < len(runes); c++ {
			if runes[c] == delim {
				right = c
				break
			}
		}
		if right < 0 {
			continue
		}
		anchor := document.Pos{Line: line, Col: left}
		head := document.Pos{Line: line, Col: right + 1}
		m.sel = &Selection{Anchor: anchor, Head: head}
		m.cursor = head
		return m, nil
	}
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
