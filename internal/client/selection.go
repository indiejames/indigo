package client

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// selectedText returns the text covered by the current selection, or "" if none.
func (m *Model) selectedText() string {
	if m.sel == nil {
		return ""
	}
	return m.textForSelection(m.sel)
}

// textForSelection returns the text covered by sel, which need not be m.sel.
func (m *Model) textForSelection(sel *Selection) string {
	start, end := sel.ordered()
	if sel.IsLine {
		var sb strings.Builder
		for l := start.Line; l <= end.Line; l++ {
			sb.WriteString(m.buf.Line(l))
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	if start.Line == end.Line {
		runes := []rune(m.buf.Line(start.Line))
		hi := min(end.Col+1, len(runes))
		lo := min(start.Col, hi)
		return string(runes[lo:hi])
	}
	var sb strings.Builder
	// First line: from start.Col to end of line.
	firstRunes := []rune(m.buf.Line(start.Line))
	if start.Col < len(firstRunes) {
		sb.WriteString(string(firstRunes[start.Col:]))
	}
	sb.WriteByte('\n')
	// Middle lines (complete).
	for l := start.Line + 1; l < end.Line; l++ {
		sb.WriteString(m.buf.Line(l))
		sb.WriteByte('\n')
	}
	// Last line: from 0 to end.Col+1.
	lastRunes := []rune(m.buf.Line(end.Line))
	hi := min(end.Col+1, len(lastRunes))
	sb.WriteString(string(lastRunes[:hi]))
	return sb.String()
}

// copySel returns an independent copy of s. Selection handlers mutate the
// pointed-to struct in place, so change detection needs a snapshot.
func copySel(s *Selection) *Selection {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

// selEqual reports whether two selections cover the same range.
func selEqual(a, b *Selection) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ordered returns (start, end) in document order (start <= end).
func (s *Selection) ordered() (start, end document.Pos) {
	if s.Anchor.Line < s.Head.Line ||
		(s.Anchor.Line == s.Head.Line && s.Anchor.Col <= s.Head.Col) {
		return s.Anchor, s.Head
	}
	return s.Head, s.Anchor
}

// selectWord selects the word at the cursor. If a word is already selected,
// advances to the next word, crossing line boundaries if needed.
func (m *Model) selectWord() {
	totalLines := m.buf.LineCount()
	if m.sel != nil && !m.sel.IsLine {
		_, end := m.sel.ordered()
		// Try advancing from end.Col on end.Line, then subsequent lines.
		for l := end.Line; l < totalLines; l++ {
			runes := []rune(m.buf.Line(l))
			var s, e int
			var ok bool
			if l == end.Line {
				s, e, ok = findNextWordFrom(runes, end.Col)
			} else {
				s, e, ok = findWordAt(runes, 0)
			}
			if ok {
				m.sel = &Selection{
					Anchor: document.Pos{Line: l, Col: s},
					Head:   document.Pos{Line: l, Col: e},
				}
				m.cursor = m.sel.Head
				m.scrollToCursor()
				return
			}
		}
		return
	}
	// Fresh word select from cursor, crossing lines if needed.
	for l := m.cursor.Line; l < totalLines; l++ {
		runes := []rune(m.buf.Line(l))
		col := 0
		if l == m.cursor.Line {
			col = m.cursor.Col
		}
		s, e, ok := findWordAt(runes, col)
		if ok {
			m.sel = &Selection{
				Anchor: document.Pos{Line: l, Col: s},
				Head:   document.Pos{Line: l, Col: e},
			}
			m.cursor = m.sel.Head
			m.scrollToCursor()
			return
		}
	}
}

// selectLine selects the entire current line. If a line selection already
// exists, it extends the selection forward to the next line.
func (m *Model) selectLine() {
	if m.sel != nil && m.sel.IsLine {
		_, end := m.sel.ordered()
		next := end.Line + 1
		if next >= m.buf.LineCount() {
			return
		}
		nextLen := m.buf.LineLen(next)
		// Extend whichever endpoint is at the forward line.
		if m.sel.Head.Line >= m.sel.Anchor.Line {
			m.sel.Head = document.Pos{Line: next, Col: max(0, nextLen-1)}
		} else {
			m.sel.Anchor = document.Pos{Line: next, Col: max(0, nextLen-1)}
		}
		m.cursor = m.sel.Head
		m.scrollToCursor()
		return
	}
	line := m.cursor.Line
	lineLen := m.buf.LineLen(line)
	m.sel = &Selection{
		Anchor: document.Pos{Line: line, Col: 0},
		Head:   document.Pos{Line: line, Col: max(0, lineLen-1)},
		IsLine: true,
	}
	m.cursor = m.sel.Head
}

// extendLineBackward extends a line selection to include the previous line.
// If there is no line selection, selects the current line then extends up.
func (m *Model) extendLineBackward() {
	if m.sel == nil || !m.sel.IsLine {
		m.selectLine()
	}
	start, _ := m.sel.ordered()
	prev := start.Line - 1
	if prev < 0 {
		return
	}
	// Move whichever endpoint is at the top backward.
	if m.sel.Anchor.Line <= m.sel.Head.Line {
		m.sel.Anchor = document.Pos{Line: prev, Col: 0}
	} else {
		m.sel.Head = document.Pos{Line: prev, Col: 0}
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
}

// deleteSelection deletes the selected text and returns the updated model + cmd.
// If there is no selection the model is returned unchanged.
func (m Model) deleteSelection() (Model, tea.Cmd) {
	if m.sel == nil {
		lineLen := m.buf.LineLen(m.cursor.Line)
		if m.cursor.Col >= lineLen {
			// Cursor rests on the line break itself: delete it like any other
			// character, joining this line with the next.
			if m.cursor.Line >= m.buf.LineCount()-1 {
				return m, nil
			}
			op := document.Op{
				ClientID: m.clientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line, FromCol: lineLen,
				ToLine: m.cursor.Line + 1, ToCol: 0,
			}
			return applyOp(m, op)
		}
		col := m.cursor.Col
		m.sel = &Selection{
			Anchor: document.Pos{Line: m.cursor.Line, Col: col},
			Head:   document.Pos{Line: m.cursor.Line, Col: col},
		}
	}
	start, end := m.sel.ordered()
	op := document.Op{
		ClientID: m.rpc.ClientID(),
		Type:     document.OpDelete,
		FromLine: start.Line,
		FromCol:  start.Col,
	}
	if m.sel.IsLine {
		lc := m.buf.LineCount()
		if end.Line+1 < lc {
			op.ToLine = end.Line + 1
			op.ToCol = 0
		} else {
			op.ToLine = end.Line
			op.ToCol = m.buf.LineLen(end.Line)
		}
	} else {
		lineLen := m.buf.LineLen(end.Line)
		if end.Col+1 <= lineLen {
			op.ToLine = end.Line
			op.ToCol = end.Col + 1
		} else {
			lc := m.buf.LineCount()
			if end.Line+1 < lc {
				op.ToLine = end.Line + 1
				op.ToCol = 0
			} else {
				op.ToLine = end.Line
				op.ToCol = lineLen
			}
		}
	}
	m.cursor = start
	m.sel = nil
	return applyOp(m, op)
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// findWholeWordAt returns the inclusive [start, end] of the word that contains col,
// scanning backward to the word start and forward to the word end.
// Unlike findWordAt (which only scans forward), this is used for double-click selection.
func findWholeWordAt(runes []rune, col int) (start, end int, found bool) {
	n := len(runes)
	if n == 0 || col >= n || !isWordChar(runes[col]) {
		return -1, -1, false
	}
	start = col
	for start > 0 && isWordChar(runes[start-1]) {
		start--
	}
	end = col
	for end+1 < n && isWordChar(runes[end+1]) {
		end++
	}
	return start, end, true
}

// findWordAt returns the inclusive [start, end] of the word at or ahead of col.
func findWordAt(runes []rune, col int) (start, end int, found bool) {
	n := len(runes)
	if n == 0 {
		return -1, -1, false
	}
	i := min(col, n-1)
	if !isWordChar(runes[i]) {
		// Not on a word char: skip forward to the next word.
		for i < n && !isWordChar(runes[i]) {
			i++
		}
		if i >= n {
			return -1, -1, false
		}
	}
	// Select from here (cursor or start of next word) to end of word.
	start = i
	for i < n && isWordChar(runes[i]) {
		i++
	}
	return start, i - 1, true
}

// findNextWordStart returns the column of the start of the next word at or after col.
// If col is inside a word, skips to the end of that word first.
func findNextWordStart(runes []rune, col int) (int, bool) {
	n := len(runes)
	i := col
	for i < n && isWordChar(runes[i]) {
		i++
	}
	for i < n && !isWordChar(runes[i]) {
		i++
	}
	if i >= n {
		return -1, false
	}
	return i, true
}

// findPrevWordStart returns the start column of the word that precedes col.
// When col is mid-word, returns the start of that word.
// When col is at a word boundary or on non-word chars, returns the start of the prior word.
func findPrevWordStart(runes []rune, col int) (int, bool) {
	n := len(runes)
	if n == 0 || col <= 0 {
		return -1, false
	}
	i := min(col, n) - 1
	// Skip non-word chars backward.
	for i >= 0 && !isWordChar(runes[i]) {
		i--
	}
	if i < 0 {
		return -1, false
	}
	// Step back to start of word.
	for i > 0 && isWordChar(runes[i-1]) {
		i--
	}
	return i, true
}

// findWordEnd returns the column of the end (inclusive) of the word at or after col.
// If col is already at a word end, advances to the end of the next word.
func findWordEnd(runes []rune, col int) (int, bool) {
	n := len(runes)
	if n == 0 {
		return -1, false
	}
	i := min(col, n-1)
	// If at the end of a word, step past it.
	if isWordChar(runes[i]) && (i+1 >= n || !isWordChar(runes[i+1])) {
		i++
	}
	// Skip non-word chars.
	for i < n && !isWordChar(runes[i]) {
		i++
	}
	if i >= n {
		return -1, false
	}
	// Advance to end of word.
	for i+1 < n && isWordChar(runes[i+1]) {
		i++
	}
	return i, true
}

// findNextWordFrom returns the inclusive [start, end] of the next word that
// begins strictly after afterEnd.
func findNextWordFrom(runes []rune, afterEnd int) (start, end int, found bool) {
	n := len(runes)
	i := afterEnd + 1
	// Skip any remaining word chars (tail of current word).
	for i < n && isWordChar(runes[i]) {
		i++
	}
	// Skip non-word chars.
	for i < n && !isWordChar(runes[i]) {
		i++
	}
	if i >= n {
		return -1, -1, false
	}
	start = i
	for i < n && isWordChar(runes[i]) {
		i++
	}
	return start, i - 1, true
}

// moveToPrevWordStart moves the cursor to the start of the previous word,
// crossing line boundaries if necessary. Selection is cleared by the caller.
func (m *Model) moveToPrevWordStart() {
	for l := m.cursor.Line; l >= 0; l-- {
		runes := []rune(m.buf.Line(l))
		col := m.cursor.Col
		if l < m.cursor.Line {
			col = len(runes) // search from end of earlier lines
		}
		if s, ok := findPrevWordStart(runes, col); ok {
			m.cursor = document.Pos{Line: l, Col: s}
			m.scrollToCursor()
			return
		}
	}
	m.cursor = document.Pos{}
	m.scrollToCursor()
}

// moveToNextWordStart moves the cursor to the start of the next word,
// crossing line boundaries if necessary. Selection is cleared by the caller.
func (m *Model) moveToNextWordStart() {
	totalLines := m.buf.LineCount()
	for l := m.cursor.Line; l < totalLines; l++ {
		runes := []rune(m.buf.Line(l))
		col := 0
		if l == m.cursor.Line {
			col = m.cursor.Col
		}
		if s, ok := findNextWordStart(runes, col); ok {
			m.cursor = document.Pos{Line: l, Col: s}
			m.scrollToCursor()
			return
		}
	}
}

// extendToNextWordStart moves the selection head forward to the start of the
// next word. If there is no selection, starts one at the cursor.
func (m *Model) extendToNextWordStart() {
	head := m.cursor
	if m.sel != nil {
		head = m.sel.Head
	}
	totalLines := m.buf.LineCount()
	for l := head.Line; l < totalLines; l++ {
		runes := []rune(m.buf.Line(l))
		col := 0
		if l == head.Line {
			col = head.Col
		}
		if s, ok := findNextWordStart(runes, col); ok {
			newHead := document.Pos{Line: l, Col: s}
			if m.sel == nil {
				m.sel = &Selection{Anchor: m.cursor, Head: newHead}
			} else {
				m.sel.Head = newHead
			}
			m.cursor = newHead
			m.scrollToCursor()
			return
		}
	}
}

// moveToWordEnd moves the cursor to the end of the current or next word,
// crossing line boundaries if necessary. Selection is cleared by the caller.
func (m *Model) moveToWordEnd() {
	totalLines := m.buf.LineCount()
	for l := m.cursor.Line; l < totalLines; l++ {
		runes := []rune(m.buf.Line(l))
		col := 0
		if l == m.cursor.Line {
			col = m.cursor.Col
		}
		if e, ok := findWordEnd(runes, col); ok {
			m.cursor = document.Pos{Line: l, Col: e}
			m.scrollToCursor()
			return
		}
	}
}

// extendWordForward moves the selection head forward to the end of the next
// word. If there is no selection, starts one at the cursor.
func (m *Model) extendWordForward() {
	head := m.cursor
	if m.sel != nil {
		head = m.sel.Head
	}
	totalLines := m.buf.LineCount()
	for l := head.Line; l < totalLines; l++ {
		runes := []rune(m.buf.Line(l))
		col := 0
		if l == head.Line {
			col = head.Col
		}
		if e, ok := findWordEnd(runes, col); ok {
			newHead := document.Pos{Line: l, Col: e}
			if m.sel == nil {
				m.sel = &Selection{Anchor: m.cursor, Head: newHead}
			} else {
				m.sel.Head = newHead
			}
			m.cursor = newHead
			m.scrollToCursor()
			return
		}
	}
}

// extendWordBackward moves the selection head backward to the start of the
// previous word. If there is no selection, starts one at the cursor.
func (m *Model) extendWordBackward() {
	head := m.cursor
	if m.sel != nil {
		head = m.sel.Head
	}
	for l := head.Line; l >= 0; l-- {
		runes := []rune(m.buf.Line(l))
		col := head.Col
		if l < head.Line {
			col = len(runes)
		}
		if s, ok := findPrevWordStart(runes, col); ok {
			newHead := document.Pos{Line: l, Col: s}
			if m.sel == nil {
				m.sel = &Selection{Anchor: m.cursor, Head: newHead}
			} else {
				m.sel.Head = newHead
			}
			m.cursor = newHead
			m.scrollToCursor()
			return
		}
	}
}

// selectAll selects the entire buffer contents.
func (m *Model) selectAll() {
	lastLine := max(0, m.buf.LineCount()-1)
	lastCol := max(0, m.buf.LineLen(lastLine)-1)
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: lastLine, Col: lastCol},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
}

// flipSelection swaps the anchor and head of the current selection,
// moving the cursor to the new head.
func (m *Model) flipSelection() {
	if m.sel == nil {
		return
	}
	m.sel.Anchor, m.sel.Head = m.sel.Head, m.sel.Anchor
	m.cursor = m.sel.Head
	m.scrollToCursor()
}
