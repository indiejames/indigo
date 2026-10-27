package app

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/indiejames/indigo/internal/client"
)

// unsavedPrompt asks what to do with unsaved changes before closing the active
// buffer (Ctrl+W) or quitting (Ctrl+Q). It replaces the modal editor's :q! and
// :qa!: the force is an answer here rather than a separate command.
type unsavedPrompt struct {
	quit  bool     // quitting the editor, not closing one buffer
	files []string // the unsaved files, as shown
}

// handleConfirmClose answers a Ctrl+W on a buffer with unsaved changes.
func (a App) handleConfirmClose() (tea.Model, tea.Cmd) {
	if len(a.buffers) == 0 {
		return a, nil
	}
	a.unsaved = &unsavedPrompt{files: []string{a.bufferName(a.active)}}
	return a, nil
}

// handleQuitRequest answers Ctrl+Q: quit at once when nothing is unsaved, and
// ask otherwise.
func (a App) handleQuitRequest() (tea.Model, tea.Cmd) {
	var files []string
	for i, m := range a.buffers {
		if m.Dirty() {
			files = append(files, a.bufferName(i))
		}
	}
	if len(files) == 0 {
		return a, a.doCloseAllAndQuit()
	}
	a.unsaved = &unsavedPrompt{quit: true, files: files}
	return a, nil
}

func (a App) bufferName(i int) string {
	if p := a.buffers[i].FilePath(); p != "" {
		return filepath.Base(p)
	}
	return "untitled"
}

// handleUnsavedKey: s or Enter saves, d discards, c or Esc cancels. Every other
// key is swallowed while the prompt is open.
func (a App) handleUnsavedKey(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.unsaved
	switch km.String() {
	case "s", "enter":
		a.unsaved = nil
		if p.quit {
			return a.handleQuitAll(client.QuitAllMsg{SaveAll: true})
		}
		return a, a.buffers[a.active].SaveAndCloseCmd()
	case "d":
		a.unsaved = nil
		if p.quit {
			return a.handleQuitAll(client.QuitAllMsg{Force: true})
		}
		return a, a.buffers[a.active].CloseCmd()
	case "c", "esc":
		a.unsaved = nil
	}
	return a, nil
}

func renderUnsavedPrompt(p *unsavedPrompt, w int) string {
	innerW := 46
	if w > 0 && w*2/3 < innerW {
		innerW = w * 2 / 3
	}
	innerW = max(innerW, 34)
	if w > 0 && innerW+4 > w {
		innerW = w - 4
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FFAA44")).
		Background(lipgloss.Color("#1E2A38"))
	titleStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#0D1B2A")).
		Foreground(lipgloss.Color("#FFAA44")).
		Bold(true).
		Padding(0, 1)
	divStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#FFAA44"))
	textStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#AABBCC")).
		Padding(0, 1)
	hintStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#88DDAA")).
		Padding(0, 1)

	title := "Save changes before closing?"
	if p.quit {
		title = "Save changes before quitting?"
	}
	rows := []string{
		titleStyle.Width(innerW).Render(title),
		divStyle.Render(strings.Repeat("─", innerW)),
	}
	const maxShown = 6
	for i, f := range p.files {
		if i == maxShown {
			rows = append(rows, textStyle.Width(innerW).Render(fmt.Sprintf("…and %d more", len(p.files)-maxShown)))
			break
		}
		rows = append(rows, textStyle.Width(innerW).Render("• "+f))
	}
	save, discard := "s: Save", "d: Don't save"
	if len(p.files) > 1 {
		save, discard = "s: Save all", "d: Discard all"
	}
	rows = append(rows, hintStyle.Width(innerW).Render(save+"   "+discard+"   Esc: Cancel"))
	return borderStyle.Render(strings.Join(rows, "\n"))
}
