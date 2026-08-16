package client

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
	sig, ok := m.hlr.FunctionSignatureAt([]byte(m.buf.Content()), m.cursor.Line+1)
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
