package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	tabBarBg       = lipgloss.Color("#065A96")
	tabActiveStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#087AC8")).
			Foreground(lipgloss.Color("#FFFFFF")).
			Bold(true)
	tabInactiveStyle = lipgloss.NewStyle().
				Background(tabBarBg).
				Foreground(lipgloss.Color("#AABBCC"))
	tabDirtyMark = "● "
	tabBarFill   = lipgloss.NewStyle().Background(tabBarBg)
)

func (a App) View() string {
	if a.picker != nil {
		return a.picker.View()
	}
	if a.grep != nil {
		return a.grep.View()
	}
	if len(a.buffers) == 0 {
		return "No buffer open. Press ctrl+p to open a file."
	}

	var sb strings.Builder
	if a.showTabBar() {
		sb.WriteString(a.renderTabBar())
		sb.WriteByte('\n')
	}
	sb.WriteString(a.buffers[a.active].View())
	base := sb.String()

	if a.fileChangedIdx >= 0 {
		return overlayCenter(base, renderFileChangedPrompt(a.width, a.fileChangedSel), a.width, a.height)
	}
	if a.bufPicker != nil {
		return overlayCenter(base, a.bufPicker.render(), a.width, a.height)
	}
	if a.searchReplace != nil {
		return overlayCenter(base, a.searchReplace.render(), a.width, a.height)
	}
	if a.pluginPopup != nil {
		return overlayCenter(base, a.pluginPopup.render(), a.width, a.height)
	}
	if a.pluginInput != nil {
		return overlayCenter(base, a.pluginInput.render(), a.width, a.height)
	}
	if a.newFileInput != nil {
		return overlayCenter(base, a.newFileInput.render(), a.width, a.height)
	}
	if a.newFileMkdirConfirm != nil {
		popup := renderNewFileMkdirConfirm(filepath.Dir(*a.newFileMkdirConfirm), a.width)
		return overlayCenter(base, popup, a.width, a.height)
	}
	if a.symbolPicker != nil {
		return overlayCenter(base, a.symbolPicker.render(), a.width, a.height)
	}
	if a.docSymbolPicker != nil {
		return overlayCenter(base, a.docSymbolPicker.render(), a.width, a.height)
	}
	if a.refPicker != nil {
		return overlayCenter(base, a.refPicker.render(), a.width, a.height)
	}
	return base
}

const pluginPopupMaxVisible = 14

var (
	pluginPopupBg = lipgloss.Color("#1E2A38")

	pluginPopupBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#4488CC")).
				Background(pluginPopupBg)

	pluginPopupTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#88BBEE")).
				Bold(true).
				Padding(0, 1)

	pluginPopupItemStyle = lipgloss.NewStyle().
				Background(pluginPopupBg).
				Foreground(lipgloss.Color("#AABBCC")).
				Padding(0, 1)

	pluginPopupSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2D5F8A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	pluginInputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#44AA88")).
				Background(lipgloss.Color("#1E2A38"))

	pluginInputTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#88DDAA")).
				Bold(true).
				Padding(0, 1)

	pluginInputFieldStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	pluginInputPlaceholderStyle = lipgloss.NewStyle().
					Background(lipgloss.Color("#0D1B2A")).
					Foreground(lipgloss.Color("#445566")).
					Padding(0, 1)
)

func (p *appPluginPopup) render() string {
	innerW := lipgloss.Width(p.title)
	for _, item := range p.items {
		n := len(item.Label)
		if item.Sublabel != "" {
			n += len(item.Sublabel) + 2
		}
		if n > innerW {
			innerW = n
		}
	}
	innerW += 4
	if p.width > 0 {
		innerW = min(innerW, p.width*2/3)
	}
	innerW = max(innerW, 30)

	vis := min(len(p.items), pluginPopupMaxVisible)
	if vis == 0 {
		vis = 1
	}
	start := max(0, min(p.idx-vis/2, len(p.items)-vis))
	end := min(start+vis, len(p.items))

	var rows []string
	rows = append(rows, pluginPopupTitleStyle.Width(innerW).Render(p.title))
	rows = append(rows, lipgloss.NewStyle().
		Background(pluginPopupBg).
		Foreground(lipgloss.Color("#4488CC")).
		Render(strings.Repeat("─", innerW)))

	if start > 0 {
		rows = append(rows, pluginPopupItemStyle.Width(innerW).Render("  ↑ more"))
	}
	for i := start; i < end; i++ {
		item := p.items[i]
		label := item.Label
		if item.Sublabel != "" {
			label += "  " + item.Sublabel
		}
		maxW := innerW - 2
		if len([]rune(label)) > maxW && maxW > 0 {
			label = string([]rune(label)[:maxW-1]) + "…"
		}
		label = "  " + label
		if i == p.idx {
			rows = append(rows, pluginPopupSelStyle.Width(innerW).Render(label))
		} else {
			rows = append(rows, pluginPopupItemStyle.Width(innerW).Render(label))
		}
	}
	if end < len(p.items) {
		rows = append(rows, pluginPopupItemStyle.Width(innerW).Render("  ↓ more"))
	}

	return pluginPopupBorderStyle.Render(strings.Join(rows, "\n"))
}

