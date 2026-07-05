package app

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/client"
)

type bufPicker struct {
	items  []bufPickerItem
	cursor int
	width  int
	height int
}

type bufPickerItem struct {
	idx   int
	path  string
	dirty bool
}

type bufPickedMsg struct{ idx int }
type bufPickerCancelledMsg struct{}

func newBufPicker(buffers []client.Model, active, w, h int) *bufPicker {
	items := make([]bufPickerItem, len(buffers))
	for i, m := range buffers {
		items[i] = bufPickerItem{idx: i, path: m.FilePath(), dirty: m.Dirty()}
	}
	cursor := active
	if cursor >= len(items) {
		cursor = 0
	}
	return &bufPicker{items: items, cursor: cursor, width: w, height: h}
}

func (bp *bufPicker) moveUp() {
	if bp.cursor > 0 {
		bp.cursor--
	}
}

func (bp *bufPicker) moveDown() {
	if bp.cursor < len(bp.items)-1 {
		bp.cursor++
	}
}

func (bp *bufPicker) selected() int {
	if len(bp.items) == 0 {
		return 0
	}
	return bp.items[bp.cursor].idx
}

const bufPickerMaxVisible = 14

var (
	bufPickerBg = lipgloss.Color("#1E2A38")

	bufPickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#4488CC")).
				Background(bufPickerBg)

	bufPickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#88BBEE")).
				Bold(true).
				Padding(0, 1)

	bufPickerItemStyle = lipgloss.NewStyle().
				Background(bufPickerBg).
				Foreground(lipgloss.Color("#AABBCC")).
				Padding(0, 1)

	bufPickerSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2D5F8A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	bufPickerDirtyMark = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFAA44")).
				Render("●")
)

// render returns the styled popup box. The caller is responsible for overlaying
// it onto the editor view at the correct position.
func (bp *bufPicker) render() string {
	// Inner width: longest base filename + chrome, capped at 2/3 of terminal.
	innerW := len("Open Buffers")
	for _, item := range bp.items {
		n := len(filepath.Base(item.path))
		if n > innerW {
			innerW = n
		}
	}
	innerW += 6 // room for dirty mark + padding
	innerW = min(innerW, bp.width*2/3)
	innerW = max(innerW, 30)

	// Scroll window centred on selection.
	vis := min(len(bp.items), bufPickerMaxVisible)
	start := max(0, min(bp.cursor-vis/2, len(bp.items)-vis))
	end := min(start+vis, len(bp.items))

	var rows []string

	// Title row.
	rows = append(rows, bufPickerTitleStyle.Width(innerW).Render("Open Buffers"))
	rows = append(rows, lipgloss.NewStyle().
		Background(bufPickerBg).
		Foreground(lipgloss.Color("#4488CC")).
		Render(strings.Repeat("─", innerW)))

	if start > 0 {
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render("  ↑ more"))
	}

	for i := start; i < end; i++ {
		item := bp.items[i]
		name := filepath.Base(item.path)
		if name == "" || name == "." {
			name = "[no name]"
		}
		maxNameW := innerW - 4
		if len([]rune(name)) > maxNameW {
			name = string([]rune(name)[:max(0, maxNameW-1)]) + "…"
		}
		var label string
		if item.dirty {
			label = bufPickerDirtyMark + " " + name
		} else {
			label = "  " + name
		}
		if i == bp.cursor {
			rows = append(rows, bufPickerSelStyle.Width(innerW).Render(label))
		} else {
			rows = append(rows, bufPickerItemStyle.Width(innerW).Render(label))
		}
	}

	if end < len(bp.items) {
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render("  ↓ more"))
	}

	return bufPickerBorderStyle.Render(strings.Join(rows, "\n"))
}
