package client

import (
	"strings"

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
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.cursor.Col = 0
	})
	return m, nil
}

func executeGoToLineEnd(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)
	})
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
			Head:   document.Pos{Line: m.cursor.Line, Col: end},
		}
		m.cursor = document.Pos{Line: m.cursor.Line, Col: end}
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

// matchingPairPos returns the position of the other half of the bracket or
// quote pair the cursor currently sits on top of — used to underline it as
// a scope indicator (see renderLineChunk). ok is false when the cursor
// isn't on a bracket/quote character, or no partner is found.
func matchingPairPos(m Model) (line, col int, ok bool) {
	runes := []rune(m.buf.Line(m.cursor.Line))
	if m.cursor.Col >= len(runes) {
		return 0, 0, false
	}
	ch := runes[m.cursor.Col]

	if close, isOpen := openBrackets[ch]; isOpen {
		return scanMatchingClose(m, m.cursor.Line, m.cursor.Col, ch, close)
	}
	if open, isClose := closeBrackets[ch]; isClose {
		return scanMatchingOpen(m, m.cursor.Line, m.cursor.Col, open, ch)
	}
	switch ch {
	case '"', '\'', '`':
		return matchingQuotePos(runes, m.cursor.Line, m.cursor.Col, ch)
	}
	return 0, 0, false
}

// matchingQuotePos pairs up same-line occurrences of delim by position (1st
// with 2nd, 3rd with 4th, ...) and returns the partner of the occurrence at
// col, if col itself is one of them. Quotes aren't directional like
// brackets, so unlike scanMatchingClose/Open this doesn't track nesting —
// same convention executeSelectInsideChar/AroundChar already use.
func matchingQuotePos(runes []rune, line, col int, delim rune) (int, int, bool) {
	var occurrences []int
	for i, r := range runes {
		if r == delim {
			occurrences = append(occurrences, i)
		}
	}
	idx := -1
	for i, c := range occurrences {
		if c == col {
			idx = i
			break
		}
	}
	if idx == -1 {
		return 0, 0, false
	}
	partner := idx + 1
	if idx%2 == 1 {
		partner = idx - 1
	}
	if partner < 0 || partner >= len(occurrences) {
		return 0, 0, false
	}
	return line, occurrences[partner], true
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
	// Select inside: from just after open to just before close (exclusive
	// of both bracket characters). posBefore can land before anchor when
	// the brackets are adjacent ("()"); clamp so the selection doesn't
	// invert and swallow the brackets instead.
	anchor := document.Pos{Line: openL, Col: openC + 1}
	head := posBefore(m, closeL, closeC)
	if head.Line < anchor.Line || (head.Line == anchor.Line && head.Col < anchor.Col) {
		head = anchor
	}
	m.sel = &Selection{Anchor: anchor, Head: head}
	m.cursor = head
	m.scrollToCursor()
	return m, nil
}

// posBefore returns the position immediately before (line, col) in
// inclusive-selection terms: col > 0 is simply col-1 on the same line;
// col == 0 means "through the end of the previous line" (using that
// line's rune length, matching how selectedText/rendering already treat
// an end column equal to a line's length as extending through its end).
func posBefore(m Model, line, col int) document.Pos {
	if col > 0 {
		return document.Pos{Line: line, Col: col - 1}
	}
	if line == 0 {
		return document.Pos{Line: 0, Col: 0}
	}
	prevLine := line - 1
	return document.Pos{Line: prevLine, Col: len([]rune(m.buf.Line(prevLine)))}
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
		// Select inside: from just after left delim to just before right
		// delim (exclusive of both delimiter characters). right-1 can land
		// before anchor when the quoted text is empty (""); clamp so the
		// selection doesn't invert.
		anchor := document.Pos{Line: line, Col: left + 1}
		headCol := right - 1
		if headCol < anchor.Col {
			headCol = anchor.Col
		}
		head := document.Pos{Line: line, Col: headCol}
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
				ToLine: ln, ToCol: indent + deleteLen,
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
			ToLine: ln, ToCol: remove,
		})
	}
	if len(ops) == 0 {
		return m, nil
	}
	m, cmd := applyBatch(m, ops)
	m.sel = nil
	return m, cmd
}

// executeJoinLines joins the current line with the line below it (vim's J):
// the line break and the next line's leading whitespace are replaced with a
// single space. The space is omitted when the current line is empty or
// already ends in whitespace, the next line is blank, or it starts with ')'.
func executeJoinLines(m Model) (tea.Model, tea.Cmd) {
	if m.cursor.Line >= m.buf.LineCount()-1 {
		return m, nil
	}
	cur := []rune(m.buf.Line(m.cursor.Line))
	next := []rune(m.buf.Line(m.cursor.Line + 1))
	indent := leadingWhitespace(next)
	rest := next[indent:]

	ops := []document.Op{{
		ClientID: m.clientID(),
		Type:     document.OpDelete,
		FromLine: m.cursor.Line, FromCol: len(cur),
		ToLine: m.cursor.Line + 1, ToCol: indent,
	}}

	space := len(cur) > 0 && cur[len(cur)-1] != ' ' && cur[len(cur)-1] != '\t' &&
		len(rest) > 0 && rest[0] != ')'
	if space {
		ops = append(ops, document.Op{
			ClientID:   m.clientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  len(cur),
			InsertText: " ",
		})
	}

	m.sel = nil
	m.cursor = document.Pos{Line: m.cursor.Line, Col: len(cur)}
	return applyBatch(m, ops)
}

