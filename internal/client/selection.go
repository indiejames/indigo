package client

import (
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// selectedText returns the text covered by the current selection, or "" if none.
func (m *Model) selectedText() string {
	if m.sel == nil {
		return ""
	}
	return m.textForSelection(m.sel)
}

// charUnderCursor returns the single character at the cursor position. When
// the cursor rests on the line break itself (see moveCursorChar/normalLineEnd),
// it returns "\n"; at the very end of the buffer it returns "".
func (m *Model) charUnderCursor() string {
	lineLen := m.buf.LineLen(m.cursor.Line)
	if m.cursor.Col >= lineLen {
		if m.cursor.Line >= m.buf.LineCount()-1 {
			return ""
		}
		return "\n"
	}
	runes := []rune(m.buf.Line(m.cursor.Line))
	return string(runes[m.cursor.Col])
}

// textForSelection returns the text covered by sel, which need not be m.sel.
func (m *Model) textForSelection(sel *Selection) string {
	start, end := sel.ordered()
	return m.buf.Text(start, end)
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

// ordered returns (start, end) in document order (start <= end). The
// selection is the half-open range [start, end): positions sit between
// characters, so end is the first character not selected.
func (s *Selection) ordered() (start, end document.Pos) {
	if s.Anchor.Line < s.Head.Line ||
		(s.Anchor.Line == s.Head.Line && s.Anchor.Col <= s.Head.Col) {
		return s.Anchor, s.Head
	}
	return s.Head, s.Anchor
}

// empty reports whether the selection covers nothing.
func (s *Selection) empty() bool { return s.Anchor == s.Head }

// dropEmptySelection clears a selection that covers nothing, so that "no
// selection" has one representation.
func (m *Model) dropEmptySelection() {
	if m.sel != nil && m.sel.empty() {
		m.sel = nil
	}
}

// bufferEnd is the position after the last character.
func (m *Model) bufferEnd() document.Pos {
	last := max(0, m.buf.LineCount()-1)
	return document.Pos{Line: last, Col: m.buf.LineLen(last)}
}

// lineSpanEnd is where a whole-line selection of line ends: the start of the
// next line, or the end of the buffer on the last line.
func (m *Model) lineSpanEnd(line int) document.Pos {
	if line+1 < m.buf.LineCount() {
		return document.Pos{Line: line + 1}
	}
	return document.Pos{Line: line, Col: m.buf.LineLen(line)}
}

// isLineSelection reports whether the selection covers whole lines: it starts
// at a line start and ends at one (or at the end of the buffer).
func (m *Model) isLineSelection() bool {
	if m.sel == nil {
		return false
	}
	start, end := m.sel.ordered()
	if start.Col != 0 || start == end {
		return false
	}
	return end.Col == 0 || end == m.bufferEnd()
}

// selectedLines returns the lines a line-wise command (indent, comment, move)
// should act on: the cursor's line with no selection, and otherwise every line
// the selection touches — except a last line it only reaches at column 0, as
// GUI editors do, since selecting down to the start of a line selects none of
// it.
func (m *Model) selectedLines() (first, last int) {
	if m.sel == nil {
		return m.cursor.Line, m.cursor.Line
	}
	start, end := m.sel.ordered()
	if end.Col == 0 && end.Line > start.Line {
		return start.Line, end.Line - 1
	}
	return start.Line, end.Line
}

// selectWord selects the word at the cursor. If a word is already selected,
// advances to the next word, crossing line boundaries if needed.
func (m *Model) selectWord() {
	totalLines := m.buf.LineCount()
	if m.sel != nil {
		_, end := m.sel.ordered()
		// Try advancing from end.Col on end.Line, then subsequent lines.
		for l := end.Line; l < totalLines; l++ {
			runes := []rune(m.buf.Line(l))
			var s, e int
			var ok bool
			if l == end.Line {
				s, e, ok = findNextWordFrom(runes, end.Col-1)
			} else {
				s, e, ok = findWordAt(runes, 0)
			}
			if ok {
				m.sel = &Selection{
					Anchor: document.Pos{Line: l, Col: s},
					Head:   document.Pos{Line: l, Col: e + 1},
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
				Head:   document.Pos{Line: l, Col: e + 1},
			}
			m.cursor = m.sel.Head
			m.scrollToCursor()
			return
		}
	}
}

// selectLine selects the entire current line, line break included. If whole
// lines are already selected, it extends the selection by the next line.
func (m *Model) selectLine() {
	if m.isLineSelection() {
		start, end := m.sel.ordered()
		if end == m.bufferEnd() {
			return
		}
		m.sel = &Selection{Anchor: start, Head: m.lineSpanEnd(end.Line)}
		m.cursor = m.sel.Head
		m.scrollToCursor()
		return
	}
	line := m.cursor.Line
	m.sel = &Selection{Anchor: document.Pos{Line: line}, Head: m.lineSpanEnd(line)}
	m.dropEmptySelection()
	m.cursor = m.lineSpanEnd(line)
}

// extendLineBackward extends a line selection to include the previous line.
// If there is no line selection, selects the current line then extends up.
func (m *Model) extendLineBackward() {
	if !m.isLineSelection() {
		m.selectLine()
	}
	if m.sel == nil {
		return
	}
	start, end := m.sel.ordered()
	if start.Line == 0 {
		return
	}
	m.sel = &Selection{Anchor: end, Head: document.Pos{Line: start.Line - 1}}
	m.cursor = m.sel.Head
	m.scrollToCursor()
}

// cutSelection deletes the selected text, first copying it to the clipboard
// (cut semantics), and returns the updated model + cmd. If there is no
// selection it instead cuts the character under the cursor (see
// deleteSelectionRaw), matching the `v` help entry in render_overlays.go; the
// model is only left unchanged in deleteSelectionRaw's true-EOF case (cursor
// on the buffer's final, empty line with no following line to join). This
// backs the explicit "cut" command; plain delete/change (d/c) call
// deleteSelectionRaw directly and never touch the clipboard.
func (m Model) cutSelection() (Model, tea.Cmd) {
	if text, ok := m.cutText(); ok {
		_ = clipboardWriter(text) // cut semantics; a failed copy must not block the delete
	}
	return m.deleteSelectionRaw()
}

// cutText returns the text that should be copied to the clipboard for the
// current cursor/selection state, without mutating anything. Deliberately
// returns ok=false when there is no explicit selection (m.sel == nil): the
// explicit cut command with no selection still deletes the character under
// the cursor (see deleteSelectionRaw), but repeatedly doing so — e.g. a run
// of bare cut presses to remove several characters — would also flood the
// clipboard with one single-character entry per keypress. Copying the
// character under the cursor is still available on request via `y`
// (executeYank), which keeps its own no-selection fallback since selecting a
// single character to yank it is comparatively awkward. Shared by
// cutSelection (single-cursor cut, one clipboard write) and
// deleteAllCursorSelections's cut path (which must combine every cursor's
// text into one clipboard write before any of them delete, since each
// deleteSelectionRaw call changes the buffer other cursors' text would
// otherwise still need).
func (m Model) cutText() (string, bool) {
	if m.sel == nil {
		return "", false
	}
	return m.textForSelection(m.sel), true
}

// deleteSelectionRaw performs the deletion cutText describes, without
// touching the clipboard — callers are responsible for that (see
// cutSelection and deleteAllCursorSelections). With no selection it deletes
// the character after the cursor, joining lines at a line end.
func (m Model) deleteSelectionRaw() (Model, tea.Cmd) {
	start, end := m.deletionRange()
	m.sel = nil
	if start == end {
		return m, nil
	}
	op := document.Op{
		ClientID: m.clientID(),
		Type:     document.OpDelete,
		FromLine: start.Line, FromCol: start.Col,
		ToLine: end.Line, ToCol: end.Col,
	}
	m.cursor = start
	return applyOp(m, op)
}

// deletionRange is the range deleteSelectionRaw deletes: the selection, or
// the character after the cursor. start == end means nothing.
func (m Model) deletionRange() (start, end document.Pos) {
	if m.sel != nil {
		return m.sel.ordered()
	}
	start = m.cursor
	switch {
	case m.cursor.Col < m.buf.LineLen(m.cursor.Line):
		return start, document.Pos{Line: m.cursor.Line, Col: m.cursor.Col + 1}
	case m.cursor.Line < m.buf.LineCount()-1:
		return start, document.Pos{Line: m.cursor.Line + 1}
	}
	return start, start
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func isSpaceChar(r rune) bool {
	return r == ' ' || r == '\t'
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

// extendSelectionTo moves the selection's head to p, starting a selection at
// the cursor if there is none, and leaves the cursor at the new head. Every
// extend command goes through it, so the cursor always equals the head and an
// empty selection is always dropped.
func (m *Model) extendSelectionTo(p document.Pos) {
	if m.sel == nil {
		m.sel = &Selection{Anchor: m.cursor}
	}
	m.sel.Head = p
	m.cursor = p
	m.dropEmptySelection()
	m.scrollToCursor()
}

// wordEndAfter is the end of the next word at or after p: past any non-word
// characters, then past the word. It crosses lines, and stops at the end of
// the buffer.
func (m *Model) wordEndAfter(p document.Pos) document.Pos {
	for l := p.Line; l < m.buf.LineCount(); l++ {
		runes := []rune(m.buf.Line(l))
		i := 0
		if l == p.Line {
			i = p.Col
		}
		for i < len(runes) && !isWordChar(runes[i]) {
			i++
		}
		if i < len(runes) {
			for i < len(runes) && isWordChar(runes[i]) {
				i++
			}
			return document.Pos{Line: l, Col: i}
		}
	}
	return m.bufferEnd()
}

// wordStartBefore is the start of the word before p, crossing lines, or the
// start of the buffer.
func (m *Model) wordStartBefore(p document.Pos) document.Pos {
	for l := p.Line; l >= 0; l-- {
		runes := []rune(m.buf.Line(l))
		col := len(runes)
		if l == p.Line {
			col = p.Col
		}
		if s, ok := findPrevWordStart(runes, col); ok {
			return document.Pos{Line: l, Col: s}
		}
	}
	return document.Pos{}
}

// extendToNextWordStart moves the selection head forward to the start of the
// next word. If there is no selection, starts one at the cursor.
func (m *Model) extendToNextWordStart() {
	head := m.cursor
	totalLines := m.buf.LineCount()
	for l := head.Line; l < totalLines; l++ {
		runes := []rune(m.buf.Line(l))
		col := 0
		if l == head.Line {
			col = head.Col
		}
		if s, ok := findNextWordStart(runes, col); ok {
			m.extendSelectionTo(document.Pos{Line: l, Col: s})
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

// extendWordForward moves the selection head to the end of the next word. If
// there is no selection, starts one at the cursor.
func (m *Model) extendWordForward() {
	m.extendSelectionTo(m.wordEndAfter(m.cursor))
}

// extendWordBackward moves the selection head back to the start of the
// previous word. If there is no selection, starts one at the cursor.
func (m *Model) extendWordBackward() {
	m.extendSelectionTo(m.wordStartBefore(m.cursor))
}

// extendToLineEnd extends the selection head to the end of the current line.
// If there is no selection, starts one at the cursor.
func (m *Model) extendToLineEnd() {
	line := m.cursor.Line
	m.extendSelectionTo(document.Pos{Line: line, Col: m.buf.LineLen(line)})
}

// extendToLineStart extends the selection head to the start of the current
// line. If there is no selection, starts one at the cursor.
func (m *Model) extendToLineStart() {
	m.extendSelectionTo(document.Pos{Line: m.cursor.Line})
}

// extendCharForward extends the selection head one character forward,
// crossing a line break as one character. If there is no selection, starts
// one at the cursor.
func (m *Model) extendCharForward() {
	from := m.cursor
	m.moveCursorChar(1)
	to := m.cursor
	m.cursor = from
	m.extendSelectionTo(to)
}

// extendCharBackward extends the selection head one character backward,
// mirroring extendCharForward.
func (m *Model) extendCharBackward() {
	from := m.cursor
	m.moveCursorChar(-1)
	to := m.cursor
	m.cursor = from
	m.extendSelectionTo(to)
}

// selectAll selects the entire buffer contents.
func (m *Model) selectAll() {
	m.sel = &Selection{Head: m.bufferEnd()}
	m.cursor = m.sel.Head
	m.dropEmptySelection()
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
