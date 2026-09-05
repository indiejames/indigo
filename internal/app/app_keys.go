package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
		if msg.Key().Text != "" {
			a.picker.setQuery(a.picker.query + msg.Key().Text)
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
		if msg.Key().Text != "" && a.pluginInput != nil {
			a.pluginInput.text += msg.Key().Text
		}
	}
	return a, nil
}

// handleNewFileInputKey routes key events to the "New File" filename prompt.
// Confirming opens the typed path via the normal open-file flow, which
// already creates an empty in-memory buffer for paths that don't exist yet
// (the file is written to disk on first save).
func (a App) handleNewFileInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		a.newFileInput = nil
		return a, nil
	case "enter":
		path := strings.TrimSpace(a.newFileInput.text)
		a.newFileInput = nil
		if path == "" {
			return a, nil
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.workDir, path)
		}
		if info, err := os.Stat(filepath.Dir(path)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Parent directory doesn't exist — ask before creating it,
				// rather than opening a buffer that will fail to save later.
				a.newFileMkdirConfirm = &path
				return a, nil
			}
			// Other error (permission denied, I/O error, etc.)
			a.status = fmt.Sprintf("E: cannot access parent directory: %v", err)
			return a, nil
		} else if !info.IsDir() {
			// Parent exists but is not a directory.
			a.status = fmt.Sprintf("E: parent path is not a directory: %s", filepath.Dir(path))
			return a, nil
		}
		return a, a.doOpenFile(path)
	case "backspace":
		if a.newFileInput != nil {
			runes := []rune(a.newFileInput.text)
			if len(runes) > 0 {
				a.newFileInput.text = string(runes[:len(runes)-1])
			}
		}
	default:
		if msg.Key().Text != "" && a.newFileInput != nil {
			a.newFileInput.text += msg.Key().Text
		}
	}
	return a, nil
}

// handleNewFileMkdirConfirmKey routes key events to the "create missing
// directory?" confirmation shown when a New File path's parent directory
// doesn't exist yet.
func (a App) handleNewFileMkdirConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	path := *a.newFileMkdirConfirm
	switch msg.String() {
	case "y", "Y", "enter":
		a.newFileMkdirConfirm = nil
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			a.status = fmt.Sprintf("E: could not create directory: %v", err)
			return a, nil
		}
		return a, a.doOpenFile(path)
	case "n", "N", "esc", "ctrl+c":
		a.newFileMkdirConfirm = nil
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
	case "space":
		switch d.focus {
		case sraFocusCase:
			d.caseSensitive = !d.caseSensitive
			a.searchReplace = d
			return a, nil
		case sraFocusRegex:
			d.useRegex = !d.useRegex
			a.searchReplace = d
			return a, nil
		case sraFocusFilterToggle:
			d.filterOpen = !d.filterOpen
			if !d.filterOpen {
				d.includeInput.Blur()
				d.excludeInput.Blur()
			}
			a.searchReplace = d
			return a, nil
		case sraFocusToggle:
			d.replaceOpen = !d.replaceOpen
			if !d.replaceOpen {
				d.replaceInput.Blur()
			}
			d.refreshResultsView()
			a.searchReplace = d
			return a, nil
		}
	case "enter":
		switch d.focus {
		case sraFocusSearch, sraFocusInclude, sraFocusExclude:
			cmd := a.startSearchReplaceSearch(d)
			a.searchReplace = d
			return a, cmd
		case sraFocusFilterToggle:
			d.filterOpen = !d.filterOpen
			a.searchReplace = d
			return a, nil
		case sraFocusToggle:
			d.replaceOpen = !d.replaceOpen
			d.refreshResultsView()
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

	return a.forwardToFocusedSraInput(d, msg)
}

// forwardToFocusedSraInput hands msg to whichever of the dialog's four text
// inputs currently has focus. Split out of handleSearchReplaceKey so a
// tea.PasteMsg can be forwarded the same way a key is: bubbles' textinput
// handles PasteMsg itself, but only if the message actually reaches it.
func (a App) forwardToFocusedSraInput(d *searchReplaceDialog, msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch d.focus {
	case sraFocusSearch:
		d.searchInput, cmd = d.searchInput.Update(msg)
	case sraFocusReplace:
		d.replaceInput, cmd = d.replaceInput.Update(msg)
		d.refreshResultsView()
	case sraFocusInclude:
		d.includeInput, cmd = d.includeInput.Update(msg)
	case sraFocusExclude:
		d.excludeInput, cmd = d.excludeInput.Update(msg)
	}
	a.searchReplace = d
	return a, cmd
}

// handlePaste delivers bracketed-paste content to whichever modal owns text
// input, using that modal's own text mechanism. ok is false when no modal
// wants it, so the caller can let the message fall through to the buffer.
//
// Needed because Bubble Tea v2 emits paste as a separate tea.PasteMsg rather
// than v1's KeyMsg-with-Paste-set. The KeyMsg routing below matches none of
// it, so without this, pasting into any of these dialogs does nothing.
func (a App) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Content == "" {
		return a, nil, false
	}
	switch {
	case a.picker != nil:
		a.picker.setQuery(a.picker.query + msg.Content)
		return a, nil, true
	case a.searchReplace != nil:
		m, cmd := a.forwardToFocusedSraInput(a.searchReplace, msg)
		return m, cmd, true
	case a.pluginInput != nil:
		a.pluginInput.text += msg.Content
		return a, nil, true
	case a.newFileInput != nil:
		a.newFileInput.text += msg.Content
		return a, nil, true
	}
	return a, nil, false
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

// handleDiagBrowserKey routes key events to the workspace diagnostic browser.
func (a App) handleDiagBrowserKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return diagBrowserCancelledMsg{} }
	case "enter":
		if a.diagBrowser != nil && len(a.diagBrowser.items) > 0 {
			it := a.diagBrowser.items[a.diagBrowser.cursor]
			return a, func() tea.Msg {
				return diagBrowserPickedMsg{absPath: it.Path, line: it.Line, col: it.Col}
			}
		}
	case "up", "ctrl+p", "k":
		if a.diagBrowser != nil {
			a.diagBrowser.moveUp()
		}
	case "down", "ctrl+n", "j":
		if a.diagBrowser != nil {
			a.diagBrowser.moveDown()
		}
	case "r":
		if a.diagBrowser != nil {
			return a.rescanDiagBrowser()
		}
	}
	return a, nil
}
