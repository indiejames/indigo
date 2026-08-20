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
		m = m.withClearedSearch()
	case "enter":
		// A second, unescaped '/' switches search into a live
		// search-and-replace preview (see splitSearchQuery) — Enter there
		// commits it. Plain search just confirms the current match position,
		// same as always.
		if m.searchReplacing {
			return m.applySearchReplace()
		}
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
			m = m.withClearedSearch()
		}
	default:
		if len(msg.Runes) > 0 {
			m.searchQuery += string(msg.Runes)
			m.updateSearch()
		}
	}
	return m, nil
}

// updateSearch reparses searchQuery (see splitSearchQuery) and recomputes
// matches — and, once a search-and-replace delimiter has been typed, each
// match's replacement text — scoped to the active selection if one exists,
// or the whole buffer otherwise. Advances the cursor to the first match at
// or after searchOrigin.
func (m *Model) updateSearch() {
	pattern, replacement, isReplace := splitSearchQuery(m.searchQuery)
	m.searchReplace = replacement
	m.searchReplacing = isReplace

	var bounds *substituteBounds
	if m.sel != nil {
		from, to := m.sel.ordered()
		bounds = &substituteBounds{from: from, to: to}
	}

	repl := ""
	if isReplace {
		repl = replacement
	}
	matches, err := findSubstituteMatches(m.buf, pattern, repl, bounds)
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

// applySearchReplace commits the current search-and-replace preview
// (Enter while searchReplacing): every previewed match's replacement is
// applied as a single undo entry, bottom-to-top so an earlier match's
// coordinates are never shifted by a not-yet-applied later one. The
// selection that scoped the preview (if any) is cleared afterward, since
// its endpoints no longer necessarily bound anything meaningful once the
// text has changed.
func (m Model) applySearchReplace() (tea.Model, tea.Cmd) {
	matches := m.searchMatches
	hadSelection := m.sel != nil
	m.mode = ModeNormal

	if len(matches) == 0 {
		m = m.pushStatus("E: pattern not found")
		return m.withClearedSearch(), nil
	}
	if hadSelection {
		m.sel = nil
	}

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
	m2 = m2.pushStatus(fmt.Sprintf("%d substitution(s)", len(matches)))
	return m2.withClearedSearch(), cmd
}

func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	cmd := strings.TrimSpace(m.cmdBuf)
	m.mode = ModeNormal
	m.cmdBuf = ""

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
		trimmed := strings.TrimSpace(searchRest)
		pattern, include, exclude := parseGrepArgs(trimmed)
		if pattern == "" {
			if m.searchQuery != "" {
				pattern, _, _ = splitSearchQuery(m.searchQuery)
			} else {
				// e.g. ":grep *.go" with no prior query — treat the whole
				// argument as a literal pattern rather than a bare filter.
				pattern, include, exclude = trimmed, "", ""
			}
		}
		return m, func() tea.Msg { return GrepMsg{Pattern: pattern, Include: include, Exclude: exclude} }
	}

	// :diagnostics/:diag — workspace diagnostic browser (open buffers only
	// for now; see PLAN.md's workspace-scan follow-up).
	if cmd == "diagnostics" || cmd == "diag" {
		return m, func() tea.Msg { return OpenDiagnosticBrowserMsg{} }
	}

	// Bare number → go to line.
	if n, err := strconv.Atoi(cmd); err == nil {
		lc := m.displayLineCount()
		if n < 1 || n > lc {
			m = m.pushStatus(fmt.Sprintf("E: line %d out of range (1-%d)", n, lc))
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
			m = m.pushStatus("E: unsaved changes (use :q! to discard)")
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
				m = m.pushStatus(fmt.Sprintf("E: bad path: %v", err))
				return m, nil
			}
			return m, m.doSaveAsNow(newPath, false)
		}
		if path, ok := strings.CutPrefix(cmd, "wq "); ok && strings.TrimSpace(path) != "" {
			newPath, err := filepath.Abs(strings.TrimSpace(path))
			if err != nil {
				m = m.pushStatus(fmt.Sprintf("E: bad path: %v", err))
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
				m = m.pushStatus("E: usage: set ft=<lang>")
				return m, nil
			}
			hlr := highlight.NewForKey(lang)
			if hlr == nil {
				m = m.pushStatus(fmt.Sprintf("E: unknown file type: %s", lang))
				return m, nil
			}
			m.hlr = hlr
			m.hlSpans = nil
			m = m.pushStatus(fmt.Sprintf("File type: %s", lang))
			return m, m.reparseHighlight()
		}
		m = m.pushStatus(fmt.Sprintf("E: unknown command: %s", cmd))
	}
	return m, nil
}

// withClearedSearch resets all within-buffer search state, removing match
// highlights and disabling n/N navigation until the next search.
func (m Model) withClearedSearch() Model {
	m.searchQuery = ""
	m.searchReplace = ""
	m.searchReplacing = false
	m.searchMatches = nil
	m.searchIdx = -1
	m.searchErr = ""
	return m
}

// parseGrepArgs splits a grep command argument into a pattern and optional
// include/exclude file globs. Trailing tokens are peeled off one at a time
// while they look like a glob (contain *, ?, [ or end with / for a directory
// filter); a token prefixed with ! is an exclude, otherwise an include.
// Peeling stops at the first trailing token that doesn't look like a glob —
// everything before that is the search pattern. Multiple include/exclude
// tokens are joined with a space (see splitGlobs).
func parseGrepArgs(s string) (pattern, include, exclude string) {
	rest := s
	var includes, excludes []string
	for {
		trimmed := strings.TrimRight(rest, " ")
		i := strings.LastIndexByte(trimmed, ' ')
		last := trimmed[i+1:]
		if last == "" {
			rest = trimmed
			break
		}
		raw := strings.TrimPrefix(last, "!")
		if !strings.ContainsAny(raw, "*?[") && !strings.HasSuffix(raw, "/") {
			rest = trimmed
			break
		}
		if strings.HasPrefix(last, "!") {
			excludes = append([]string{raw}, excludes...)
		} else {
			includes = append([]string{last}, includes...)
		}
		rest = trimmed[:i+1]
	}
	pattern = strings.TrimSpace(rest)
	return pattern, strings.Join(includes, " "), strings.Join(excludes, " ")
}
