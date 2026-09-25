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

// The reference picker uses the "Go to Symbol in File" picker's colours. It
// had its own orange-and-rust set, which read as an error dialog; sharing the
// styles keeps the two navigation pickers looking like one family and stops
// them drifting apart again.
var (
	refPickerBg          = symPickerBg
	refPickerBorderStyle = symPickerBorderStyle
	refPickerTitleStyle  = symPickerTitleStyle
	refPickerItemStyle   = symPickerItemStyle
	refPickerSelStyle    = symPickerSelStyle
	// Locations take the symbol picker's kind-label colour, the same role:
	// the short tag in front of each row.
	refPickerLocStyle = symPickerKindStyle
	// The code preview keeps a neutral grey: the symbol picker's nearest
	// equivalent (its container colour) is too dim to read a line of code in.
	refPickerPreviewStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#778899"))
	// refPickerRuleColor is the separator under the title — the symbol
	// picker's, where this one used the old border orange.
	refPickerRuleColor = lipgloss.Color("#44AACC")
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
		Foreground(refPickerRuleColor).
		Render(strings.Repeat("─", innerW)))

	vis := visibleRows(len(p.refs), refPickerMaxVisible, p.height, 4) // title, separator, 2 borders
	start := max(0, min(p.cursor-vis/2, len(p.refs)-vis))
	end := min(start+vis, len(p.refs))

	// Both indicator rows are reserved whenever the list overflows, blank
	// when there is nothing more that way, so scrolling never resizes the box.
	if start > 0 || end < len(p.refs) {
		rows = append(rows, refPickerItemStyle.Width(innerW).Render(moreLabel(start > 0, "↑")))
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
	if start > 0 || end < len(p.refs) {
		rows = append(rows, refPickerItemStyle.Width(innerW).Render(moreLabel(end < len(p.refs), "↓")))
	}

	return refPickerBorderStyle.Render(strings.Join(rows, "\n"))
}
