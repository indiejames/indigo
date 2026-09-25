package app

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/indiejames/indigo/internal/client"
)

// symbolResultsMsg delivers workspace symbol query results to the App.
type symbolResultsMsg struct {
	syms  []client.ClientSymbol
	query string
}

type symbolPickerState struct {
	bufID   uint32
	query   string
	results []client.ClientSymbol
	cursor  int
	loading bool
	width   int
	height  int
}

type docSymbolPickerState struct {
	allSyms  []client.ClientSymbol
	filter   string
	filtered []client.ClientSymbol
	cursor   int
	width    int
	height   int
	// filters are the kind/top-level checkboxes; focus is which control Tab
	// has selected — docSymFocusFilter (the text box) or one of the boxes.
	filters docSymbolFilters
	focus   int
}

func newSymbolPicker(bufID uint32, w, h int) *symbolPickerState {
	return &symbolPickerState{bufID: bufID, width: w, height: h}
}

func newDocSymbolPicker(syms []client.ClientSymbol, filters docSymbolFilters, w, h int) *docSymbolPickerState {
	p := &docSymbolPickerState{allSyms: sortedSymbols(syms), filters: filters, width: w, height: h}
	p.applyFilter("")
	return p
}

// sortedSymbols returns syms in alphabetical order, case-insensitively, with
// ties (overloads, or one name defined twice) in file order. A copy, so the
// caller's slice is left as it was. Sorted once here: applyFilter only ever
// drops entries, so every filtered view keeps this order.
func sortedSymbols(syms []client.ClientSymbol) []client.ClientSymbol {
	out := slices.Clone(syms)
	slices.SortStableFunc(out, func(a, b client.ClientSymbol) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.Line, b.Line)
	})
	return out
}

// applyFilter sets the text filter and recomputes the visible list from it and
// the checkboxes together.
func (p *docSymbolPickerState) applyFilter(filter string) {
	p.filter = filter
	low := strings.ToLower(filter)
	out := make([]client.ClientSymbol, 0, len(p.allSyms))
	for _, s := range p.allSyms {
		if !p.filters.allows(s) {
			continue
		}
		if low != "" && !strings.Contains(strings.ToLower(s.Name), low) {
			continue
		}
		out = append(out, s)
	}
	p.filtered = out
	p.cursor = 0
}

// hiddenByCheckboxes counts symbols that match the text filter but are hidden
// by a checkbox, so an empty list can say why it is empty.
func (p *docSymbolPickerState) hiddenByCheckboxes() int {
	low := strings.ToLower(p.filter)
	n := 0
	for _, s := range p.allSyms {
		if (low == "" || strings.Contains(strings.ToLower(s.Name), low)) && !p.filters.allows(s) {
			n++
		}
	}
	return n
}

// -- Symbol picker key handler --

func (a App) handleSymbolPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.symbolPicker
	switch msg.String() {
	case "esc", "ctrl+c":
		a.symbolPicker = nil
		return a, nil
	case "enter":
		if p != nil && len(p.results) > 0 {
			sym := p.results[p.cursor]
			a.symbolPicker = nil
			return a, a.doOpenFileAtPos(sym.Path, sym.Line, sym.Col)
		}
	case "up", "ctrl+p":
		if p != nil && p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p != nil && p.cursor < len(p.results)-1 {
			p.cursor++
		}
	case "backspace":
		if p != nil && len(p.query) > 0 {
			runes := []rune(p.query)
			p.query = string(runes[:len(runes)-1])
			p.loading = true
			return a, a.fetchWorkspaceSymbols()
		}
	default:
		if msg.Key().Text != "" && p != nil {
			p.query += msg.Key().Text
			p.loading = true
			return a, a.fetchWorkspaceSymbols()
		}
	}
	return a, nil
}

