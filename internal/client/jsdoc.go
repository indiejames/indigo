package client

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// jsdocExtensions is the set of file extensions that use /** */ JSDoc
// comments — gates the /** + Enter template expansion below so it doesn't
// fire on languages (e.g. Go) whose function node types collide with JS/TS's
// in tsFunctionTypes but use a different doc-comment convention.
var jsdocExtensions = map[string]bool{
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
}

// tryExpandJSDoc expands a lone "/**" line into a full JSDoc template —
// summary line, one "@param" per parameter, and "@returns" when the function
// has a non-void return type — when Enter is pressed at the end of that
// line and it sits directly above a JS/TS function declaration, matching the
// trigger VS Code uses. ok is false (caller falls through to a plain
// newline) unless both conditions hold.
func (m Model) tryExpandJSDoc() (Model, tea.Cmd, bool) {
	if m.hlr == nil || !jsdocExtensions[strings.ToLower(filepath.Ext(m.filePath))] {
		return m, nil, false
	}
	line := m.buf.Line(m.cursor.Line)
	if strings.TrimSpace(line) != "/**" || m.cursor.Col != len([]rune(line)) {
		return m, nil, false
	}
	// The "/**" line itself is an unclosed block comment as far as the
	// parser is concerned — left in place, tree-sitter treats it as open and
	// swallows everything up to the next "*/" anywhere later in the file
	// (e.g. an existing JSDoc block on a function further down), hiding the
	// function declaration we're actually looking for. Blank it out (keeping
	// the line break, so later rows don't shift) before parsing.
	sig, ok := m.hlr.FunctionSignatureAt(blankLine(m.buf.Content(), m.cursor.Line), m.cursor.Line+1)
	if !ok {
		return m, nil, false
	}

	indent := string([]rune(line)[:leadingWhitespace([]rune(line))])
	text := buildJSDocTemplate(indent, sig)

	op := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: text,
	}
	// The template's first content line (where the cursor lands, ready for a
	// summary) is one line below the "/**" line.
	m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: len([]rune(indent)) + 3} // past " * "
	m2, cmd := applyOp(m, op)
	return m2, cmd, true
}

// blankLine returns content with the given 0-based line's text replaced by
// an equal-width blank, preserving every line break so line numbers for the
// rest of the file are unaffected.
func blankLine(content string, line int) []byte {
	lines := strings.Split(content, "\n")
	if line < 0 || line >= len(lines) {
		return []byte(content)
	}
	lines[line] = strings.Repeat(" ", len([]rune(lines[line])))
	return []byte(strings.Join(lines, "\n"))
}

// buildJSDocTemplate renders a JSDoc comment block for sig, each line
// prefixed with indent to match the "/**" line it replaces. The opening
// "/**" is not included (the caller's edit starts right after it); the
// returned text always starts with "\n" and ends with the closing "*/" line
// (no trailing newline).
func buildJSDocTemplate(indent string, sig highlight.JSDocSignature) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(indent)
	b.WriteString(" * ")
	for _, p := range sig.Params {
		b.WriteString("\n")
		b.WriteString(indent)
		b.WriteString(" * @param ")
		if p.Type != "" {
			b.WriteString("{" + p.Type + "} ")
		}
		b.WriteString(p.Name)
	}
	if sig.ReturnType != "" {
		b.WriteString("\n")
		b.WriteString(indent)
		b.WriteString(" * @returns {" + sig.ReturnType + "}")
	}
	b.WriteString("\n")
	b.WriteString(indent)
	b.WriteString(" */")
	return b.String()
}
