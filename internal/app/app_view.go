package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
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
	// tabOverflowStyle marks the "+N" count of tabs that didn't fit. Dimmer
	// than an inactive tab so it reads as a note about the bar rather than as
	// another buffer sitting on it.
	tabOverflowStyle = lipgloss.NewStyle().
				Background(tabBarBg).
				Foreground(lipgloss.Color("#7788AA")).
				Italic(true)
	// The group strip's names are set apart from the tabs by colour, so a
	// group name never reads as a file.
	groupActiveStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#6A4FB0")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Bold(true)
	groupInactiveStyle = lipgloss.NewStyle().
				Background(tabBarBg).
				Foreground(lipgloss.Color("#C8B4F0"))
	groupSeparatorStyle = lipgloss.NewStyle().
				Background(tabBarBg).
				Foreground(lipgloss.Color("#AABBCC"))
)

// View returns the frame plus the terminal modes indigo needs. v2 moved
// these out of tea.NewProgram options and onto the View itself, so they're
// declared per-frame here rather than once at startup: alt screen, focus
// reporting, and cell-motion mouse tracking (clicks, wheel, and drag) —
// the direct equivalents of v1's WithAltScreen/WithReportFocus/
// WithMouseCellMotion.
func (a App) View() tea.View {
	content, cur := a.renderFrame()
	v := tea.NewView(content)
	v.Cursor = cur
	v.AltScreen = true
	v.ReportFocus = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// renderFrame returns the composed frame and the terminal cursor to draw on
// it, if any.
//
// The cursor comes from the active buffer, shifted down by the tab bar, and
// is dropped entirely whenever something is layered on top: a full-screen
// picker replaces the buffer view outright, and every centered dialog has
// its own text input drawing its own cursor, so leaving the buffer's real
// cursor visible would strand it behind the dialog.
func (a App) renderFrame() (string, *tea.Cursor) {
	// Over everything, including the full-screen pickers a directory start
	// opens with — it is shown before any other dialog could have opened.
	if a.staleServer != nil {
		base, _ := a.renderBaseFrame()
		return overlayCenter(base, a.staleServer.Render(a.width), a.width, a.height), nil
	}
	return a.renderBaseFrame()
}

// renderBaseFrame is renderFrame without the stale-server prompt.
func (a App) renderBaseFrame() (string, *tea.Cursor) {
	if a.picker != nil {
		return a.picker.View(), nil
	}
	if a.grep != nil {
		return a.grep.View(), nil
	}
	if a.diagBrowser != nil {
		return a.diagBrowser.View(), nil
	}
	if len(a.buffers) == 0 {
		return "No buffer open. Press ctrl+p to open a file.", nil
	}

	var sb strings.Builder
	tabRows := 0
	if a.showTabBar() {
		sb.WriteString(a.renderTabBar())
		sb.WriteByte('\n')
		tabRows = 1
	}
	bufView := a.buffers[a.active].View()
	sb.WriteString(bufView.Content)
	base := sb.String()

	if a.fileChangedIdx >= 0 {
		return overlayCenter(base, renderFileChangedPrompt(a.width, a.fileChangedSel), a.width, a.height), nil
	}
	if a.procPicker != nil {
		return overlayCenter(base, a.procPicker.render(), a.width, a.height), nil
	}
	if a.bufPicker != nil {
		return overlayCenter(base, a.bufPicker.render(), a.width, a.height), nil
	}
	if a.groupPicker != nil {
		return overlayCenter(base, a.groupPicker.render(), a.width, a.height), nil
	}
	if a.searchReplace != nil {
		return overlayCenter(base, a.searchReplace.render(), a.width, a.height), nil
	}
	if a.pluginPopup != nil {
		return overlayCenter(base, a.pluginPopup.render(), a.width, a.height), nil
	}
	if a.pluginInput != nil {
		return overlayCenter(base, a.pluginInput.render(), a.width, a.height), nil
	}
	if a.newFileInput != nil {
		return overlayCenter(base, a.newFileInput.render(), a.width, a.height), nil
	}
	if a.newFileMkdirConfirm != nil {
		popup := renderNewFileMkdirConfirm(filepath.Dir(*a.newFileMkdirConfirm), a.width)
		return overlayCenter(base, popup, a.width, a.height), nil
	}
	if a.symbolPicker != nil {
		return overlayCenter(base, a.symbolPicker.render(), a.width, a.height), nil
	}
	if a.docSymbolPicker != nil {
		return overlayCenter(base, a.docSymbolPicker.render(), a.width, a.height), nil
	}
	if a.refPicker != nil {
		return overlayCenter(base, a.refPicker.render(), a.width, a.height), nil
	}

	// Copy rather than shifting the buffer's own cursor in place: View()
	// hands back a pointer into the value it just built, and nothing here
	// should depend on that being safe to mutate.
	var cur *tea.Cursor
	if bufView.Cursor != nil {
		c := *bufView.Cursor
		c.Y += tabRows
		cur = &c
	}
	return base, cur
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

	vis := visibleRows(len(p.items), pluginPopupMaxVisible, p.height, 4) // title, separator, 2 borders
	start := max(0, min(p.idx-vis/2, len(p.items)-vis))
	end := min(start+vis, len(p.items))

	var rows []string
	rows = append(rows, pluginPopupTitleStyle.Width(innerW).Render(p.title))
	rows = append(rows, lipgloss.NewStyle().
		Background(pluginPopupBg).
		Foreground(lipgloss.Color("#4488CC")).
		Render(strings.Repeat("─", innerW)))

	// Both indicator rows are reserved whenever the list overflows, blank
	// when there is nothing more that way, so scrolling never resizes the box.
	if start > 0 || end < len(p.items) {
		rows = append(rows, pluginPopupItemStyle.Width(innerW).Render(moreLabel(start > 0, "↑")))
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
	if start > 0 || end < len(p.items) {
		rows = append(rows, pluginPopupItemStyle.Width(innerW).Render(moreLabel(end < len(p.items), "↓")))
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

// tabLabel returns buffer i's tab text (matching renderTabBar's format)
// and its rendered width — shared between renderTabBar and tabAtColumn so
// the two can never drift apart. None of tabActiveStyle/tabInactiveStyle
// add padding/borders, so the label's own width equals the styled width.
func (a App) tabLabel(i int) (label string, width int) {
	m := a.buffers[i]
	name := filepath.Base(m.FilePath())
	dirty := ""
	if m.Dirty() {
		dirty = tabDirtyMark
	}
	label = fmt.Sprintf("  %s%s  ", dirty, name)
	return label, lipgloss.Width(label)
}

// tabSlot is one tab the bar actually shows: which buffer, and the label and
// width it occupies (which may be a truncation of the full label).
type tabSlot struct {
	idx   int
	label string
	width int
}

// tabLayout decides which tabs fit on the bar and in what order they render.
//
// Tabs used to be emitted unconditionally, so past roughly ten buffers the row
// overflowed the terminal width and the tabs beyond it were simply cut off —
// including, quite often, the active one, leaving no indication of which
// buffer you were actually in. Making every tab narrower instead trades that
// for filenames too truncated to tell apart, which is not obviously better.
//
// So when they no longer all fit, tabs age off by recency: the ones admitted
// are the most recently active (a.tabUse), the active tab first and therefore
// always present. They are then rendered in buffer order, not recency order —
// which tab is where must stay stable, or every buffer switch would reshuffle
// the bar under the user's cursor. The ones left out are counted in an
// overflow marker at the right end.
//
// Nothing becomes unreachable: switching to a hidden buffer (ctrl+tab, the
// buffer picker, a jump) makes it the most recent, so it appears immediately.
//
// Only the active tab group's tabs are laid out (groupTabs), in the group's
// order, in the room the group strip leaves.
func (a App) tabLayout() (slots []tabSlot, hidden int) {
	tabs := a.groupTabs()
	all := make([]tabSlot, len(tabs))
	pos := make(map[int]int, len(tabs)) // buffer index → position in all
	total := 0
	for p, i := range tabs {
		label, w := a.tabLabel(i)
		all[p] = tabSlot{idx: i, label: label, width: w}
		pos[i] = p
		total += w
	}

	_, stripW := a.groupStrip()
	budget := a.tabBudget() - stripW
	if a.width <= 0 || total <= budget {
		return all, 0
	}

	// Reserve room for the widest marker this bar could end up showing, so
	// admitting one more tab can't widen the marker and overflow the row.
	marker := tabOverflowLabel(len(tabs) - 1)
	room := budget - lipgloss.Width(marker)
	if room < 0 {
		room = 0
	}

	admitted := make(map[int]bool, len(all)) // keyed by position in all
	used := 0
	for _, i := range a.tabsByRecency() {
		p, ok := pos[i]
		if !ok {
			continue // in another group
		}
		if used+all[p].width <= room {
			admitted[p] = true
			used += all[p].width
			continue
		}
		// Keep scanning rather than stopping at the first tab that doesn't
		// fit: a narrower, more recent tab further down the list should still
		// get a slot.
	}

	// The active tab is guaranteed a slot even when it alone is wider than the
	// whole bar — truncated to fit rather than dropped, since a tab bar that
	// can't show where you are is the failure being fixed here.
	if p, ok := pos[a.active]; ok && !admitted[p] {
		t := all[p]
		t.label = ansi.Truncate(t.label, room, "")
		t.width = lipgloss.Width(t.label)
		all[p] = t
		admitted[p] = true
	}

	for p := range all {
		if admitted[p] {
			slots = append(slots, all[p])
		}
	}
	return slots, len(all) - len(slots)
}

// groupSlot is one tab group name on the bar's group strip.
type groupSlot struct {
	gi    int
	label string
	width int
}

// groupSeparator ends the group strip, before the tabs.
const groupSeparator = "│"

// groupStripMaxShortName is how many runes of a name the strip keeps once
// the names no longer fit whole.
const groupStripMaxShortName = 6

// groupStrip lays out the group names the tab bar starts with, and returns
// their total width including the separator. Empty when the bar shows no
// strip (showGroupStrip).
//
// The strip takes at most half the bar, so the tabs keep room. When the names
// don't fit whole, the other groups' are shortened, and then left out, but
// the active group's name is always shown: like the active tab, it is where
// you are.
func (a App) groupStrip() (slots []groupSlot, width int) {
	if !a.showGroupStrip() {
		return nil, 0
	}
	active := a.activeGroup()
	sepW := lipgloss.Width(groupSeparator)
	layout := func(short, onlyActive bool) ([]groupSlot, int) {
		var out []groupSlot
		w := sepW
		for gi, g := range a.groups {
			if onlyActive && gi != active {
				continue
			}
			name := []rune(g.label())
			if short && gi != active && len(name) > groupStripMaxShortName {
				name = append(name[:groupStripMaxShortName-1], '…')
			}
			label := " " + string(name) + " "
			lw := lipgloss.Width(label)
			out = append(out, groupSlot{gi: gi, label: label, width: lw})
			w += lw
		}
		return out, w
	}
	if a.width <= 0 {
		return layout(false, false)
	}
	limit := a.tabBudget() / 2
	for _, try := range []struct{ short, onlyActive bool }{{false, false}, {true, false}, {true, true}} {
		slots, width = layout(try.short, try.onlyActive)
		if width <= limit {
			return slots, width
		}
	}
	// Even the active name alone is too wide: shorten it to fit.
	if len(slots) == 1 {
		s := slots[0]
		s.label = ansi.Truncate(s.label, max(0, limit-sepW), "")
		s.width = lipgloss.Width(s.label)
		slots[0] = s
		width = s.width + sepW
	}
	return slots, width
}

// groupAtColumn returns the index of the group whose name covers column x on
// the tab bar, or false when x is not on the group strip.
func (a App) groupAtColumn(x int) (int, bool) {
	slots, _ := a.groupStrip()
	col := 0
	for _, s := range slots {
		if x >= col && x < col+s.width {
			return s.gi, true
		}
		col += s.width
	}
	return -1, false
}

// tabBudget is how many columns the tabs and the overflow marker have between
// them. The status message shares this row, so it comes out of the budget.
//
// A width of 0 means the terminal size isn't known yet (startup, and the App
// literals tests build), where laying out against a zero budget would hide
// every tab; callers treat that as unconstrained instead.
func (a App) tabBudget() int {
	budget := a.width
	if a.status != "" {
		budget -= lipgloss.Width(a.status)
	}
	return budget
}

// tabsByRecency returns buffer indices most-recently-active first. Buffers
// never recorded in tabUse (opened but never focused) sort last, in buffer
// order, so the result is deterministic rather than map-iteration order.
func (a App) tabsByRecency() []int {
	order := make([]int, len(a.buffers))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		return a.tabUse[a.buffers[order[x]].BufID()] > a.tabUse[a.buffers[order[y]].BufID()]
	})
	return order
}

// tabOverflowLabel is the marker shown at the right end of a bar that is
// hiding tabs, sized to match the tab padding around it.
func tabOverflowLabel(hidden int) string {
	return fmt.Sprintf("  +%d  ", hidden)
}

// tabAtColumn returns the index of the buffer whose tab covers column x in
// the rendered tab bar, or false if x falls past the last visible tab (the
// overflow marker and the fill/status area to its right).
//
// Shares tabLayout with renderTabBar so a click can never land on a buffer
// other than the one drawn there.
func (a App) tabAtColumn(x int) (int, bool) {
	slots, _ := a.tabLayout()
	_, col := a.groupStrip()
	if x < col {
		return -1, false
	}
	for _, t := range slots {
		if x < col+t.width {
			return t.idx, true
		}
		col += t.width
	}
	return -1, false
}

func (a App) renderTabBar() string {
	var sb strings.Builder
	used := 0
	if groups, stripW := a.groupStrip(); stripW > 0 {
		active := a.activeGroup()
		for _, g := range groups {
			if g.gi == active {
				sb.WriteString(groupActiveStyle.Render(g.label))
			} else {
				sb.WriteString(groupInactiveStyle.Render(g.label))
			}
		}
		sb.WriteString(groupSeparatorStyle.Render(groupSeparator))
		used += stripW
	}
	slots, hidden := a.tabLayout()
	for _, t := range slots {
		var rendered string
		if t.idx == a.active {
			rendered = tabActiveStyle.Render(t.label)
		} else {
			rendered = tabInactiveStyle.Render(t.label)
		}
		sb.WriteString(rendered)
		used += t.width
	}
	if hidden > 0 {
		// The marker is what's left over, not a reservation that must be
		// honoured: with a long enough status message the budget can be
		// narrower than the marker itself, and printing it whole would push
		// the row past the terminal width and wrap it. Showing where you are
		// beats showing how many tabs you can't see, so the truncated active
		// tab keeps its columns and the marker takes what remains — down to
		// nothing.
		if marker := ansi.Truncate(tabOverflowLabel(hidden), max(0, a.tabBudget()-used), ""); marker != "" {
			sb.WriteString(tabOverflowStyle.Render(marker))
			used += lipgloss.Width(marker)
		}
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