func (a App) fetchWorkspaceSymbols() tea.Cmd {
	if a.symbolPicker == nil {
		return nil
	}
	bufID := a.symbolPicker.bufID
	query := a.symbolPicker.query
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		syms, err := rpc.WorkspaceSymbols(ctx, bufID, query)
		if err != nil {
			return symbolResultsMsg{query: query}
		}
		return symbolResultsMsg{syms: syms, query: query}
	}
}

// -- Doc symbol picker key handler --

func (a App) handleDocSymbolPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.docSymbolPicker
	if p != nil {
		// Remembered for the next time the picker opens, this session: the
		// checkboxes are a preference, and resetting them on every open would
		// make changing one pointless.
		f := p.filters
		a.docSymFilters = &f
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		a.docSymbolPicker = nil
		return a, nil
	case "tab", "shift+tab":
		if p != nil {
			step := 1
			if msg.String() == "shift+tab" {
				step = docSymFocusCount - 1
			}
			p.focus = (p.focus + step) % docSymFocusCount
		}
		return a, nil
	case "space":
		// Toggles the focused box. In the text filter a space is just text —
		// handled here, since this case would otherwise swallow it.
		if p == nil {
			return a, nil
		}
		if p.focus == docSymFocusFilter {
			p.applyFilter(p.filter + " ")
			return a, nil
		}
		p.filters.toggle(p.focus)
		f := p.filters
		a.docSymFilters = &f
		p.applyFilter(p.filter)
		return a, nil
	case "enter":
		if p != nil && p.focus != docSymFocusFilter {
			// Same as Space on a box — Enter should never jump away from a
			// checkbox the user is looking at.
			p.filters.toggle(p.focus)
			f := p.filters
			a.docSymFilters = &f
			p.applyFilter(p.filter)
			return a, nil
		}
		if p != nil && len(p.filtered) > 0 {
			sym := p.filtered[p.cursor]
			a.docSymbolPicker = nil
			// Same buffer — just move cursor.
			if len(a.buffers) > 0 {
				a.buffers[a.active] = a.buffers[a.active].AtPos(sym.Line, sym.Col, a.bufHeight())
			}
			return a, nil
		}
	case "up", "ctrl+p":
		if p != nil && p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p != nil && p.cursor < len(p.filtered)-1 {
			p.cursor++
		}
	case "backspace":
		if p != nil && len(p.filter) > 0 {
			p.focus = docSymFocusFilter
			runes := []rune(p.filter)
			p.applyFilter(string(runes[:len(runes)-1]))
		}
	default:
		// Typing always goes to the text filter, wherever focus was.
		if msg.Key().Text != "" && p != nil {
			p.focus = docSymFocusFilter
			p.applyFilter(p.filter + msg.Key().Text)
		}
	}
	return a, nil
}

// -- Render functions --

// moreLabel is the text of a scroll-indicator row: "  ↑ more" when the list
// continues that way, blank otherwise. Blank rather than absent, so a list
// that overflows always has both rows and the box keeps one height as it
// scrolls — adding and removing the rows made every picker jump by a line at
// the top and bottom of its list.
func moreLabel(show bool, arrow string) string {
	if !show {
		return ""
	}
	return "  " + arrow + " more"
}

// visibleRows is how many list items a centered picker can show: at most
// maxVisible, and no more than fit in termHeight after chrome — the rows that
// are not list items (borders, title, input, separators). When the list then
// overflows, the two indicator rows moreLabel fills are reserved too.
//
// overlayCenter drops whatever falls off-screen, so a picker taller than a
// short terminal lost rows at the top and bottom — possibly the selected one.
// termHeight <= 0 (not yet known) leaves the count at maxVisible, and at
// least one item is always shown.
func visibleRows(n, maxVisible, termHeight, chrome int) int {
	vis := min(n, maxVisible)
	if termHeight > 0 {
		vis = min(vis, termHeight-chrome)
		if vis < n {
			vis = min(vis, termHeight-chrome-2)
		}
	}
	return max(vis, 1)
}

