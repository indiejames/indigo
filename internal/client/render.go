package client

import (
	"fmt"
	"math"
	"strings"
	"time"

	"path/filepath"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/highlight"
)

const tabWidth = 4

// decorOverlayStyle is used to render plugin overlay decorations (e.g. jumpy labels).
var decorOverlayStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#FFB300")).
	Background(lipgloss.Color("#1A1A2E")).
	Bold(true)

var (
	searchMatchStyle   = lipgloss.NewStyle().Background(lipgloss.Color("#444400")).Foreground(lipgloss.Color("#FFFF88"))
	searchCurrentStyle = lipgloss.NewStyle().Background(lipgloss.Color("#AAAA00")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
)

// lineOverlay describes a styled text injection at a visual column in the content area.
// col is an index into the tab-expanded rune slice for the line; w is how many of those
// rune positions the text visually occupies (i.e. positions to skip in the underlying content).
type lineOverlay struct {
	col  int
	text string
	w    int
}

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

// hasGutterDecorations reports whether any plugin decoration uses the gutter kind.
func (m Model) hasGutterDecorations() bool {
	for _, d := range m.decorations {
		if d.Kind == ClientDecorationGutter {
			return true
		}
	}
	return false
}

// gutterDecorFor returns the gutter decoration text for lineNum, or "".
func (m Model) gutterDecorFor(lineNum int) string {
	for _, d := range m.decorations {
		if d.Kind == ClientDecorationGutter && int(d.Line) == lineNum {
			return d.Text
		}
	}
	return ""
}

// gutterWidth returns the number of columns reserved for line numbers (and diag/plugin markers).
func (m Model) gutterWidth() int {
	hasPluginGutter := m.hasGutterDecorations()
	if m.cfg == nil || !m.cfg.LineNumbers {
		w := 0
		if len(m.diagnostics) > 0 {
			w += 2 // diag marker
		}
		if hasPluginGutter {
			w += 3 // space + up to 2 chars
		}
		return w
	}
	w := len(fmt.Sprint(m.displayLineCount())) + 1
	if len(m.diagnostics) > 0 {
		w += 2 // space + marker
	}
	if hasPluginGutter {
		w += 3 // space + up to 2 chars
	}
	return w
}

// diagMarker returns a styled "● " for the most severe diagnostic on line, or "  ".
func (m Model) diagMarker(lineNum int) string {
	diags := m.diagsOnLine(lineNum)
	if len(diags) == 0 {
		return "  "
	}
	switch diags[0].Severity {
	case 1:
		return diagErrorStyle.Render("●") + " "
	case 2:
		return diagWarnStyle.Render("●") + " "
	default:
		return diagInfoStyle.Render("●") + " "
	}
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

// renderLineRunes writes runes with selection, cursor, highlight-span, and overlay
// label rendering applied in a single pass. overlays must be sorted by col ascending.
// selA/selB are inclusive selected column bounds (-1,-1 for no selection).
// curCol is the cursor column (-1 if cursor is not on this line).
func renderLineRunes(sb *strings.Builder, runes []rune, selA, selB, curCol int, spans []highlight.Span, overlays []lineOverlay) {
	n := len(runes)
	hasCursor := curCol >= 0
	hasSel := selA >= 0
	oi := 0 // next overlay to inject

	spanIdxAt := func(col int) int {
		for i, s := range spans {
			if col >= s.StartCol && col < s.EndCol {
				return i
			}
		}
		return -1
	}

	nextOvlCol := func() int {
		if oi < len(overlays) {
			return overlays[oi].col
		}
		return math.MaxInt
	}

	i := 0
	for i < n {
		// Drain and inject overlays whose column ≤ i (handles skipped positions too).
		for oi < len(overlays) && overlays[oi].col <= i {
			if overlays[oi].col == i {
				sb.WriteString(overlays[oi].text)
				i += overlays[oi].w
				if i > n {
					i = n
				}
			}
			oi++
		}
		if i >= n {
			break
		}

		noc := nextOvlCol()
		isCursor := hasCursor && i == curCol
		inSel := hasSel && i >= selA && i <= selB
		switch {
		case isCursor:
			sb.WriteString(cursorStyle.Render(string(runes[i : i+1])))
			i++
		case inSel:
			j := i + 1
			for j < n && j < noc && (!hasCursor || j != curCol) && j >= selA && j <= selB {
				j++
			}
			sb.WriteString(selectionStyle.Render(string(runes[i:j])))
			i = j
		default:
			si := spanIdxAt(i)
			j := i + 1
			for j < n && j < noc {
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
	// Write overlays that fall at or past end of content.
	for ; oi < len(overlays); oi++ {
		sb.WriteString(overlays[oi].text)
	}
}

// renderLine renders screen row i (0-based, relative to topLine) without a trailing newline.
// Tabs are expanded to spaces so that cellbuf correctly measures visual widths.
// Each line is padded to m.width so prior content is always fully overwritten.
// overlays (sorted by col) are injected during the rune pass with no ANSI re-parsing.
func (m Model) renderLine(i int, overlays []lineOverlay) string {
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
		hasDiags := len(m.diagnostics) > 0
		hasPluginGutter := m.hasGutterDecorations()
		numW := gutterW
		if hasDiags {
			numW -= 2
		}
		if hasPluginGutter {
			numW -= 3
		}
		if numW > 0 {
			numStr := fmt.Sprintf("%*d ", numW-1, lineNum+1)
			if lineNum == m.cursor.Line {
				sb.WriteString(gutterCurStyle.Render(numStr))
			} else {
				sb.WriteString(gutterStyle.Render(numStr))
			}
		}
		if hasDiags {
			sb.WriteString(m.diagMarker(lineNum))
		}
		if hasPluginGutter {
			text := m.gutterDecorFor(lineNum)
			if text == "" {
				sb.WriteString(gutterStyle.Render("   "))
			} else {
				label := text
				if len([]rune(label)) > 2 {
					label = string([]rune(label)[:2])
				}
				sb.WriteString(" ")
				sb.WriteString(decorOverlayStyle.Render(fmt.Sprintf("%-2s", label)))
			}
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

	renderLineRunes(&sb, expandedRunes, selA, selB, curCol, remappedSpans, overlays)
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

// glamourCache caches the renderer so View() doesn't recreate it every frame.
var glamourCache struct {
	r     *glamour.TermRenderer
	width int
}

// renderHoverPopup renders LSP hover content as markdown inside a rounded border.
func renderHoverPopup(content string, maxW int) []string {
	// Leave room for the lipgloss border (2) and 1-char padding each side.
	renderW := min(76, maxW-4)
	if renderW < 20 {
		renderW = 20
	}

	// Re-use the cached renderer unless the wrap width changed (terminal resize).
	// Use WithStandardStyle("dark") — WithAutoStyle() sends ANSI terminal queries
	// that corrupt bubbletea's input stream.
	if glamourCache.r == nil || glamourCache.width != renderW {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(renderW),
		)
		if err == nil {
			glamourCache.r = r
			glamourCache.width = renderW
		}
	}

	body := content
	if glamourCache.r != nil {
		if rendered, err := glamourCache.r.Render(content); err == nil {
			if trimmed := strings.TrimSpace(rendered); trimmed != "" {
				body = trimmed
			}
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#4488CC"))

	lines := strings.Split(boxStyle.Render(body), "\n")
	// Trim any trailing empty lines that lipgloss may append.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// renderSigHelpBar builds a single-line signature-help display.
func renderSigHelpBar(sh *ClientSigHelp, width int) string {
	if sh == nil || len(sh.Signatures) == 0 {
		return ""
	}
	sig := sh.Signatures[sh.ActiveSignature]
	activeParam := sh.ActiveParameter

	// Build label with active parameter highlighted.
	label := sig.Label
	if activeParam < len(sig.Params) {
		paramLabel := sig.Params[activeParam].Label
		idx := strings.Index(label, paramLabel)
		if idx >= 0 {
			before := label[:idx]
			after := label[idx+len(paramLabel):]
			label = popupTextStyle.Render(before) + popupKeyStyle.Render(paramLabel) + popupTextStyle.Render(after)
		} else {
			label = popupTextStyle.Render(label)
		}
	} else {
		label = popupTextStyle.Render(label)
	}

	if len(sh.Signatures) > 1 {
		counter := fmt.Sprintf(" [%d/%d]", sh.ActiveSignature+1, len(sh.Signatures))
		label += popupBorderStyle.Render(counter)
	}
	w := lipgloss.Width(label)
	if w < width {
		label += popupTextStyle.Render(strings.Repeat(" ", width-w))
	}
	return label
}

// kindAbbrev returns a short 3-char label for a completion kind.
func kindAbbrev(k uint8) string {
	switch k {
	case 2:
		return "mth"
	case 3:
		return "fn "
	case 4:
		return "new"
	case 5:
		return "fld"
	case 6:
		return "var"
	case 7:
		return "cls"
	case 8:
		return "ifc"
	case 9:
		return "mod"
	case 10:
		return "prp"
	case 13:
		return "enm"
	case 14:
		return "kwd"
	case 15:
		return "snp"
	case 21:
		return "cst"
	case 22:
		return "str"
	case 25:
		return "typ"
	default:
		return "   "
	}
}

// renderCompletionPopup builds styled completion list lines.
// Layout per row (between │ borders, width = innerW):
//
//	" " + kind(3) + " " + label + "  " + detail + trailing_spaces
//
// innerW is computed from the widest label and detail across all items.
func renderCompletionPopup(items []ClientCompletion, selected, maxW int) []string {
	const maxVisible = 10
	const kindW = 3
	const maxLabelW = 30
	const maxDetailW = 25

	// Measure natural widths.
	labelW, detailW := 0, 0
	for _, it := range items {
		l := it.InsertText
		if l == "" {
			l = it.Label
		}
		if w := len([]rune(l)); w > labelW {
			labelW = w
		}
		if w := len([]rune(it.Detail)); w > detailW {
			detailW = w
		}
	}
	labelW = min(labelW, maxLabelW)
	detailW = min(detailW, maxDetailW)

	// innerW = 1(lead) + kindW + 1(sep) + labelW + 2(sep) + detailW + 1(trail)
	// If no items have detail, omit the detail columns.
	hasDetail := detailW > 0
	innerW := 1 + kindW + 1 + labelW + 1
	if hasDetail {
		innerW += 1 + detailW // extra sep + detail
	}
	// Cap to screen: total box = innerW+2 must fit in maxW.
	if innerW+2 > maxW {
		innerW = max(20, maxW-2)
		// Trim detail first, then label.
		available := innerW - 1 - kindW - 1 - 1 // subtract fixed cols
		if hasDetail {
			detailW = min(detailW, available/3)
			labelW = available - detailW - 1 // 1 for detail sep
			if labelW < 5 {
				labelW = 5
				detailW = 0
				hasDetail = false
			}
		} else {
			labelW = available
		}
		innerW = 1 + kindW + 1 + labelW + 1
		if hasDetail {
			innerW += 1 + detailW
		}
	}

	// Visible window into the list.
	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	end := min(start+maxVisible, len(items))

	title := "Completions"
	titleR := []rune(title)
	dashCount := max(0, innerW-len(titleR))
	top := popupBorderStyle.Render("╭" + string(titleR) + strings.Repeat("─", dashCount) + "╮")
	lines := []string{top}

	for i := start; i < end; i++ {
		it := items[i]
		label := it.InsertText
		if label == "" {
			label = it.Label
		}
		lr := []rune(label)
		if len(lr) > labelW {
			lr = lr[:labelW]
		}
		kind := kindAbbrev(it.Kind)

		detailPart := ""
		if hasDetail {
			dr := []rune(it.Detail)
			if len(dr) > detailW {
				dr = dr[:detailW]
			}
			detailPart = "  " + string(dr)
		}

		// trailing spaces to fill innerW exactly.
		// filled = 1(lead) + kindW + 1(sep) + len(lr) + len(detailPart) + 1(trail minimum)
		filled := 1 + kindW + 1 + len(lr) + len([]rune(detailPart))
		trail := max(1, innerW-filled)

		if i == selected {
			content := " " + kind + " " + string(lr) + detailPart + strings.Repeat(" ", trail)
			lines = append(lines, popupBorderStyle.Render("│")+selectionStyle.Render(content)+popupBorderStyle.Render("│"))
		} else {
			lines = append(lines,
				popupBorderStyle.Render("│")+
					popupKeyStyle.Render(" "+kind+" ")+
					popupTextStyle.Render(string(lr)+detailPart+strings.Repeat(" ", trail))+
					popupBorderStyle.Render("│"))
		}
	}
	lines = append(lines, popupBorderStyle.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return lines
}

// buildRowOverlays groups overlay decorations by visible row and pre-computes each
// overlay's visual column and styled text. The returned slice has length vis; rows
// without any overlay decoration have a nil entry. Overlays within each row are
// sorted by col so renderLineRunes can inject them in a single left-to-right pass.
func (m Model) buildRowOverlays(vis int) [][]lineOverlay {
	rows := make([][]lineOverlay, vis)
	for _, d := range m.decorations {
		if d.Kind != ClientDecorationOverlay || d.Text == "" {
			continue
		}
		row := int(d.Line) - m.topLine
		if row < 0 || row >= vis {
			continue
		}
		lineStr := m.buf.Line(int(d.Line))
		_, colMap := expandTabsRemap([]rune(lineStr))
		visCol := int(d.Col)
		if int(d.Col) < len(colMap) {
			visCol = colMap[d.Col]
		}
		styledText := decorOverlayStyle.Render(d.Text)
		rows[row] = append(rows[row], lineOverlay{col: visCol, text: styledText, w: lipgloss.Width(styledText)})
	}
	// Sort each row's overlays by column (insertion sort; typically 1-4 items).
	for ri, ovls := range rows {
		for j := 1; j < len(ovls); j++ {
			for k := j; k > 0 && ovls[k].col < ovls[k-1].col; k-- {
				ovls[k], ovls[k-1] = ovls[k-1], ovls[k]
			}
		}
		rows[ri] = ovls
	}
	return rows
}

// buildSearchOverlays builds per-row overlays for all search matches. Returns nil
// when there are no matches.
func (m Model) buildSearchOverlays(vis int) [][]lineOverlay {
	if len(m.searchMatches) == 0 {
		return nil
	}
	rows := make([][]lineOverlay, vis)
	for i, sm := range m.searchMatches {
		row := sm.line - m.topLine
		if row < 0 || row >= vis {
			continue
		}
		lineRunes := []rune(m.buf.Line(sm.line))
		_, colMap := expandTabsRemap(lineRunes)
		visCol := sm.col
		if sm.col < len(colMap) {
			visCol = colMap[sm.col]
		}
		end := sm.col + sm.length
		if end > len(lineRunes) {
			end = len(lineRunes)
		}
		matchText := string(lineRunes[sm.col:end])
		style := searchMatchStyle
		if i == m.searchIdx {
			style = searchCurrentStyle
		}
		styledText := style.Render(matchText)
		rows[row] = append(rows[row], lineOverlay{col: visCol, text: styledText, w: sm.length})
	}
	for ri, ovls := range rows {
		for j := 1; j < len(ovls); j++ {
			for k := j; k > 0 && ovls[k].col < ovls[k-1].col; k-- {
				ovls[k], ovls[k-1] = ovls[k-1], ovls[k]
			}
		}
		rows[ri] = ovls
	}
	return rows
}

// mergeOverlays combines two sorted overlay slices into one sorted slice.
func mergeOverlays(a, b []lineOverlay) []lineOverlay {
	out := make([]lineOverlay, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].col < out[j-1].col; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- View ----

func (m Model) View() string {
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
	rowOverlays := m.buildRowOverlays(vis)
	if searchOverlays := m.buildSearchOverlays(vis); searchOverlays != nil {
		for i := range vis {
			if len(searchOverlays[i]) > 0 {
				rowOverlays[i] = mergeOverlays(searchOverlays[i], rowOverlays[i])
			}
		}
	}
	for i := range vis {
		lines[i] = m.renderLine(i, rowOverlays[i])
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

	// Overlay hover popup (centered).
	if m.hoverContent != nil {
		popup := renderHoverPopup(*m.hoverContent, m.width)
		popH := len(popup)
		popW := lipgloss.Width(popup[0])
		popCol := (m.width - popW) / 2
		if popCol < 0 {
			popCol = 0
		}
		startRow := max(0, vis/2-popH/2)
		for pi, popLine := range popup {
			if row := startRow + pi; row < vis {
				lines[row] = overlayRight(lines[row], popLine, popCol)
			}
		}
	}

	// Overlay signature help above the status bar.
	if m.sigHelp != nil {
		bar := renderSigHelpBar(m.sigHelp, m.width)
		if bar != "" && vis > 0 {
			lines[vis-1] = bar
		}
	}

	// Overlay completion popup near the cursor.
	if m.completionOn && len(m.completions) > 0 {
		popup := renderCompletionPopup(m.completions, m.completionIdx, m.width)
		popH := len(popup)
		popW := lipgloss.Width(popup[0])

		// Horizontal: align left edge with the cursor's screen column.
		gutterW := m.gutterWidth()
		cursorVisCol := m.cursor.Col
		if line := m.buf.Line(m.cursor.Line); len(line) > 0 {
			_, colMap := expandTabsRemap([]rune(line))
			if m.cursor.Col < len(colMap) {
				cursorVisCol = colMap[m.cursor.Col]
			}
		}
		popCol := gutterW + cursorVisCol
		// Shift left if the popup would overflow the right edge.
		if popCol+popW > m.width {
			popCol = max(0, m.width-popW)
		}

		// Vertical: prefer below the cursor, fall back to above.
		cursorScreenRow := m.cursor.Line - m.topLine
		startRow := cursorScreenRow + 1
		if startRow+popH > vis {
			startRow = cursorScreenRow - popH
		}
		if startRow < 0 {
			startRow = 0
		}

		for pi, popLine := range popup {
			if row := startRow + pi; row >= 0 && row < vis {
				lines[row] = overlayRight(lines[row], popLine, popCol)
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

// fileTypeName maps a file extension to a human-readable language name.
func fileTypeName(path string) string {
	switch strings.TrimPrefix(filepath.Ext(path), ".") {
	case "go":
		return "Go"
	case "rs":
		return "Rust"
	case "ts":
		return "TypeScript"
	case "tsx":
		return "TSX"
	case "js":
		return "JavaScript"
	case "jsx":
		return "JSX"
	case "py":
		return "Python"
	case "c":
		return "C"
	case "cpp", "cc", "cxx":
		return "C++"
	case "h":
		return "C Header"
	case "hpp":
		return "C++ Header"
	case "java":
		return "Java"
	case "rb":
		return "Ruby"
	case "lua":
		return "Lua"
	case "zig":
		return "Zig"
	case "md":
		return "Markdown"
	case "toml":
		return "TOML"
	case "json":
		return "JSON"
	case "yaml", "yml":
		return "YAML"
	case "sh", "bash":
		return "Shell"
	case "html", "htm":
		return "HTML"
	case "css":
		return "CSS"
	case "sql":
		return "SQL"
	case "proto":
		return "Protobuf"
	case "capnp":
		return "Cap'n Proto"
	default:
		ext := strings.TrimPrefix(filepath.Ext(path), ".")
		if ext != "" {
			return ext
		}
		return "Plain Text"
	}
}

// lspServerName returns the command name of the configured LSP server for the
// current file, or "" if none is configured.
func (m Model) lspServerName() string {
	if m.cfg == nil {
		return ""
	}
	ext := strings.TrimPrefix(filepath.Ext(m.filePath), ".")
	if ext == "" {
		return ""
	}
	for _, ls := range m.cfg.EffectiveLanguageServers() {
		for _, e := range ls.Extensions {
			if e == ext {
				return ls.Command
			}
		}
	}
	return ""
}

func (m Model) renderStatusBar() string {
	if m.width == 0 {
		return ""
	}

	if m.mode == ModeSearch {
		prompt := "/" + m.searchQuery
		promptRunes := []rune(prompt)
		var countStr string
		switch {
		case m.searchErr != "":
			countStr = " [invalid]"
		case m.searchQuery != "" && len(m.searchMatches) == 0:
			countStr = " [0/0]"
		case len(m.searchMatches) > 0:
			countStr = fmt.Sprintf(" [%d/%d]", m.searchIdx+1, len(m.searchMatches))
		}
		countW := lipgloss.Width(countStr)
		maxPromptW := m.width - countW - 1
		if len(promptRunes) > maxPromptW {
			promptRunes = promptRunes[len(promptRunes)-maxPromptW:]
		}
		padW := max(0, m.width-len(promptRunes)-1-countW)
		return barStyle.Render(string(promptRunes)) + cursorStyle.Render(" ") + barStyle.Width(padW).Render("") + barStyle.Render(countStr)
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

	// Right side: [file type] [lsp] [line:col]
	posStr := fmt.Sprintf("  %d:%d  ", m.cursor.Line+1, m.cursor.Col+1)
	right := barStyle.Render(posStr)

	ftName := fileTypeName(m.filePath)
	right = fileTypeStyle.Render("  "+ftName+"  ") + right

	if lsp := m.lspServerName(); lsp != "" {
		var lspSeg string
		if m.lspActive {
			lspSeg = lspActiveStyle.Render("  " + lsp + " ●  ")
		} else {
			lspSeg = lspIdleStyle.Render("  " + lsp + "  ")
		}
		right = lspSeg + right
	}

	rightW := lipgloss.Width(right)

	var centerContent string
	switch {
	case m.recoveryPrompt:
		centerContent = "Recovery file found!   Use it [y]   Ignore and delete [n]"
	case m.warnQuit:
		centerContent = "Unsaved changes!   Save [s]   Discard [q]   Cancel [esc]"
	case m.status != "":
		centerContent = m.filePath + "   " + m.status
		if m.buf.Dirty() {
			centerContent = m.filePath + " [+]   " + m.status
		}
	default:
		// Plugin status bar decorations take priority over diagnostics.
		var pluginStatus string
		for _, d := range m.decorations {
			if d.Kind == ClientDecorationStatusBar && d.Text != "" {
				pluginStatus = d.Text
				break
			}
		}
		if pluginStatus != "" {
			centerContent = pluginStatus
		} else if diags := m.diagsOnLine(m.cursor.Line); len(diags) > 0 {
			// Show most-severe diagnostic on the cursor line.
			d := diags[0]
			src := d.Source
			if src != "" {
				src = "[" + src + "] "
			}
			centerContent = src + d.Message
		} else {
			centerContent = m.filePath
			if m.buf.Dirty() {
				centerContent += " [+]"
			}
		}
	}

	centerW := max(0, m.width-leftW-rightW)
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + right
}
