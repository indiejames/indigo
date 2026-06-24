package client

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/twist/internal/highlight"
)

const tabWidth = 4

// metricsInnerW is the fixed visible width between the box borders.
// Layout per row: 1 space + 9 label + 1 space + 8 value (right-aligned) + 1 space = 20.
const metricsInnerW = 20

func (m *Model) moveCursor(dLine, dCol int) {
	line := max(0, m.cursor.Line+dLine)
	if line >= m.buf.LineCount() {
		line = max(0, m.buf.LineCount()-1)
	}
	lineLen := m.buf.LineLen(line)
	maxCol := lineLen
	if m.mode == ModeNormal && maxCol > 0 {
		maxCol--
	}
	col := max(0, min(m.cursor.Col+dCol, maxCol))
	m.cursor.Line = line
	m.cursor.Col = col
	m.scrollToCursor()
}

func (m *Model) clampCursor() {
	line := min(m.cursor.Line, max(0, m.buf.LineCount()-1))
	col := min(m.cursor.Col, m.buf.LineLen(line))
	m.cursor.Line = line
	m.cursor.Col = col
	m.scrollToCursor()
}

func (m *Model) scrollToCursor() {
	vis := m.visibleLines()
	if m.cursor.Line < m.topLine {
		m.topLine = m.cursor.Line
	}
	if m.cursor.Line >= m.topLine+vis {
		m.topLine = m.cursor.Line - vis + 1
	}
	m.topLine = max(0, m.topLine)
}

func (m Model) visibleLines() int {
	return max(1, m.height-1)
}

// displayLineCount returns the number of lines that receive a line number.
// A trailing empty line (from a final newline) never gets a number — it only
// becomes numbered once the user types at least one character on it.
func (m Model) displayLineCount() int {
	lc := m.buf.LineCount()
	if lc > 1 && m.buf.Line(lc-1) == "" {
		return lc - 1
	}
	return lc
}

// gutterWidth returns the number of columns reserved for line numbers (0 when disabled).
func (m Model) gutterWidth() int {
	if m.cfg == nil || !m.cfg.LineNumbers {
		return 0
	}
	return len(fmt.Sprint(m.displayLineCount())) + 1
}

// selectionCols returns the inclusive [selA, selB] column range selected on
// lineNum, or (-1, -1) when lineNum is not inside the current selection.
func (m Model) selectionCols(lineNum, lineLen int) (selA, selB int) {
	if m.sel == nil {
		return -1, -1
	}
	start, end := m.sel.ordered()
	if lineNum < start.Line || lineNum > end.Line {
		return -1, -1
	}
	selA = 0
	if lineNum == start.Line {
		selA = start.Col
	}
	selB = max(0, lineLen-1)
	if !m.sel.IsLine && lineNum == end.Line {
		selB = min(end.Col, max(0, lineLen-1))
	}
	if lineLen == 0 || selA > selB {
		return -1, -1
	}
	return selA, selB
}

// expandTabsRemap replaces tab runes with spaces (tabWidth-column tab stops).
// Returns the expanded slice and colMap where colMap[i] = visual column of original rune i.
// colMap has len(runes)+1 entries; colMap[len(runes)] is the total visual width.
func expandTabsRemap(runes []rune) (expanded []rune, colMap []int) {
	colMap = make([]int, len(runes)+1)
	vcol := 0
	for i, r := range runes {
		colMap[i] = vcol
		if r == '\t' {
			spaces := tabWidth - (vcol % tabWidth)
			for k := 0; k < spaces; k++ {
				expanded = append(expanded, ' ')
			}
			vcol += spaces
		} else {
			expanded = append(expanded, r)
			vcol++
		}
	}
	colMap[len(runes)] = vcol
	return
}

