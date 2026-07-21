package app

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (a App) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return pickerCancelledMsg{} }

	case "enter":
		if a.picker.showingRecent() {
			if path := a.picker.selectedPath(); path != "" {
				return a, func() tea.Msg { return pickedMsg{absPath: path} }
			}
			return a, nil
		}
		if a.picker.browseMode() {
			e := a.picker.selectedEntry()
			if e == nil {
				return a, nil
			}
			if e.name == ".." {
				a.picker.navigateUp()
				return a, nil
			}
			if e.isDir {
				a.picker.navigateInto(e.name)
				return a, nil
			}
			// File selected in browse mode.
			if path := a.picker.selectedPath(); path != "" {
				return a, func() tea.Msg { return pickedMsg{absPath: path} }
			}
		} else {
			// Search mode.
			if path := a.picker.selectedPath(); path != "" {
				return a, func() tea.Msg { return pickedMsg{absPath: path} }
			}
			// No match — treat the raw query as a direct path.
			q := strings.TrimSpace(a.picker.query)
			if q == "" {
				return a, nil
			}
			if !filepath.IsAbs(q) {
				q = filepath.Join(a.workDir, q)
			}
			return a, func() tea.Msg { return pickedMsg{absPath: q} }
		}

	case "up", "ctrl+p":
		a.picker.moveUp()
	case "down", "ctrl+n":
		a.picker.moveDown()

	case "tab":
		if a.picker.browseMode() && len(a.picker.recentFiles) > 0 {
			a.picker.recentMode = !a.picker.recentMode
			a.picker.cursor = 0
		}

	case "backspace":
		q := []rune(a.picker.query)
		if len(q) > 0 {
			a.picker.setQuery(string(q[:len(q)-1]))
		} else {
			// Query already empty: go up one directory level.
			a.picker.navigateUp()
		}

	default:
		if len(msg.Runes) > 0 {
			a.picker.setQuery(a.picker.query + string(msg.Runes))
		}
	}
	return a, nil
}

// handleBufPickerKey routes key events to the buffer picker popup.
func (a App) handleBufPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return bufPickerCancelledMsg{} }
	case "enter":
		idx := a.bufPicker.selected()
		return a, func() tea.Msg { return bufPickedMsg{idx: idx} }
	case "up", "k":
		a.bufPicker.moveUp()
	case "down", "j":
		a.bufPicker.moveDown()
	}
	return a, nil
}

// handlePluginPopupKey routes key events to the plugin-driven list popup.
func (a App) handlePluginPopupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		a.pluginPopup = nil
		rpc := a.rpc
		return a, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = rpc.PluginPopupCancelled(ctx)
			return nil
		}
	case "enter":
		if a.pluginPopup != nil && len(a.pluginPopup.items) > 0 {
			idx := uint32(a.pluginPopup.idx)
			a.pluginPopup = nil
			rpc := a.rpc
			return a, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = rpc.PluginPopupSelected(ctx, idx)
				return nil
			}
		}
	case "up", "k":
		if a.pluginPopup != nil && a.pluginPopup.idx > 0 {
			a.pluginPopup.idx--
		}
	case "down", "j":
		if a.pluginPopup != nil && a.pluginPopup.idx < len(a.pluginPopup.items)-1 {
			a.pluginPopup.idx++
		}
	}
	return a, nil
}

// handlePluginInputKey routes key events to the plugin-driven text-input overlay.
func (a App) handlePluginInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		a.pluginInput = nil
		rpc := a.rpc
		return a, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = rpc.PluginInputCancelled(ctx)
			return nil
		}
	case "enter":
		if a.pluginInput != nil {
			text := a.pluginInput.text
			a.pluginInput = nil
			rpc := a.rpc
			return a, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = rpc.PluginInputConfirmed(ctx, text)
				return nil
			}
		}
	case "backspace":
		if a.pluginInput != nil {
			runes := []rune(a.pluginInput.text)
			if len(runes) > 0 {
				a.pluginInput.text = string(runes[:len(runes)-1])
			}
		}
	default:
		if len(msg.Runes) > 0 && a.pluginInput != nil {
			a.pluginInput.text += string(msg.Runes)
		}
	}
	return a, nil
}

