package client

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// effectiveIndentSettings resolves the indent style/width to use for this
// buffer: the style already detected in its content takes precedence over
// configured settings, so editing an existing file stays consistent with it
// even when it differs from your own default.
func (m Model) effectiveIndentSettings() config.IndentSettings {
	if m.detectedIndent != nil {
		return *m.detectedIndent
	}
	if m.cfg == nil {
		return config.IndentSettings{Style: "tabs", Width: 4}
	}
	ext := strings.TrimPrefix(filepath.Ext(m.filePath), ".")
	return m.cfg.EffectiveIndent(ext)
}

// indentUnit returns the string to insert for one indent level.
func (m Model) indentUnit() string {
	settings := m.effectiveIndentSettings()
	if settings.Style == "spaces" {
		width := settings.Width
		if width <= 0 {
			width = 4
		}
		return strings.Repeat(" ", width)
	}
	return "\t"
}

// indentOpeners are trailing (non-whitespace) characters before the cursor
// that mean the next line should be indented one level deeper: an open
// bracket with nothing closing it yet, or a trailing ':' (function/class
// headers, conditionals, dict/YAML keys, etc. across most languages that
// use one).
var indentOpeners = map[rune]bool{
	'{': true, '(': true, '[': true, ':': true,
}

// currentLineIndent returns the leading whitespace of the cursor's line.
func (m Model) currentLineIndent() string {
	runes := []rune(m.buf.Line(m.cursor.Line))
	return string(runes[:leadingWhitespace(runes)])
}

// lastNonSpaceBefore returns the last non-whitespace rune before col in
// runes, or 0 if there isn't one.
func lastNonSpaceBefore(runes []rune, col int) rune {
	for i := min(col, len(runes)) - 1; i >= 0; i-- {
		if runes[i] != ' ' && runes[i] != '\t' {
			return runes[i]
		}
	}
	return 0
}

// bracketPairAtCursor reports the closing rune when the cursor sits
// directly between a matching, still-empty bracket pair (parens, brackets,
// or braces). Quotes are excluded: splitting a string literal onto its own
// line would usually break it rather than help.
func (m Model) bracketPairAtCursor() (rune, bool) {
	before := m.charBeforeCursor()
	if autoPairQuotes[before] {
		return 0, false
	}
	closer, ok := autoPairs[before]
	if !ok || m.charAfterCursor() != closer {
		return 0, false
	}
	return closer, true
}

// splitInsideBracketPair turns "foo(|)" into:
//
//	foo(
//		|
//	)
//
// matching the indentation of the line the pair opened on. Used when Enter
// is pressed with the cursor between an empty bracket pair, mirroring how
// every mainstream editor treats that as a request to expand the pair
// rather than just inserting a bare newline.
func (m Model) splitInsideBracketPair() (Model, tea.Cmd) {
	indent := m.currentLineIndent()
	unit := m.indentUnit()

	op := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: "\n" + indent + unit + "\n" + indent,
	}
	m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: len(indent) + len(unit)}
	return applyOp(m, op)
}

// dedentTarget consults the buffer's tree-sitter indent query (when one is
// available for its language) for the case where the next token after the
// cursor closes the block/call/group enclosing it — see
// highlight.Highlighter.DedentTarget for exactly what this does and doesn't
// cover.
func (m Model) dedentTarget() (string, bool) {
	if m.hlr == nil {
		return "", false
	}
	return m.hlr.DedentTarget([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col)
}

// handleEnter inserts a newline with semantic indentation: the same level
// as the current line by default, one level deeper after an opening
// bracket or trailing ':'; matching the enclosing block's indent instead
// when the next token closes it (see dedentTarget); and — if the cursor
// sits inside an empty bracket pair — split into an indented block instead.
func (m Model) handleEnter() (Model, tea.Cmd) {
	if _, ok := m.bracketPairAtCursor(); ok {
		return m.splitInsideBracketPair()
	}

	var indent string
	if target, ok := m.dedentTarget(); ok {
		indent = target
	} else {
		indent = m.currentLineIndent()
		runes := []rune(m.buf.Line(m.cursor.Line))
		if indentOpeners[lastNonSpaceBefore(runes, m.cursor.Col)] {
			indent += m.indentUnit()
		}
	}

	op := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: "\n" + indent,
	}
	m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: len(indent)}
	return applyOp(m, op)
}
