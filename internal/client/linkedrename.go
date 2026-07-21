package client

import (
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// defaultExtractedName maps well-known LSP CodeActionKinds to the language
// server's default name for the newly introduced symbol, used as a last
// resort when detectExtractedName can't find any candidate at all. gopls
// uses a different default per kind — "newMethod" (not "newFunction") when
// extracting into a method with a receiver.
func defaultExtractedName(kind string) string {
	switch {
	case strings.HasPrefix(kind, "refactor.extract.method"):
		return "newMethod"
	case strings.HasPrefix(kind, "refactor.extract.function"):
		return "newFunction"
	case strings.HasPrefix(kind, "refactor.extract.variable"), strings.HasPrefix(kind, "refactor.extract.constant"):
		return "newVar"
	}
	return ""
}

// identifierRE matches identifier-like tokens across the common C-family/Go
// syntax used by every language this editor supports LSP for.
var identifierRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// funcDeclRE matches a function or method declaration's name within an
// edit's inserted text: "func NAME(" or "func (recv) NAME(".
var funcDeclRE = regexp.MustCompile(`func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// detectExtractedName finds the name of a newly extracted symbol (function
// or variable) from a single code-action edit.
//
// It first looks for a function/method declared within the edit's inserted
// text (gopls always appends the new definition last, after adjusting the
// call site) — this is the strongest, unambiguous signal, and the only one
// that works reliably once the extracted code has multiple return paths:
// gopls then also introduces a second new, repeated identifier (a
// "shouldReturn"-style control-flow variable) alongside the function name,
// which would otherwise make the fallback heuristic below ambiguous.
//
// Failing that, it falls back to the identifier that appears 2+ times in
// the inserted text but didn't already exist anywhere in the buffer before
// the edit (the definition and its use site, for an extracted variable).
// Returns "" if neither finds a unique candidate.
func detectExtractedName(oldContent, newText string) string {
	if matches := funcDeclRE.FindAllStringSubmatch(newText, -1); len(matches) > 0 {
		return matches[len(matches)-1][1]
	}

	oldIdents := make(map[string]bool)
	for _, tok := range identifierRE.FindAllString(oldContent, -1) {
		oldIdents[tok] = true
	}

	counts := make(map[string]int)
	var order []string
	for _, tok := range identifierRE.FindAllString(newText, -1) {
		if counts[tok] == 0 {
			order = append(order, tok)
		}
		counts[tok]++
	}

	candidate := ""
	for _, tok := range order {
		if oldIdents[tok] || counts[tok] < 2 {
			continue
		}
		if candidate != "" {
			return "" // more than one new repeated identifier: ambiguous
		}
		candidate = tok
	}
	return candidate
}

// findWholeWordOccurrences returns the position of every whole-word
// occurrence of word in the buffer, in document order.
func findWholeWordOccurrences(m Model, word string) []document.Pos {
	wordRunes := []rune(word)
	n := len(wordRunes)
	if n == 0 {
		return nil
	}
	var out []document.Pos
	for line := 0; line < m.buf.LineCount(); line++ {
		lineRunes := []rune(m.buf.Line(line))
		for col := 0; col+n <= len(lineRunes); col++ {
			if col > 0 && isWordChar(lineRunes[col-1]) {
				continue
			}
			if col+n < len(lineRunes) && isWordChar(lineRunes[col+n]) {
				continue
			}
			match := true
			for i := 0; i < n; i++ {
				if lineRunes[col+i] != wordRunes[i] {
					match = false
					break
				}
			}
			if match {
				out = append(out, document.Pos{Line: line, Col: col})
			}
		}
	}
	return out
}

// pendingExtractRename is stashed on Model while the user is prompted for a
// new name after selecting a range-extract code action.
type pendingExtractRename struct {
	edits []ClientLspEdit
	kind  string
}

// startExtractRenamePrompt stashes a range-extract action's edits and drops
// into the command line pre-filled with "extract-rename ", ready for the
// user to type the new symbol's name — resolved in executeCommand. No
// buffer change happens until they submit, so they never see the language
// server's default ("newFunction"/"newVar") name.
func startExtractRenamePrompt(m Model, edits []ClientLspEdit, kind string) (Model, tea.Cmd) {
	m.pendingExtract = &pendingExtractRename{edits: edits, kind: kind}
	m.mode = ModeCommand
	m.cmdBuf = "extract-rename "
	m.cmdCompletionIdx = -1
	return m, nil
}
