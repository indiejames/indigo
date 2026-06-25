package client

import (
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// ordered returns (start, end) in document order (start <= end).
func (s *Selection) ordered() (start, end document.Pos) {
	if s.Anchor.Line < s.Head.Line ||
		(s.Anchor.Line == s.Head.Line && s.Anchor.Col <= s.Head.Col) {
		return s.Anchor, s.Head
	}
	return s.Head, s.Anchor
}

// selectWord selects the word at the cursor. If a word is already selected,
// advances to the next word on the same line.
func (m *Model) selectWord() {
	runes := []rune(m.buf.Line(m.cursor.Line))
	lineNum := m.cursor.Line
	if m.sel != nil && !m.sel.IsLine {
		// Advance: find next word after the current selection head.
		_, end := m.sel.ordered()
		if end.Line == lineNum {
			s, e, ok := findNextWordFrom(runes, end.Col)
			if ok {
				m.sel = &Selection{Anchor: document.Pos{Line: lineNum, Col: s}, Head: document.Pos{Line: lineNum, Col: e}}
				m.cursor = m.sel.Head
				m.scrollToCursor()
				return
			}
		}
	}
	// Fresh word select from cursor.
	s, e, ok := findWordAt(runes, m.cursor.Col)
	if ok {
		m.sel = &Selection{Anchor: document.Pos{Line: lineNum, Col: s}, Head: document.Pos{Line: lineNum, Col: e}}
		m.cursor = m.sel.Head
		m.scrollToCursor()
	}
}

// selectLine selects the entire current line.
func (m *Model) selectLine() {
	line := m.cursor.Line
	lineLen := m.buf.LineLen(line)
	m.sel = &Selection{
		Anchor: document.Pos{Line: line, Col: 0},
		Head:   document.Pos{Line: line, Col: max(0, lineLen-1)},
		IsLine: true,
	}
	m.cursor = m.sel.Head
}

// deleteSelection deletes the selected text and returns the updated model + cmd.
// If there is no selection the model is returned unchanged.
func (m Model) deleteSelection() (Model, tea.Cmd) {
	if m.sel == nil {
		lineLen := m.buf.LineLen(m.cursor.Line)
		if lineLen == 0 {
			return m, nil
		}
		col := min(m.cursor.Col, lineLen-1)
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
