package client

import (
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// isIdentifierPartChar reports whether r can appear inside a source token
// that case-convert operates on: letters, digits, and the separators used by
// snake_case/kebab-case/dot.case.
func isIdentifierPartChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
}

// findIdentifierAt returns the inclusive [start, end] column range of the
// identifier-like token on the line containing col, or found=false if col
// isn't on one.
func findIdentifierAt(runes []rune, col int) (start, end int, found bool) {
	n := len(runes)
	if n == 0 || col >= n || !isIdentifierPartChar(runes[col]) {
		return -1, -1, false
	}
	start = col
	for start > 0 && isIdentifierPartChar(runes[start-1]) {
		start--
	}
	end = col
	for end+1 < n && isIdentifierPartChar(runes[end+1]) {
		end++
	}
	return start, end, true
}

// splitCaseWords splits s into words regardless of its current case style:
// any run of non-letter/non-digit characters (_, -, ., space, newline, ...)
// is a delimiter, and camelCase/PascalCase words are further split on case
// transitions. Acronym runs are kept together except for their last letter,
// which starts the following word (XMLParser -> "XML", "Parser").
func splitCaseWords(s string) []string {
	var words []string
	var cur []rune
	runes := []rune(s)
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) == 0 {
			cur = append(cur, r)
			continue
		}
		prev := cur[len(cur)-1]
		switch {
		case unicode.IsUpper(r) && !unicode.IsUpper(prev):
			// lower/digit -> upper transition: start a new word.
			flush()
			cur = append(cur, r)
		case unicode.IsUpper(r) && unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
			// Acronym followed by a new capitalized word.
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

func capitalizeWord(w string) string {
	r := []rune(w)
	if len(r) == 0 {
		return w
	}
	return string(unicode.ToUpper(r[0])) + strings.ToLower(string(r[1:]))
}

func joinSnakeCase(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToLower(w)
	}
	return strings.Join(out, "_")
}

func joinScreamingSnakeCase(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToUpper(w)
	}
	return strings.Join(out, "_")
}

func joinKebabCase(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToLower(w)
	}
	return strings.Join(out, "-")
}

func joinDotCase(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToLower(w)
	}
	return strings.Join(out, ".")
}

func joinCamelCase(words []string) string {
	var sb strings.Builder
	for i, w := range words {
		if i == 0 {
			sb.WriteString(strings.ToLower(w))
		} else {
			sb.WriteString(capitalizeWord(w))
		}
	}
	return sb.String()
}

func joinPascalCase(words []string) string {
	var sb strings.Builder
	for _, w := range words {
		sb.WriteString(capitalizeWord(w))
	}
	return sb.String()
}

func executeCaseConvertSnake(m Model) (tea.Model, tea.Cmd) {
	return convertCaseAllCursors(m, joinSnakeCase)
}

func executeCaseConvertScreamingSnake(m Model) (tea.Model, tea.Cmd) {
	return convertCaseAllCursors(m, joinScreamingSnakeCase)
}

func executeCaseConvertKebab(m Model) (tea.Model, tea.Cmd) {
	return convertCaseAllCursors(m, joinKebabCase)
}

func executeCaseConvertDot(m Model) (tea.Model, tea.Cmd) {
	return convertCaseAllCursors(m, joinDotCase)
}

func executeCaseConvertCamel(m Model) (tea.Model, tea.Cmd) {
	return convertCaseAllCursors(m, joinCamelCase)
}

func executeCaseConvertPascal(m Model) (tea.Model, tea.Cmd) {
	return convertCaseAllCursors(m, joinPascalCase)
}

// convertCaseAllCursors replaces the text at every cursor (its selection, or
// else the identifier-like token enclosing the cursor) with its case-converted
// form. Processed back-to-front by position so earlier edits don't shift
// later cursor positions, mirroring deleteAllCursorSelections. A cursor with
// no selection and no enclosing token, or whose token is already in the
// target form, is left untouched.
func convertCaseAllCursors(m Model, join func([]string) string) (Model, tea.Cmd) {
	type entry struct {
		cursor    document.Pos
		sel       *Selection
		isPrimary bool
	}

	entries := []entry{{m.cursor, m.sel, true}}
	for _, ec := range m.extraCursors {
		entries = append(entries, entry{ec.pos, ec.sel, false})
	}

	sort.Slice(entries, func(i, j int) bool {
		ci, cj := entries[i].cursor, entries[j].cursor
		if ci.Line != cj.Line {
			return ci.Line > cj.Line
		}
		return ci.Col > cj.Col
	})

	snapBefore := m.cursorSnap()
	openedGroup := m.currentGroup == nil
	if openedGroup {
		m.currentGroup = []document.Op{}
	}

	var cmds []tea.Cmd
	type result struct {
		cursor    document.Pos
		isPrimary bool
	}
	results := make([]result, 0, len(entries))

	for _, e := range entries {
		sel := e.sel
		if sel == nil {
			runes := []rune(m.buf.Line(e.cursor.Line))
			start, end, ok := findIdentifierAt(runes, e.cursor.Col)
			if !ok {
				results = append(results, result{e.cursor, e.isPrimary})
				continue
			}
			sel = &Selection{
				Anchor: document.Pos{Line: e.cursor.Line, Col: start},
				Head:   document.Pos{Line: e.cursor.Line, Col: end},
			}
		}

		text := m.textForSelection(sel)
		converted := join(splitCaseWords(text))
		if converted == "" || converted == text {
			results = append(results, result{e.cursor, e.isPrimary})
			continue
		}

		editLine := e.cursor.Line
		selStart := sel.Anchor.Col
		selEnd := sel.Head.Col
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
		oldLen := selEnd - selStart + 1
		newLen := len([]rune(converted))
		delta := newLen - oldLen

		m.cursor = e.cursor
		m.sel = sel
		var delCmd tea.Cmd
		m, delCmd = m.deleteSelection()
		cmds = append(cmds, delCmd)

		var insCmd tea.Cmd
		m, insCmd = applyOp(m, document.Op{
			ClientID:   m.clientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: converted,
		})
		cmds = append(cmds, insCmd)

		// Translate any already-saved cursors on the same line that are to the
		// right of this edit to account for the length change.
		if delta != 0 {
			for i := range results {
				if results[i].cursor.Line == editLine && results[i].cursor.Col > selStart {
					results[i].cursor.Col += delta
				}
			}
		}

		results = append(results, result{m.cursor, e.isPrimary})
	}

	if openedGroup {
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: snapBefore})
		}
		m.currentGroup = nil
	}

	m.extraCursors = nil
	for _, r := range results {
		if r.isPrimary {
			m.cursor = r.cursor
		} else {
			m.extraCursors = append(m.extraCursors, ExtraCursor{pos: r.cursor})
		}
	}
	m.sel = nil
	m.scrollToCursor()

	return m, tea.Batch(cmds...)
}
