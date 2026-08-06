package client

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

func (m Model) handleCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.cmdBuf = ""
		m.cmdCompletionIdx = -1
		m.pendingExtract = nil

	case "tab", "down":
		matches := filteredCmds(m.cmdBuf)
		if len(matches) > 0 {
			if m.cmdCompletionIdx < 0 {
				m.cmdCompletionIdx = 0
			} else {
				m.cmdCompletionIdx = (m.cmdCompletionIdx + 1) % len(matches)
			}
		}

	case "shift+tab", "up":
		matches := filteredCmds(m.cmdBuf)
		if len(matches) > 0 {
			if m.cmdCompletionIdx < 0 {
				m.cmdCompletionIdx = len(matches) - 1
			} else {
				m.cmdCompletionIdx = (m.cmdCompletionIdx - 1 + len(matches)) % len(matches)
			}
		}

	case "enter":
		// If a completion item is selected, act on it.
		if m.cmdCompletionIdx >= 0 {
			matches := filteredCmds(m.cmdBuf)
			if m.cmdCompletionIdx < len(matches) {
				sel := matches[m.cmdCompletionIdx]
				m.cmdCompletionIdx = -1
				if sel.needsArgs {
					// Fill command name and stay in command mode for argument input.
					m.cmdBuf = sel.name + " "
					return m, nil
				}
				m.cmdBuf = sel.name
				return m.executeCommand()
			}
		}
		return m.executeCommand()

	case "backspace":
		runes := []rune(m.cmdBuf)
		if len(runes) > 0 {
			m.cmdBuf = string(runes[:len(runes)-1])
			m.cmdCompletionIdx = -1
		} else {
			m.mode = ModeNormal
			m.cmdCompletionIdx = -1
		}

	default:
		if len(msg.Runes) > 0 {
			m.cmdBuf += string(msg.Runes)
			m.cmdCompletionIdx = -1 // reset selection whenever the filter changes
		}
	}
	return m, nil
}

func (m Model) handleSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.cursor = m.searchOrigin
		m.scrollToCursor()
		m.searchQuery = ""
		m.searchMatches = nil
		m.searchIdx = -1
		m.searchErr = ""
	case "enter":
		m.mode = ModeNormal
		m.searchErr = ""
		if len(m.searchMatches) > 0 && m.searchIdx >= 0 {
			sm := m.searchMatches[m.searchIdx]
			m.cursor = document.Pos{Line: sm.line, Col: sm.col}
			m.scrollToCursor()
		}
	case "backspace":
		runes := []rune(m.searchQuery)
		if len(runes) > 0 {
			m.searchQuery = string(runes[:len(runes)-1])
			m.updateSearch()
		} else {
			m.mode = ModeNormal
			m.cursor = m.searchOrigin
			m.scrollToCursor()
			m.searchMatches = nil
			m.searchIdx = -1
			m.searchErr = ""
		}
	default:
		if len(msg.Runes) > 0 {
			m.searchQuery += string(msg.Runes)
			m.updateSearch()
		}
	}
	return m, nil
}

// updateSearch recomputes matches for the current searchQuery and advances the
// cursor to the first match at or after searchOrigin.
func (m *Model) updateSearch() {
	matches, err := findMatches(m.buf, m.searchQuery)
	if err != nil {
		m.searchErr = err.Error()
		m.searchMatches = nil
		m.searchIdx = -1
		return
	}
	m.searchErr = ""
	m.searchMatches = matches
	m.searchIdx = matchIdxAtOrAfter(matches, m.searchOrigin.Line, m.searchOrigin.Col)
	if m.searchIdx >= 0 {
		sm := matches[m.searchIdx]
		m.cursor = document.Pos{Line: sm.line, Col: sm.col}
		m.scrollToCursor()
	}
}

