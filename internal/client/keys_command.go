package client

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

func (m Model) handleCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.cmdBuf = ""
		m.cmdCompletionIdx = -1

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