// renderLineRunes writes runes with selection and cursor highlighting applied.
// selA/selB are inclusive selected column bounds (-1,-1 for no selection).
// curCol is the cursor column (-1 if cursor is not on this line).
func renderLineRunes(sb *strings.Builder, runes []rune, selA, selB, curCol int, spans []highlight.Span) {
	n := len(runes)
	hasCursor := curCol >= 0
	hasSel := selA >= 0

	// spanIdxAt returns the index of the first (highest-priority) span covering col, or -1.
	spanIdxAt := func(col int) int {
		for i, s := range spans {
			if col >= s.StartCol && col < s.EndCol {
				return i
			}
		}
		return -1
	}

	i := 0
	for i < n {
		isCursor := hasCursor && i == curCol
		inSel := hasSel && i >= selA && i <= selB
		switch {
		case isCursor:
			sb.WriteString(cursorStyle.Render(string(runes[i : i+1])))
			i++
		case inSel:
			j := i + 1
			for j < n && !(hasCursor && j == curCol) && j >= selA && j <= selB {
				j++
			}
			sb.WriteString(selectionStyle.Render(string(runes[i:j])))
			i = j
		default:
			// Batch consecutive plain chars with the same highlight span.
			si := spanIdxAt(i)
			j := i + 1
			for j < n {
				if hasCursor && j == curCol {
					break
				}
				if hasSel && j >= selA && j <= selB {
					break
				}
				if spanIdxAt(j) != si {
					break
				}
				j++
			}
			text := string(runes[i:j])
			if si >= 0 {
				sb.WriteString(spans[si].ANSI)
				sb.WriteString(text)
				sb.WriteString(highlight.ANSIReset)
			} else {
				sb.WriteString(text)
			}
			i = j
		}
	}
	if hasCursor && curCol >= n {
		sb.WriteString(cursorStyle.Render(" "))
	}
}

// renderLine renders screen row i (0-based, relative to topLine) without a trailing newline.
// Tabs are expanded to spaces so that cellbuf correctly measures visual widths.
// Each line is padded to m.width so prior content is always fully overwritten.
func (m Model) renderLine(i int) string {
	lineNum := m.topLine + i
	bufLineCount := m.buf.LineCount()
	dispLineCount := m.displayLineCount()
	gutterW := m.gutterWidth()
	var sb strings.Builder

	padToWidth := func(s string) string {
		if m.width <= 0 {
			return s
		}
		w := lipgloss.Width(s)
		if w < m.width {
			return s + strings.Repeat(" ", m.width-w)
		}
		return s
	}

	if lineNum >= bufLineCount {
		if gutterW > 0 {
			sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
		}
		sb.WriteString("~")
		return padToWidth(sb.String())
	}

	if lineNum >= dispLineCount {
		if gutterW > 0 {
			sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
		}
		if lineNum == m.cursor.Line {
			sb.WriteString(cursorStyle.Render(" "))
		} else {
			sb.WriteString("~")
		}
		return padToWidth(sb.String())
	}

	if gutterW > 0 {
		numStr := fmt.Sprintf("%*d ", gutterW-1, lineNum+1)
		if lineNum == m.cursor.Line {
			sb.WriteString(gutterCurStyle.Render(numStr))
		} else {
			sb.WriteString(gutterStyle.Render(numStr))
		}
	}

	line := m.buf.Line(lineNum)
	runes := []rune(line)
	expandedRunes, colMap := expandTabsRemap(runes)

	// Remap selection columns from rune indices to visual columns.
	selA, selB := m.selectionCols(lineNum, len(runes))
	if selA >= 0 {
		newSelA := colMap[selA]
		var newSelB int
		if selB+1 <= len(runes) {
			newSelB = colMap[selB+1] - 1
		} else {
			newSelB = colMap[len(runes)] - 1
		}
		selA = newSelA
		selB = max(newSelA, newSelB)
	}

	// Remap cursor column from rune index to visual column.
	curCol := -1
	if lineNum == m.cursor.Line {
		c := min(m.cursor.Col, len(runes))
		curCol = colMap[c]
	}

	// Remap highlight spans from rune indices to visual columns.
	var remappedSpans []highlight.Span
	if spans := m.hlSpans[lineNum]; len(spans) > 0 {
		remappedSpans = make([]highlight.Span, len(spans))
		for idx, s := range spans {
			newStart := 0
			if s.StartCol < len(colMap) {
				newStart = colMap[s.StartCol]
			}
			newEnd := math.MaxInt
			if s.EndCol != math.MaxInt {
				if s.EndCol < len(colMap) {
					newEnd = colMap[s.EndCol]
				} else {
					newEnd = colMap[len(runes)]
				}
			}
			remappedSpans[idx] = highlight.Span{StartCol: newStart, EndCol: newEnd, ANSI: s.ANSI}
		}
	}

	renderLineRunes(&sb, expandedRunes, selA, selB, curCol, remappedSpans)
	return padToWidth(sb.String())
}

