package client

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// autoPairs maps an opening bracket/quote rune to its matching closer.
var autoPairs = map[rune]rune{
	'(':  ')',
	'[':  ']',
	'{':  '}',
	'"':  '"',
	'\'': '\'',
	'`':  '`',
}

// autoPairQuotes holds the runes in autoPairs whose open and close forms are
// identical, so pairing only makes sense outside of an existing word (e.g.
// an apostrophe in the middle of "don't" shouldn't try to open a string).
var autoPairQuotes = map[rune]bool{
	'"':  true,
	'\'': true,
	'`':  true,
}

// autoPairClosers is the set of characters that, when typed immediately
// before a matching occurrence of themselves, move the cursor past it
// instead of inserting a duplicate.
var autoPairClosers = map[rune]bool{
	')': true, ']': true, '}': true, '"': true, '\'': true, '`': true,
}

// charAfterCursor returns the rune immediately at the cursor, or 0 if the
// cursor is at (or past) the end of the line.
func (m Model) charAfterCursor() rune {
	runes := []rune(m.buf.Line(m.cursor.Line))
	if m.cursor.Col < 0 || m.cursor.Col >= len(runes) {
		return 0
	}
	return runes[m.cursor.Col]
}

// charBeforeCursor returns the rune immediately before the cursor, or 0 if
// the cursor is at the start of the line.
func (m Model) charBeforeCursor() rune {
	runes := []rune(m.buf.Line(m.cursor.Line))
	if m.cursor.Col <= 0 || m.cursor.Col > len(runes) {
		return 0
	}
	return runes[m.cursor.Col-1]
}

// shouldAutoPair decides whether typing opener r should also insert its
// matching closer. Brackets always pair; quotes only pair when the cursor
// isn't adjacent to a word character on either side.
func (m Model) shouldAutoPair(r rune) bool {
	if !autoPairQuotes[r] {
		return true
	}
	return !isWordChar(m.charBeforeCursor()) && !isWordChar(m.charAfterCursor())
}

// insertBraceBlock expands typing '{' into a three-line block:
//
//	{
//	<indent+1 level, cursor lands here>
//	}
//
// indented to match the current line, since a '{' almost always opens a
// block whose body belongs on its own line.
func (m Model) insertBraceBlock() (Model, tea.Cmd) {
	runes := []rune(m.buf.Line(m.cursor.Line))
	indent := string(runes[:leadingWhitespace(runes)])

	op := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: "{\n" + indent + "\t\n" + indent + "}",
	}
	m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: len(indent) + 1}
	return applyOp(m, op)
}
