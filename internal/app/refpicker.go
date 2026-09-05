package app

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/indiejames/indigo/internal/client"
)

type refPickerState struct {
	title  string
	refs   []client.ClientReference
	cursor int
	width  int
	height int
}

func newRefPicker(title string, refs []client.ClientReference, w, h int) *refPickerState {
	return &refPickerState{title: title, refs: refs, width: w, height: h}
}

func (a App) handleRefPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.refPicker
	switch msg.String() {
	case "esc", "ctrl+c":
		a.refPicker = nil
		return a, nil
	case "enter":
		if p != nil && len(p.refs) > 0 {
			ref := p.refs[p.cursor]
			a.refPicker = nil
			return a, a.doOpenFileAtPos(ref.Path, ref.Line, ref.Col)
		}
	case "up", "k", "ctrl+p":
		if p != nil && p.cursor > 0 {
			p.cursor--
		}
	case "down", "j", "ctrl+n":
		if p != nil && p.cursor < len(p.refs)-1 {
			p.cursor++
		}
	}
	return a, nil
}

const refPickerMaxVisible = 16

var (
	refPickerBg = lipgloss.Color("#1E2A38")

	refPickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#AA6644")).
				Background(refPickerBg)

	refPickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#DDAA88")).
				Bold(true).
				Padding(0, 1)

	refPickerItemStyle = lipgloss.NewStyle().
				Background(refPickerBg).
				Foreground(lipgloss.Color("#AABBCC")).
				Padding(0, 1)

	refPickerSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#6A3010")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	refPickerLocStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#AA7755"))

	refPickerPreviewStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#778899"))
)

func (p *refPickerState) render() string {
	innerW := 60
	if p.width > 0 {
		innerW = min(p.width*3/4, 90)
		innerW = max(innerW, 50)
	}

	title := fmt.Sprintf("%s  [%d]", p.title, len(p.refs))
	var rows []string
	rows = append(rows, refPickerTitleStyle.Width(innerW).Render(title))
	rows = append(rows, lipgloss.NewStyle().
		Background(refPickerBg).
		Foreground(lipgloss.Color("#AA6644")).
		Render(strings.Repeat("─", innerW)))

	vis := min(len(p.refs), refPickerMaxVisible)
	start := max(0, min(p.cursor-vis/2, len(p.refs)-vis))
	end := min(start+vis, len(p.refs))

	if start > 0 {
		rows = append(rows, refPickerItemStyle.Width(innerW).Render("  ↑ more"))
	}
	for i := start; i < end; i++ {
		ref := p.refs[i]
		loc := refPickerLocStyle.Render(fmt.Sprintf("%s:%d", filepath.Base(ref.Path), ref.Line+1))
		preview := strings.TrimSpace(ref.Preview)
		maxPreview := innerW - lipgloss.Width(loc) - 4
		if len([]rune(preview)) > maxPreview && maxPreview > 3 {
			preview = string([]rune(preview)[:maxPreview-1]) + "…"
		}
		var label string
		if preview != "" {
			label = loc + "  " + refPickerPreviewStyle.Render(preview)
		} else {
			label = loc
		}
		if i == p.cursor {
			rows = append(rows, refPickerSelStyle.Width(innerW).Render(label))
		} else {
			rows = append(rows, refPickerItemStyle.Width(innerW).Render(label))
		}
	}
	if end < len(p.refs) {
		rows = append(rows, refPickerItemStyle.Width(innerW).Render("  ↓ more"))
	}

	return refPickerBorderStyle.Render(strings.Join(rows, "\n"))
}