// renderPopupBox builds the styled lines of a popup menu with rounded borders.
func renderPopupBox(title string, items []command, maxW int) []string {
	// Compute needed inner width.
	minInner := len([]rune(title))
	for _, item := range items {
		w := len([]rune(fmt.Sprintf("  %c  %s  ", item.key, item.label)))
		if w > minInner {
			minInner = w
		}
	}
	innerW := minInner
	if innerW+2 > maxW {
		innerW = max(2, maxW-2)
	}

	// Top border with centered title.
	titleStr := title
	titleRunes := []rune(titleStr)
	if len(titleRunes) > innerW {
		titleRunes = titleRunes[:innerW]
		titleStr = string(titleRunes)
	}
	remaining := max(0, innerW-len(titleRunes))
	top := popupBorderStyle.Render("╭") +
		popupTextStyle.Render(titleStr) +
		popupBorderStyle.Render(strings.Repeat("─", remaining)+"╮")

	lines := []string{top}
	for _, item := range items {
		keyPart := fmt.Sprintf("  %c", item.key)
		sep := "  "
		label := item.label
		// Truncate label if needed.
		maxLabel := innerW - len([]rune(keyPart+sep+"  "))
		if maxLabel < 0 {
			maxLabel = 0
		}
		labelRunes := []rune(label)
		if len(labelRunes) > maxLabel {
			label = string(labelRunes[:maxLabel])
		}
		content := keyPart + sep + label
		padW := innerW - len([]rune(content))
		if padW < 0 {
			padW = 0
		}
		row := popupBorderStyle.Render("│") +
			popupKeyStyle.Render(keyPart) +
			popupTextStyle.Render(sep+label+strings.Repeat(" ", padW)) +
			popupBorderStyle.Render("│")
		lines = append(lines, row)
	}

	bottom := popupBorderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")
	lines = append(lines, bottom)
	return lines
}