func (p *appPluginInput) render() string {
	innerW := max(lipgloss.Width(p.title), lipgloss.Width(p.placeholder))
	innerW += 4
	if p.width > 0 {
		innerW = min(innerW, p.width*2/3)
	}
	innerW = max(innerW, 30)

	var rows []string
	rows = append(rows, pluginInputTitleStyle.Width(innerW).Render(p.title))
	rows = append(rows, lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#44AA88")).
		Render(strings.Repeat("─", innerW)))

	if p.text == "" && p.placeholder != "" {
		rows = append(rows, pluginInputPlaceholderStyle.Width(innerW).Render(p.placeholder))
	} else {
		rows = append(rows, pluginInputFieldStyle.Width(innerW).Render(p.text+"█"))
	}

	return pluginInputBorderStyle.Render(strings.Join(rows, "\n"))
}

// renderNewFileMkdirConfirm renders the "create missing directory?"
// confirmation shown when a New File path's parent directory doesn't exist.
func renderNewFileMkdirConfirm(dir string, w int) string {
	innerW := 46
	if w > 0 && w*2/3 < innerW {
		innerW = w * 2 / 3
	}
	innerW = max(innerW, 34)
	// Ensure the dialog (including borders) fits within the terminal width.
	// RoundedBorder adds 2 chars on each side.
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

	msg := dir + " does not exist."
	if maxW := innerW - 2; len([]rune(msg)) > maxW && maxW > 1 {
		r := []rune(msg)
		msg = "…" + string(r[len(r)-(maxW-1):])
	}

	rows := []string{
		titleStyle.Width(innerW).Render("Create Directory?"),
		divStyle.Render(strings.Repeat("─", innerW)),
		textStyle.Width(innerW).Render(msg),
		textStyle.Width(innerW).Render("Create it and the new file?"),
		hintStyle.Width(innerW).Render("Enter/y: Create   Esc/n: Cancel"),
	}
	return borderStyle.Render(strings.Join(rows, "\n"))
}

func renderFileChangedPrompt(w, sel int) string {
	innerW := 40
	if w > 0 && w*2/3 < innerW {
		innerW = w * 2 / 3
	}
	if innerW < 30 {
		innerW = 30
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
	itemStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#AABBCC")).
		Padding(0, 1)
	selStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#2D5F8A")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1)

	style := func(idx int, label string) string {
		if idx == sel {
			return selStyle.Width(innerW).Render(label)
		}
		return itemStyle.Width(innerW).Render(label)
	}

	rows := []string{
		titleStyle.Width(innerW).Render("⚠  File changed on disk"),
		divStyle.Render(strings.Repeat("─", innerW)),
		style(0, "  Reload  (discard local changes)"),
		style(1, "  Keep local version"),
	}
	return borderStyle.Render(strings.Join(rows, "\n"))
}

// overlayCenter splices popup (a multi-line box) into the centre of base,
// using ANSI-aware left/right truncation so editor colours show on both sides.
func overlayCenter(base, popup string, termW, termH int) string {
	baseLines := strings.Split(base, "\n")
	popLines := strings.Split(popup, "\n")

	popH := len(popLines)
	popW := lipgloss.Width(popLines[0])

	startRow := (termH - popH) / 2
	startCol := (termW - popW) / 2
	if startCol < 0 {
		startCol = 0
	}

	out := make([]string, termH)
	for i := range out {
		if i < len(baseLines) {
			out[i] = baseLines[i]
		} else {
			out[i] = strings.Repeat(" ", termW)
		}
	}

	for pi, popLine := range popLines {
		ri := startRow + pi
		if ri < 0 || ri >= termH {
			continue
		}
		baseLine := out[ri]
		left := ansi.Truncate(baseLine, startCol, "")
		// Pad left side if baseLine is shorter than startCol (e.g. empty lines).
		leftW := lipgloss.Width(left)
		if leftW < startCol {
			left += strings.Repeat(" ", startCol-leftW)
		}
		right := ansi.TruncateLeft(baseLine, startCol+popW, "")
		out[ri] = left + popLine + right
	}

	return strings.Join(out, "\n")
}

func (a App) renderTabBar() string {
	var sb strings.Builder
	used := 0
	for i, m := range a.buffers {
		name := filepath.Base(m.FilePath())
		dirty := ""
		if m.Dirty() {
			dirty = tabDirtyMark
		}
		label := fmt.Sprintf("  %s%s  ", dirty, name)
		var rendered string
		if i == a.active {
			rendered = tabActiveStyle.Render(label)
		} else {
			rendered = tabInactiveStyle.Render(label)
		}
		sb.WriteString(rendered)
		used += lipgloss.Width(rendered)
	}
	// Show app-level status at the right if set.
	if a.status != "" {
		gap := a.width - used - lipgloss.Width(a.status)
		if gap > 0 {
			sb.WriteString(tabBarFill.Render(strings.Repeat(" ", gap)))
		}
		sb.WriteString(tabInactiveStyle.Foreground(lipgloss.Color("#FF5555")).Render(a.status))
	} else if used < a.width {
		sb.WriteString(tabBarFill.Render(strings.Repeat(" ", a.width-used)))
	}
	return sb.String()
}
