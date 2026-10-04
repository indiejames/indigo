package client

import (
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// Markdown list continuation: pressing Enter at the end of a list item starts
// the next one, and pressing it on an item with no content ends the list.
//
// Markdown only, because the markers are ordinary characters everywhere else —
// a line starting with "*" is a comment continuation in C and a glob in a
// shell script, and continuing it there would be wrong.

// markdownExtensions are the file types this applies to, keyed the way
// isMarkdown compares them: lowercase, with the leading dot.
var markdownExtensions = map[string]bool{".md": true, ".markdown": true}

// isMarkdown reports whether this buffer is markdown.
//
// An explicit ":set ft=md" wins over the path, the same precedence
// effectiveIndentSettings uses, so a CHANGELOG or a file with no extension
// gets list continuation once its type is set. The override is a bare
// language key ("md"), not an extension, hence the dot handling.
func (m Model) isMarkdown() bool {
	if m.langOverride != "" {
		return markdownExtensions["."+strings.ToLower(strings.TrimPrefix(m.langOverride, "."))]
	}
	return markdownExtensions[strings.ToLower(filepath.Ext(m.filePath))]
}

// markdownList is a list item parsed off the start of a line.
type markdownList struct {
	indent  string // leading whitespace, repeated on the next item
	bullet  string // "-", "*" or "+"; empty for an ordered item
	number  int    // ordered item's number; 0 for a bullet
	delim   string // "." or ")" for an ordered item
	gap     string // whitespace between marker and content, repeated as typed
	task    bool   // the item is a "- [ ]" / "- [x]" checkbox
	content string // everything after the marker (and checkbox), may be empty
}

// marker renders the marker this item's *successor* should carry: the same
// bullet, or the next number for an ordered list. A checkbox item continues as
// an unchecked one — continuing "[x]" as "[x]" would tick a box nobody has
// done yet.
func (l markdownList) nextMarker() string {
	var b strings.Builder
	b.WriteString(l.indent)
	if l.bullet != "" {
		b.WriteString(l.bullet)
	} else {
		b.WriteString(strconv.Itoa(l.number + 1))
		b.WriteString(l.delim)
	}
	b.WriteString(l.gap)
	if l.task {
		b.WriteString("[ ] ")
	}
	return b.String()
}

// parseMarkdownList reads a list marker off the start of line, if there is
// one. A marker must be followed by whitespace or end the line: "*italic*" and
// "1.5" are not list items.
func parseMarkdownList(line string) (markdownList, bool) {
	runes := []rune(line)
	n := leadingWhitespace(runes)
	var l markdownList
	l.indent = string(runes[:n])
	rest := runes[n:]
	if len(rest) == 0 {
		return markdownList{}, false
	}

	switch {
	case rest[0] == '-' || rest[0] == '*' || rest[0] == '+':
		l.bullet = string(rest[0])
		rest = rest[1:]
	default:
		d := 0
		for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
			d++
		}
		// A number needs a delimiter after it, and a cap: nobody writes a
		// thousand-item list by hand, and "2026." opening a line is a date.
		if d == 0 || d > 3 || d >= len(rest) || (rest[d] != '.' && rest[d] != ')') {
			return markdownList{}, false
		}
		num, err := strconv.Atoi(string(rest[:d]))
		if err != nil {
			return markdownList{}, false
		}
		l.number, l.delim = num, string(rest[d])
		rest = rest[d+1:]
	}

	// Whitespace (or end of line) is what separates a marker from its text.
	g := 0
	for g < len(rest) && (rest[g] == ' ' || rest[g] == '\t') {
		g++
	}
	if g == 0 && len(rest) > 0 {
		return markdownList{}, false // "-text" / "1.text": not a list item
	}
	l.gap = string(rest[:g])
	if l.gap == "" {
		l.gap = " " // marker alone on the line; the next item still needs a space
	}
	rest = rest[g:]

	// A checkbox is part of the marker rather than content, so that an item
	// with a box but no text counts as empty and ends the list.
	if len(rest) >= 3 && rest[0] == '[' && rest[2] == ']' &&
		(rest[1] == ' ' || rest[1] == 'x' || rest[1] == 'X') {
		l.task = true
		rest = rest[3:]
		for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
			rest = rest[1:]
		}
	}

	l.content = strings.TrimRight(string(rest), " \t")
	return l, true
}

// tryContinueMarkdownList handles Enter inside a markdown list, reporting
// whether it did.
//
// Two cases, and the second is what makes a list exitable at all:
//
//   - an item with content continues: the next line opens with the same
//     indentation and bullet, or the next number.
//   - an item with no content ends the list: its marker is deleted and the
//     cursor stays on the now-blank line, so a second Enter is an ordinary
//     blank line. Without this, every list would be a trap.
//
// Only with the cursor at the end of the line (trailing whitespace aside).
// Enter in the middle of an item falls through to the ordinary indent-aware
// newline, which splits the text without inventing a marker for it.
func (m Model) tryContinueMarkdownList() (Model, tea.Cmd, bool) {
	if !m.isMarkdown() {
		return m, nil, false
	}
	line := m.buf.Line(m.cursor.Line)
	runes := []rune(line)
	if m.cursor.Col < len(strings.TrimRight(line, " \t")) {
		return m, nil, false // mid-item: not ours
	}
	l, ok := parseMarkdownList(line)
	if !ok {
		return m, nil, false
	}

	if l.content == "" {
		// Delete the marker, keeping the indentation: the line becomes blank
		// and the list is over.
		op := document.Op{
			ClientID: m.clientID(),
			Type:     document.OpDelete,
			FromLine: m.cursor.Line,
			FromCol:  len([]rune(l.indent)),
			ToLine:   m.cursor.Line,
			ToCol:    len(runes),
		}
		m.cursor = document.Pos{Line: m.cursor.Line, Col: len([]rune(l.indent))}
		m2, cmd := applyOp(m, op)
		return m2, cmd, true
	}

	next := l.nextMarker()
	op := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: "\n" + next,
	}
	m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: len([]rune(next))}
	m2, cmd := applyOp(m, op)
	return m2, cmd, true
}