// renderMetricsBox builds the styled lines of the metrics overlay panel.
func renderMetricsBox(m *metricsData) []string {
	fmtDur := func(d time.Duration) string {
		µs := d.Microseconds()
		if µs < 1000 {
			return fmt.Sprintf("%dµs", µs)
		}
		ms := float64(µs) / 1000
		if ms < 1000 {
			return fmt.Sprintf("%.2fms", ms)
		}
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	rows := []struct{ label, val string }{
		{"render   ", fmtDur(m.renderDuration)},
		{"parse    ", fmtDur(m.highlightDuration)},
		{"key→frame", fmtDur(m.keyToFrameDuration)},
	}
	title := "Metrics"
	top := popupBorderStyle.Render("╭") +
		popupTextStyle.Render(title) +
		popupBorderStyle.Render(strings.Repeat("─", metricsInnerW-len([]rune(title)))+"╮")
	lines := []string{top}
	for _, r := range rows {
		// Right-align value in an 8-char field so the panel width never changes.
		valField := fmt.Sprintf("%8s", r.val)
		line := popupBorderStyle.Render("│") +
			popupTextStyle.Render(" "+r.label+" ") +
			popupKeyStyle.Render(valField) +
			popupTextStyle.Render(" ") +
			popupBorderStyle.Render("│")
		lines = append(lines, line)
	}
	lines = append(lines, popupBorderStyle.Render("╰"+strings.Repeat("─", metricsInnerW)+"╯"))
	return lines
}

// overlayRight truncates mainLine to popCol visual columns and appends popupLine.
func overlayRight(mainLine, popupLine string, popCol int) string {
	truncated := ansi.Truncate(mainLine, popCol, "")
	tw := lipgloss.Width(truncated)
	if tw < popCol {
		truncated += strings.Repeat(" ", popCol-tw)
	}
	return truncated + popupLine
}

// ---- View ----

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "loading…"
	}

	// Record timing via the shared pointer so the value receiver can write back.
	if m.metrics != nil {
		viewStart := time.Now()
		defer func() {
			m.metrics.renderDuration = time.Since(viewStart)
			// Only capture key→frame for the first View() after a key press,
			// then zero lastKeyAt so tick-driven redraws don't keep updating it.
			if !m.metrics.lastKeyAt.IsZero() {
				m.metrics.keyToFrameDuration = time.Since(m.metrics.lastKeyAt)
				m.metrics.lastKeyAt = time.Time{}
			}
		}()
	}

	vis := m.visibleLines()
	lines := make([]string, vis)
	for i := range vis {
		lines[i] = m.renderLine(i)
	}

	// Overlay prefix-command popup in the bottom-right corner.
	if len(m.prefixSeq) > 0 {
		if cmd, ok := findCommand(m.prefixSeq); ok && len(cmd.children) > 0 {
			popup := renderPopupBox(cmd.menuTitle, cmd.children, m.width)
			popH := len(popup)
			popW := lipgloss.Width(popup[0])
			popCol := m.width - popW
			if popCol >= 0 {
				startRow := max(0, vis-popH)
				for pi, popLine := range popup {
					row := startRow + pi
					if row < vis {
						lines[row] = overlayRight(lines[row], popLine, popCol)
					}
				}
			}
		}
	}

	// Overlay command completion popup above the status bar.
	if m.mode == ModeCommand {
		popup := renderCmdCompletionPopup(m.cmdBuf, m.width)
		if len(popup) > 0 {
			popH := len(popup)
			startRow := max(0, vis-popH)
			for pi, popLine := range popup {
				if row := startRow + pi; row < vis {
					lines[row] = popLine
				}
			}
		}
	}

	// Overlay metrics panel in the top-right corner.
	if m.metrics != nil && m.metrics.show {
		box := renderMetricsBox(m.metrics)
		boxW := lipgloss.Width(box[0])
		boxCol := m.width - boxW
		if boxCol >= 0 {
			for ri, boxLine := range box {
				if ri < vis {
					lines[ri] = overlayRight(lines[ri], boxLine, boxCol)
				}
			}
		}
	}

	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(m.renderStatusBar())
	return sb.String()
}

func (m Model) renderStatusBar() string {
	if m.width == 0 {
		return ""
	}

	if m.mode == ModeCommand {
		prompt := ":" + m.cmdBuf
		promptRunes := []rune(prompt)
		maxPromptW := m.width - 1
		if len(promptRunes) > maxPromptW {
			promptRunes = promptRunes[len(promptRunes)-maxPromptW:]
		}
		padW := max(0, m.width-len(promptRunes)-1)
		return barStyle.Render(string(promptRunes)) + cursorStyle.Render(" ") + barStyle.Width(padW).Render("")
	}

	modeLabel := "NORMAL"
	ms := normalModeStyle
	if m.mode == ModeInsert {
		modeLabel = "INSERT"
		ms = insertModeStyle
	}
	left := ms.Render("  " + modeLabel + "  ")
	leftW := lipgloss.Width(left)

	posStr := fmt.Sprintf("  %d:%d  ", m.cursor.Line+1, m.cursor.Col+1)
	right := barStyle.Render(posStr)
	rightW := lipgloss.Width(right)

	var centerContent string
	switch {
	case m.warnQuit:
		centerContent = "Unsaved changes!   Save [s]   Discard [q]   Cancel [esc]"
	case m.status != "":
		centerContent = m.filePath + "   " + m.status
		if m.buf.Dirty() {
			centerContent = m.filePath + " [+]   " + m.status
		}
	default:
		centerContent = m.filePath
		if m.buf.Dirty() {
			centerContent += " [+]"
		}
	}

	centerW := max(0, m.width-leftW-rightW)
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + right
}