// handleSearchReplaceKey routes key events to the global search & replace dialog.
func (a App) handleSearchReplaceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := a.searchReplace
	if d == nil {
		return a, nil
	}

	if d.confirmingAll {
		switch msg.String() {
		case "y", "enter":
			d.confirmingAll = false
			a.searchReplace = d
			return a, a.startApplyAll(d)
		case "n", "esc":
			d.confirmingAll = false
			a.searchReplace = d
		}
		return a, nil
	}

	if d.focus == sraFocusResults {
		switch msg.String() {
		case "esc":
			a.searchReplace = nil
			return a, nil
		case "up", "ctrl+p", "k":
			d.moveCursor(-1)
			a.searchReplace = d
			return a, nil
		case "down", "ctrl+n", "j":
			d.moveCursor(1)
			a.searchReplace = d
			return a, nil
		case "tab":
			d.advanceFocus(1)
			a.searchReplace = d
			return a, nil
		case "shift+tab":
			d.advanceFocus(-1)
			a.searchReplace = d
			return a, nil
		case "enter":
			if !d.replaceOpen {
				cmd := a.openSearchReplaceMatch(d)
				a.searchReplace = nil
				return a, cmd
			}
			return a, a.acceptSearchReplaceMatch(d)
		}
		return a, nil
	}

	switch msg.String() {
	case "esc":
		a.searchReplace = nil
		return a, nil
	case "tab":
		d.advanceFocus(1)
		a.searchReplace = d
		return a, nil
	case "shift+tab":
		d.advanceFocus(-1)
		a.searchReplace = d
		return a, nil
	case " ":
		switch d.focus {
		case sraFocusCase:
			d.caseSensitive = !d.caseSensitive
			a.searchReplace = d
			return a, nil
		case sraFocusRegex:
			d.useRegex = !d.useRegex
			a.searchReplace = d
			return a, nil
		case sraFocusToggle:
			d.replaceOpen = !d.replaceOpen
			if !d.replaceOpen {
				d.replaceInput.Blur()
			}
			a.searchReplace = d
			return a, nil
		}
	case "enter":
		switch d.focus {
		case sraFocusSearch:
			cmd := a.startSearchReplaceSearch(d)
			a.searchReplace = d
			return a, cmd
		case sraFocusToggle:
			d.replaceOpen = !d.replaceOpen
			a.searchReplace = d
			return a, nil
		case sraFocusAll:
			if len(d.results) > 0 {
				d.confirmingAll = true
			}
			a.searchReplace = d
			return a, nil
		}
		a.searchReplace = d
		return a, nil
	}

	var cmd tea.Cmd
	switch d.focus {
	case sraFocusSearch:
		d.searchInput, cmd = d.searchInput.Update(msg)
	case sraFocusReplace:
		d.replaceInput, cmd = d.replaceInput.Update(msg)
		d.refreshResultsView()
	}
	a.searchReplace = d
	return a, cmd
}

// handleGrepKey routes key events to the workspace search picker.
func (a App) handleGrepKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return grepCancelledMsg{} }
	case "enter":
		if a.grep != nil && len(a.grep.results) > 0 {
			r := a.grep.results[a.grep.cursor]
			absPath := filepath.Join(a.grep.workDir, r.RelPath)
			return a, func() tea.Msg {
				return grepPickedMsg{absPath: absPath, line: r.Line, col: r.Col, matchLen: r.MatchLen}
			}
		}
	case "up", "ctrl+p", "k":
		if a.grep != nil {
			a.grep.moveUp()
		}
	case "down", "ctrl+n", "j":
		if a.grep != nil {
			a.grep.moveDown()
		}
	}
	return a, nil
}
