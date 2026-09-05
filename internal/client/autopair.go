package client

import (
	tea "charm.land/bubbletea/v2"

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
// matching closer. Quotes only pair when the cursor isn't adjacent to a
// word character on either side, and not when the cursor sits right
// before a stray, unmatched quote (see insideOpenQuote) — that quote gets
// claimed as the closer instead of nesting a redundant new pair. Brackets
// follow the same "claim, don't nest" rule via bracketBalance: pairing is
// skipped when the very next character is already an unmatched instance
// of the closer (e.g. the cursor sits right before a stray ')' with no
// '(' before it to claim it), so typing '(' before a lone ')' yields "()"
// instead of "())".
func (m Model) shouldAutoPair(r rune) bool {
	if autoPairQuotes[r] {
		if isWordChar(m.charBeforeCursor()) || isWordChar(m.charAfterCursor()) {
			return false
		}
		if m.charAfterCursor() != r {
			return true
		}
		// The next character is the same quote we're about to type. If a
		// later quote on the line matches it (countUnescapedQuotes from here
		// to end-of-line is even), it's the opener of its own complete
		// string and this is a separate pair to nest in front of it. If
		// there's no later partner (odd), it's a stray closer waiting to be
		// claimed (see insertSelfInsert's skip-over check) rather than
		// nested redundantly.
		runes := []rune(m.buf.Line(m.cursor.Line))
		col := min(m.cursor.Col, len(runes))
		return countUnescapedQuotes(runes[col:], r)%2 == 0
	}
	closer := autoPairs[r]
	if m.charAfterCursor() != closer {
		return true
	}
	content := []rune(m.buf.Content())
	offset := m.buf.RuneOffset(m.cursor.Line, m.cursor.Col)
	return bracketBalance(content, offset, r, closer) > 0
}

// insideOpenQuote reports whether an odd number of quote characters r
// precede the cursor on the current line, i.e. whether the cursor sits
// inside a string opened earlier on this line rather than next to an
// unrelated, unmatched quote character. Quotes are self-symmetric (the
// opener and closer are the same rune), so — unlike brackets — a running
// stack can't tell "the closer of the string I'm in" apart from "some
// other quote entirely"; parity of the count is the standard stand-in.
func (m Model) insideOpenQuote(r rune) bool {
	runes := []rune(m.buf.Line(m.cursor.Line))
	col := min(m.cursor.Col, len(runes))
	return countUnescapedQuotes(runes[:col], r)%2 == 1
}

// countUnescapedQuotes counts occurrences of r in runes, skipping any
// character immediately preceded by an unescaped backslash — so an escaped
// quote like the \" in "a\"" isn't mistaken for a real delimiter.
func countUnescapedQuotes(runes []rune, r rune) int {
	count := 0
	escaped := false
	for _, c := range runes {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == r {
			count++
		}
	}
	return count
}

// bracketBalance counts unmatched opener runes among content[:upTo],
// i.e. openers not yet closed by a following closer within that prefix.
// A positive result means an earlier, still-open opener is waiting for a
// closer, so the closer rune sitting right after upTo belongs to it
// rather than being "free" for a newly typed opener to claim.
func bracketBalance(content []rune, upTo int, opener, closer rune) int {
	balance := 0
	if upTo > len(content) {
		upTo = len(content)
	}
	for _, r := range content[:upTo] {
		switch r {
		case opener:
			balance++
		case closer:
			if balance > 0 {
				balance--
			}
		}
	}
	return balance
}

// shouldExpandBraceBlock decides whether typing '{' should expand into an
// indented block versus just inserting "{}" inline. It defers to the
// language's syntax tree (via the highlighter) so constructs conventionally
// written on one line, like a TypeScript import's named-import list, don't
// get split across three lines the way a function body should.
func (m Model) shouldExpandBraceBlock() bool {
	if m.hlr == nil {
		return true
	}
	return m.hlr.ShouldExpandBraceBlock([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col)
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
	indent := m.currentLineIndent()
	unit := m.indentUnit()

	op := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: "{\n" + indent + unit + "\n" + indent + "}",
	}
	m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: len(indent) + len(unit)}
	return applyOp(m, op)
}