func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	cmd := strings.TrimSpace(m.cmdBuf)
	m.mode = ModeNormal
	m.cmdBuf = ""

	// :s/pattern/replacement/ — substitute, scoped to the active selection
	// (if any) or the whole buffer otherwise. Checked before the "s"/"save"
	// exact-match case below, since "s/..." never equals the bare string "s".
	if rest, ok := strings.CutPrefix(cmd, "s"); ok {
		if pattern, replacement, ok := parseSubstitute(rest); ok {
			return m.doSubstitute(pattern, replacement)
		}
	}

	// :grep/:find [pattern] [glob] — workspace search; falls back to current search query.
	// The optional trailing token is treated as a file glob if it contains *, ?, [ or ends with /.
	var searchRest string
	var doSearch bool
	if rest, ok := strings.CutPrefix(cmd, "grep"); ok {
		searchRest, doSearch = rest, true
	} else if rest, ok := strings.CutPrefix(cmd, "find"); ok {
		searchRest, doSearch = rest, true
	}
	if doSearch {
		pattern, glob := parseGrepArgs(strings.TrimSpace(searchRest))
		if pattern == "" {
			if m.searchQuery != "" {
				pattern = m.searchQuery
			} else {
				// e.g. ":grep *.go" with no prior query — treat glob as literal pattern.
				pattern, glob = glob, ""
			}
		}
		return m, func() tea.Msg { return GrepMsg{Pattern: pattern, Glob: glob} }
	}

	// Bare number → go to line.
	if n, err := strconv.Atoi(cmd); err == nil {
		lc := m.displayLineCount()
		if n < 1 || n > lc {
			m.status = fmt.Sprintf("E: line %d out of range (1-%d)", n, lc)
			return m, nil
		}
		m.cursor = document.Pos{Line: n - 1, Col: 0}
		m.scrollToCursor()
		return m, nil
	}

	switch cmd {
	case "fmt", "format":
		return m, m.fetchFormat(false)
	case "w", "write", "s", "save":
		return m, m.doSave()
	case "q", "quit":
		if m.buf.Dirty() {
			m.status = "E: unsaved changes (use :q! to discard)"
			return m, nil
		}
		return m, m.doCloseBuffer()
	case "q!", "quit!":
		return m, m.doCloseBuffer()
	case "wq", "x", "write-quit":
		return m, m.doSaveAndClose()
	case "qa", "quit-all":
		return m, func() tea.Msg { return QuitAllMsg{} }
	case "qa!", "quit-all!":
		return m, func() tea.Msg { return QuitAllMsg{Force: true} }
	case "wqa":
		return m, func() tea.Msg { return QuitAllMsg{SaveAll: true} }
	case "e", "edit":
		return m, func() tea.Msg { return OpenPickerMsg{} }
	case "new":
		return m, func() tea.Msg { return OpenNewFileMsg{} }
	case "metrics":
		if m.metrics != nil {
			m.metrics.show = !m.metrics.show
		}
	default:
		if path, ok := strings.CutPrefix(cmd, "w "); ok && strings.TrimSpace(path) != "" {
			newPath, err := filepath.Abs(strings.TrimSpace(path))
			if err != nil {
				m.status = fmt.Sprintf("E: bad path: %v", err)
				return m, nil
			}
			return m, m.doSaveAsNow(newPath, false)
		}
		if path, ok := strings.CutPrefix(cmd, "wq "); ok && strings.TrimSpace(path) != "" {
			newPath, err := filepath.Abs(strings.TrimSpace(path))
			if err != nil {
				m.status = fmt.Sprintf("E: bad path: %v", err)
				return m, nil
			}
			return m, m.doSaveAsNow(newPath, true)
		}
		if newName, ok := strings.CutPrefix(cmd, "extract-rename "); ok && strings.TrimSpace(newName) != "" && m.pendingExtract != nil {
			p := m.pendingExtract
			m.pendingExtract = nil
			return m.doApplyExtractAndRename(p.edits, p.kind, strings.TrimSpace(newName))
		}
		m.pendingExtract = nil
		if newName, ok := strings.CutPrefix(cmd, "rename "); ok && strings.TrimSpace(newName) != "" {
			return m, m.doRenameSymbol(strings.TrimSpace(newName))
		}
		if destPath, ok := strings.CutPrefix(cmd, "move-to-file "); ok && strings.TrimSpace(destPath) != "" {
			return m, m.doMoveFunctionToFile(strings.TrimSpace(destPath))
		}
		if rest, ok := strings.CutPrefix(cmd, "set ft="); ok {
			lang := strings.TrimSpace(rest)
			if lang == "" {
				m.status = "E: usage: set ft=<lang>"
				return m, nil
			}
			hlr := highlight.NewForKey(lang)
			if hlr == nil {
				m.status = fmt.Sprintf("E: unknown file type: %s", lang)
				return m, nil
			}
			m.hlr = hlr
			m.hlSpans = nil
			m.status = fmt.Sprintf("File type: %s", lang)
			return m, m.reparseHighlight()
		}
		m.status = fmt.Sprintf("E: unknown command: %s", cmd)
	}
	return m, nil
}