// executeMoveLineUp moves the current line (or every selected line) up by
// one line, swapping places with the line above it.
func executeMoveLineUp(m Model) (tea.Model, tea.Cmd) {
	return moveLines(m, -1)
}

// executeMoveLineDown moves the current line (or every selected line) down by
// one line, swapping places with the line below it.
func executeMoveLineDown(m Model) (tea.Model, tea.Cmd) {
	return moveLines(m, 1)
}

// moveLines swaps the selected line range (or the cursor line, via
// selectionLineRange) with its neighboring line in the direction of dir (-1
// up, +1 down). The cursor and selection shift by dir so repeated presses
// keep moving the same block further.
//
// The whole-line delete mirrors deleteSelection's IsLine branch: it consumes
// the newline after its last line when a following line exists, and
// otherwise stops at that line's content, since the last line in a buffer
// with no trailing "\n" has none to consume.
func moveLines(m Model, dir int) (tea.Model, tea.Cmd) {
	startLine, endLine := m.selectionLineRange()
	lc := m.buf.LineCount()
	if dir < 0 && startLine == 0 {
		return m, nil
	}
	if dir > 0 && endLine >= lc-1 {
		return m, nil
	}

	block := make([]string, 0, endLine-startLine+1)
	for ln := startLine; ln <= endLine; ln++ {
		block = append(block, m.buf.Line(ln))
	}

	// Reindent the block to fit the line it will end up right after — the
	// same rule handleEnter uses for a freshly typed line landing there —
	// so it settles into the right indentation the moment it crosses a
	// brace (or other block) boundary, rather than carrying its old
	// indentation with it. prevLine is that line's content, as it stands
	// before the swap below (dir<0: the line above the neighbor being
	// swapped past, since that neighbor ends up between it and the block;
	// dir>0: the neighbor itself, which the block swaps below). Passing -1
	// for contextIndent's line number deliberately skips its tree-sitter
	// dedent check: that check only makes sense for content spliced in
	// right before an already-typed closer *on the same line*, and prevLine
	// here is always used at its own end-of-line, so it would never apply —
	// worse, doing the equivalent check against the line the block lands
	// *before* would dedent it to the enclosing scope's own level even when
	// a sibling statement right above (prevLine) already establishes the
	// correct, deeper body indent.
	var from, to int
	var prevLine string
	if dir < 0 {
		from, to = startLine-1, endLine
		if from > 0 {
			prevLine = m.buf.Line(from - 1)
		}
	} else {
		from, to = startLine, endLine+1
		prevLine = m.buf.Line(to)
	}
	var indentDelta int
	if baseline, ok := blockBaseIndent(block); ok {
		target := m.contextIndent(prevLine, -1, len([]rune(prevLine)))
		block, indentDelta = reindentLines(block, baseline, target)
	}

	var lines []string
	if dir < 0 {
		lines = append(append([]string{}, block...), m.buf.Line(from))
	} else {
		lines = append([]string{m.buf.Line(to)}, block...)
	}

	delOp := document.Op{ClientID: m.clientID(), Type: document.OpDelete, FromLine: from, FromCol: 0}
	newText := strings.Join(lines, "\n")
	if to+1 < lc {
		delOp.ToLine, delOp.ToCol = to+1, 0
		newText += "\n"
	} else {
		delOp.ToLine, delOp.ToCol = to, m.buf.LineLen(to)
	}
	insOp := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: from,
		InsertCol:  0,
		InsertText: newText,
	}

	// Adjust cursor and selection positions for both line and column changes.
	// Line numbers shift by dir; columns on moved non-blank lines shift by indentDelta.
	// Check against original positions before moving.
	if m.cursor.Line >= startLine && m.cursor.Line <= endLine {
		movedLineIdx := m.cursor.Line - startLine
		if movedLineIdx >= 0 && movedLineIdx < len(block) && strings.TrimSpace(block[movedLineIdx]) != "" {
			m.cursor.Col = max(0, m.cursor.Col+indentDelta)
		}
	}
	m.cursor.Line += dir

	if m.sel != nil {
		if m.sel.Anchor.Line >= startLine && m.sel.Anchor.Line <= endLine {
			movedLineIdx := m.sel.Anchor.Line - startLine
			if movedLineIdx >= 0 && movedLineIdx < len(block) && strings.TrimSpace(block[movedLineIdx]) != "" {
				m.sel.Anchor.Col = max(0, m.sel.Anchor.Col+indentDelta)
			}
		}
		m.sel.Anchor.Line += dir

		if m.sel.Head.Line >= startLine && m.sel.Head.Line <= endLine {
			movedLineIdx := m.sel.Head.Line - startLine
			if movedLineIdx >= 0 && movedLineIdx < len(block) && strings.TrimSpace(block[movedLineIdx]) != "" {
				m.sel.Head.Col = max(0, m.sel.Head.Col+indentDelta)
			}
		}
		m.sel.Head.Line += dir
	}
	return applyBatch(m, []document.Op{delOp, insOp})
}