const symbolPickerMaxVisible = 16

var (
	symPickerBg = lipgloss.Color("#1E2A38")

	symPickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#44AACC")).
				Background(symPickerBg)

	symPickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#88CCEE")).
				Bold(true).
				Padding(0, 1)

	symPickerInputStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0D1B2A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	symPickerItemStyle = lipgloss.NewStyle().
				Background(symPickerBg).
				Foreground(lipgloss.Color("#AABBCC")).
				Padding(0, 1)

	symPickerSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2D5F8A")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	symPickerKindStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6699AA"))

	symPickerContainerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#556677"))

	// The checkbox styles carry the picker's background: the search dialog's
	// set only a foreground, and inside this box each would reset the
	// background behind it and leave gaps.
	symPickerCheckStyle = lipgloss.NewStyle().
				Background(symPickerBg).
				Foreground(lipgloss.Color("#AABBCC"))
	symPickerCheckFocusStyle = lipgloss.NewStyle().
					Background(symPickerBg).
					Foreground(lipgloss.Color("#5FD7FF")).
					Bold(true)
)

// symPickerCheckbox renders "[x] label" in the picker's colours, in the same
// format as the search dialog's checkboxes.
func symPickerCheckbox(label string, checked, focused bool) string {
	mark := " "
	if checked {
		mark = "x"
	}
	s := fmt.Sprintf("[%s] %s", mark, label)
	if focused {
		return symPickerCheckFocusStyle.Render(s)
	}
	return symPickerCheckStyle.Render(s)
}

// renderFilterRows lays out the checkboxes, wrapping onto as many rows as
// innerW needs — the picker can be as narrow as 40 columns.
func (p *docSymbolPickerState) renderFilterRows(innerW int) []string {
	boxes := []string{symPickerCheckbox("Top-level only", p.filters.topLevelOnly, p.focus == docSymFocusTopLevel)}
	widths := []int{len("[x] Top-level only")}
	for c := symCategory(0); c < numSymCategories; c++ {
		boxes = append(boxes, symPickerCheckbox(symCategoryLabels[c], p.filters.show[c], p.focus == docSymFocusCategory0+int(c)))
		widths = append(widths, len("[x] ")+len(symCategoryLabels[c]))
	}
	gap := symPickerCheckStyle.Render("  ")
	var rows []string
	var line string
	lineW := 1 // leading space
	for i, box := range boxes {
		if line != "" && lineW+2+widths[i] > innerW-2 {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render(line))
			line, lineW = "", 1
		}
		if line != "" {
			line += gap
			lineW += 2
		}
		line += box
		lineW += widths[i]
	}
	if line != "" {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render(line))
	}
	return rows
}

func (p *symbolPickerState) render() string {
	innerW := 50
	if p.width > 0 {
		innerW = min(p.width*2/3, 80)
		innerW = max(innerW, 40)
	}

	var rows []string
	rows = append(rows, symPickerTitleStyle.Width(innerW).Render("Go to Symbol in Project"))
	rows = append(rows, symPickerInputStyle.Width(innerW).Render(" "+p.query+"█"))
	rows = append(rows, lipgloss.NewStyle().
		Background(symPickerBg).
		Foreground(lipgloss.Color("#44AACC")).
		Render(strings.Repeat("─", innerW)))

	if p.loading {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  searching…"))
	} else if len(p.results) == 0 && p.query == "" {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  type to search"))
	} else if len(p.results) == 0 {
		rows = append(rows, symPickerItemStyle.Width(innerW).Render("  no results"))
	} else {
		vis := visibleRows(len(p.results), symbolPickerMaxVisible, p.height, 5)
		start := max(0, min(p.cursor-vis/2, len(p.results)-vis))
		end := min(start+vis, len(p.results))

		// Both indicator rows are reserved whenever the list overflows, blank
		// when there is nothing more that way, so scrolling never resizes the box.
		if start > 0 || end < len(p.results) {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render(moreLabel(start > 0, "↑")))
		}
		for i := start; i < end; i++ {
			sym := p.results[i]
			// [kind] name  container  file:line
			kind := symPickerKindStyle.Render(fmt.Sprintf("[%s]", sym.KindLabel))
			loc := fmt.Sprintf("  %s:%d", filepath.Base(sym.Path), sym.Line+1)
			container := ""
			if sym.ContainerName != "" {
				container = symPickerContainerStyle.Render(" (" + sym.ContainerName + ")")
			}
			label := kind + " " + sym.Name + container + symPickerContainerStyle.Render(loc)
			if i == p.cursor {
				rows = append(rows, symPickerSelStyle.Width(innerW).Render(label))
			} else {
				rows = append(rows, symPickerItemStyle.Width(innerW).Render(label))
			}
		}
		if start > 0 || end < len(p.results) {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render(moreLabel(end < len(p.results), "↓")))
		}
	}

	return symPickerBorderStyle.Render(strings.Join(rows, "\n"))
}

