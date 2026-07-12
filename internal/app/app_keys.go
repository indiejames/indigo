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
		path := a.picker.selected()
		if path == "" {
			// No filtered match — treat the query as a direct (possibly relative) path.
			q := strings.TrimSpace(a.picker.query)
			if q == "" {
				return a, nil
			}
			if !filepath.IsAbs(q) {
				q = filepath.Join(a.workDir, q)
			}
			return a, func() tea.Msg { return pickedMsg{absPath: q} }
		}
		return a, func() tea.Msg { return pickedMsg{absPath: path} }
	case "up", "ctrl+p":
		a.picker.moveUp()
	case "down", "ctrl+n":
		a.picker.moveDown()
	case "backspace":
		q := []rune(a.picker.query)
		if len(q) > 0 {
			a.picker.setQuery(string(q[:len(q)-1]))
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
