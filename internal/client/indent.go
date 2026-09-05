package client

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// effectiveIndentSettings resolves the indent style/width to use for this
// buffer: an explicit ":set ft=<key>" override (see Model.langOverride)
// takes precedence over everything, since it's the user overruling
// auto-detection on purpose; short of that, the style already detected in
// the buffer's content takes precedence over configured settings, so
// editing an existing file stays consistent with it even when it differs
// from your own default.
func (m Model) effectiveIndentSettings() config.IndentSettings {
	if m.langOverride == "" && m.detectedIndent != nil {
		return *m.detectedIndent
	}
	if m.cfg == nil {
		return config.IndentSettings{Style: "tabs", Width: 4}
	}
	ext := strings.TrimPrefix(filepath.Ext(m.filePath), ".")
	if m.langOverride != "" {
		ext = strings.ToLower(strings.TrimPrefix(m.langOverride, "."))
	}
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

// tabInsertText returns what a literal Tab keypress should insert at
// (line, col): a single tab character in "tabs" style (unambiguous — a tab
// is its own stop), or, in "spaces" style, enough spaces to reach the next
// tab stop measured in on-screen visual columns (VS Code-style) — anywhere
// from 1 to width spaces, collapsing to a full indentUnit()-width only when
// col already sits on a stop (e.g. line start). Unlike indentUnit(), which
// builds a fresh indent string from column 0, this accounts for the
// existing content (including any tabs) already on the line before col.
func (m Model) tabInsertText(line, col int) string {
	settings := m.effectiveIndentSettings()
	if settings.Style != "spaces" {
		return "\t"
	}
	width := settings.Width
	if width <= 0 {
		width = 4
	}
	runes := []rune(m.buf.Line(line))
	_, colMap := expandTabsRemap(runes)
	visualCol := colMap[len(colMap)-1]
	if col < len(colMap) {
		visualCol = colMap[col]
	}
	spaces := width - (visualCol % width)
	return strings.Repeat(" ", spaces)
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

// dedentTargetAt consults the buffer's tree-sitter indent query (when one
// is available for its language) for the case where the next token at
// (line, col) — skipping same-line whitespace — closes the block/call/group
// enclosing it — see highlight.Highlighter.DedentTarget for exactly what
// this does and doesn't cover.
func (m Model) dedentTargetAt(line, col int) (string, bool) {
	if m.hlr == nil {
		return "", false
	}
	return m.hlr.DedentTarget([]byte(m.buf.Content()), line, col)
}

// contextIndent returns the indentation appropriate for content inserted
// into prevLine (the whole line immediately before it) at prevCol, on
// buffer line prevLineNum (pass -1 when prevLine isn't a real buffer line,
// e.g. it was synthesized as empty for the top of the file): matching the
// enclosing block's indent when trailing content on that same line, after
// prevCol, closes it (see dedentTarget) — the narrow case of splicing a new
// line in right before an already-typed closer — one level deeper than
// prevLine after an opening bracket or trailing ':', otherwise prevLine's
// own indent. This is the same rule handleEnter uses for a freshly typed
// line, generalized to a position that isn't necessarily the cursor's.
//
// Deliberately does not look at what follows on a *different* line (e.g.
// the line a moved-down block will land in front of): dedentTarget only
// answers "does trailing same-line content close an enclosing block", and
// stretching it across a line boundary would dedent content down to the
// enclosing scope's own level even when a sibling statement right above
// already establishes the correct (deeper) body indent.
func (m Model) contextIndent(prevLine string, prevLineNum, prevCol int) string {
	if prevLineNum >= 0 {
		if target, ok := m.dedentTargetAt(prevLineNum, prevCol); ok {
			return target
		}
	}
	runs := []rune(prevLine)
	indent := string(runs[:leadingWhitespace(runs)])
	if indentOpeners[lastNonSpaceBefore(runs, prevCol)] {
		indent += m.indentUnit()
	}
	return indent
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
	if m2, cmd, ok := m.tryExpandJSDoc(); ok {
		return m2, cmd
	}

	indent := m.contextIndent(m.buf.Line(m.cursor.Line), m.cursor.Line, m.cursor.Col)

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

// blockBaseIndent returns the leading whitespace of the *least*-indented
// non-blank line in lines, used as the reference point a moved or pasted
// block's indentation is shifted relative to: lines deeper than this keep
// their extra nesting, lines at this level land exactly on target. The
// first non-blank line isn't necessarily representative — e.g. a wrapped
// import's opening line sits at the block's own level while its middle
// line is nested one deeper — so every line is checked, not just the
// first. Blank lines are skipped so an empty leading line in the block
// doesn't masquerade as zero indentation; ok is false when every line is
// blank, since there's then nothing to anchor a shift to.
func blockBaseIndent(lines []string) (indent string, ok bool) {
	best := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		runs := []rune(l)
		n := leadingWhitespace(runs)
		if best == -1 || n < best {
			best = n
			indent = string(runs[:n])
			ok = true
		}
	}
	return indent, ok
}

// reindentLines replaces the leading whitespace (baseline) of every non-blank
// line in lines with target, preserving each line's indentation relative to the
// baseline. Blank lines are left untouched. Returns both the reindented lines
// and the column delta (in runes) applied to non-blank lines.
func reindentLines(lines []string, baseline, target string) ([]string, int) {
	if len(lines) == 0 {
		return lines, 0
	}
	baseRunes := []rune(baseline)
	targetRunes := []rune(target)
	delta := len(targetRunes) - len(baseRunes)
	out := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			out[i] = line
			continue
		}
		runs := []rune(line)
		n := leadingWhitespace(runs)
		// Find how much of the baseline prefix this line actually has.
		prefixLen := 0
		for prefixLen < len(baseRunes) && prefixLen < n && runs[prefixLen] == baseRunes[prefixLen] {
			prefixLen++
		}
		// Replace the baseline prefix with target, keeping the rest.
		suffix := runs[prefixLen:]
		out[i] = target + string(suffix)
	}
	return out, delta
}