func (p *docSymbolPickerState) render() string {
	innerW := 50
	if p.width > 0 {
		innerW = min(p.width*2/3, 70)
		innerW = max(innerW, 40)
	}

	var rows []string
	title := fmt.Sprintf("Go to Symbol in File  %d/%d   Tab: filters", len(p.filtered), len(p.allSyms))
	rows = append(rows, symPickerTitleStyle.Width(innerW).Render(title))
	cursor := "█"
	if p.focus != docSymFocusFilter {
		cursor = "" // focus is on a checkbox; no caret in the text box
	}
	rows = append(rows, symPickerInputStyle.Width(innerW).Render(" "+p.filter+cursor))
	filterRows := p.renderFilterRows(innerW)
	rows = append(rows, filterRows...)
	rows = append(rows, lipgloss.NewStyle().
		Background(symPickerBg).
		Foreground(lipgloss.Color("#44AACC")).
		Render(strings.Repeat("─", innerW)))

	if len(p.filtered) == 0 {
		msg := "  no matching symbols"
		if n := p.hiddenByCheckboxes(); n > 0 {
			msg = fmt.Sprintf("  no matching symbols (%d hidden by filters — Tab to change them)", n)
		}
		rows = append(rows, symPickerItemStyle.Width(innerW).Render(msg))
	} else {
		// Chrome: title, input, the filter rows (as many as the checkboxes
		// wrapped onto), separator, and two borders.
		vis := visibleRows(len(p.filtered), symbolPickerMaxVisible, p.height, 5+len(filterRows))
		start := max(0, min(p.cursor-vis/2, len(p.filtered)-vis))
		end := min(start+vis, len(p.filtered))

		// Both indicator rows are reserved whenever the list overflows, blank
		// when there is nothing more that way, so scrolling never resizes the box.
		if start > 0 || end < len(p.filtered) {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render(moreLabel(start > 0, "↑")))
		}
		for i := start; i < end; i++ {
			sym := p.filtered[i]
			kind := symPickerKindStyle.Render(fmt.Sprintf("[%s]", sym.KindLabel))
			container := ""
			if sym.ContainerName != "" {
				container = symPickerContainerStyle.Render(" (" + sym.ContainerName + ")")
			}
			label := kind + " " + sym.Name + container
			if i == p.cursor {
				rows = append(rows, symPickerSelStyle.Width(innerW).Render(label))
			} else {
				rows = append(rows, symPickerItemStyle.Width(innerW).Render(label))
			}
		}
		if start > 0 || end < len(p.filtered) {
			rows = append(rows, symPickerItemStyle.Width(innerW).Render(moreLabel(end < len(p.filtered), "↓")))
		}
	}

	return symPickerBorderStyle.Render(strings.Join(rows, "\n"))
}