// withClearedSearch resets all within-buffer search state, removing match
// highlights and disabling n/N navigation until the next search.
func (m Model) withClearedSearch() Model {
	m.searchQuery = ""
	m.searchMatches = nil
	m.searchIdx = -1
	m.searchErr = ""
	return m
}

// parseGrepArgs splits a grep command argument into a pattern and an optional
// file glob. The trailing token is treated as a glob when it contains *, ?, [
// or ends with / (directory filter). Everything else is the search pattern.
func parseGrepArgs(s string) (pattern, glob string) {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		last := s[i+1:]
		if strings.ContainsAny(last, "*?[") || strings.HasSuffix(last, "/") {
			return strings.TrimSpace(s[:i]), last
		}
	}
	return s, ""
}

// parseSubstitute parses the argument to :s/pattern/replacement/ — the text
// immediately after "s" (no space). Delimiters are '/'; a literal '/' inside
// pattern or replacement is written as '\/'. ok is false when rest doesn't
// start with '/', or has fewer than two delimited parts — in particular for
// every other command starting with "s" (bare "s", "save", "set ft=...").
func parseSubstitute(rest string) (pattern, replacement string, ok bool) {
	if len(rest) == 0 || rest[0] != '/' {
		return "", "", false
	}
	parts := splitUnescaped(rest[1:], '/')
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// splitUnescaped splits s on sep, treating "\"+sep as a literal, unescaped
// sep character rather than a delimiter. Other backslash sequences (e.g. a
// regex pattern's \d) are left untouched.
func splitUnescaped(s string, sep rune) []string {
	var parts []string
	var cur strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && runes[i+1] == sep {
			cur.WriteRune(sep)
			i++
			continue
		}
		if r == sep {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	parts = append(parts, cur.String())
	return parts
}

// doSubstitute replaces every match of pattern with replacement (regex
// backreferences already expanded per match — see findSubstituteMatches),
// scoped to the active selection if one exists, or the whole buffer
// otherwise. All replacements apply as a single undo entry. The selection
// (if any) is cleared afterward, since its endpoints no longer necessarily
// bound anything meaningful once the text has changed.
func (m Model) doSubstitute(pattern, replacement string) (tea.Model, tea.Cmd) {
	var bounds *substituteBounds
	hadSelection := m.sel != nil
	if m.sel != nil {
		from, to := m.sel.ordered()
		bounds = &substituteBounds{from: from, to: to}
	}

	matches, err := findSubstituteMatches(m.buf, pattern, replacement, bounds)
	if err != nil {
		m.status = "E: " + err.Error()
		return m, nil
	}
	if len(matches) == 0 {
		m.status = "E: pattern not found"
		return m, nil
	}
	if hadSelection {
		m.sel = nil
	}

	// Apply bottom-to-top so an earlier-in-buffer match's coordinates are
	// never shifted by a not-yet-applied later one.
	ops := make([]document.Op, 0, len(matches)*2)
	for i := len(matches) - 1; i >= 0; i-- {
		mt := matches[i]
		ops = append(ops,
			document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: mt.line, FromCol: mt.col,
				ToLine: mt.line, ToCol: mt.col + mt.length,
			},
			document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: mt.line, InsertCol: mt.col,
				InsertText: mt.replacement,
			},
		)
	}

	m2, cmd := applyBatch(m, ops)
	first := matches[0]
	m2.cursor = document.Pos{Line: first.line, Col: first.col}
	m2.scrollToCursor()
	m2.status = fmt.Sprintf("%d substitution(s)", len(matches))
	return m2, cmd
}
