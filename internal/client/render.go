package client

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"path/filepath"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/highlight"
)

const tabWidth = 4

// diagUnderlineStyle is the underline style used for LSP diagnostics.
// Set once at startup based on terminal capabilities.
var diagUnderlineStyle = func() ClientUnderlineStyle {
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "kitty", "ghostty":
		return ClientUnderlineCurly
	default:
		return ClientUnderlineStraight
	}
}()

// diagColor maps LSP severity (1=error, 2=warn, 3+=info) to the active theme hex color.
func diagColor(severity uint8) string {
	switch severity {
	case 1:
		return activeDiagError
	case 2:
		return activeDiagWarn
	default:
		return activeDiagInfo
	}
}

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

// contentWidth returns the number of terminal columns available for buffer
// text after subtracting the gutter. Always at least 1.
func (m Model) contentWidth() int {
	return max(1, m.width-m.gutterWidth())
}

// visualChunks returns the number of screen rows that a tab-expanded line of
// length expandedLen occupies when soft-wrapped at cw columns. An empty line
// still counts as one row.
func visualChunks(expandedLen, cw int) int {
	if cw <= 0 || expandedLen == 0 {
		return 1
	}
	return (expandedLen + cw - 1) / cw
}

// layoutEntry describes one screen row in the current frame.
type layoutEntry struct {
	bufLine    int // buffer line index
	chunk      int // 0-based wrap segment within bufLine
	chunkStart int // visual column where this chunk begins (chunk * cw)
}

// buildScreenLayout returns a slice mapping each visible screen row to its
// (bufLine, chunk). Rows beyond the last buffer line use incrementing bufLine
// values for the tilde-row renderer.
func (m Model) buildScreenLayout(vis, cw int) []layoutEntry {
	layout := make([]layoutEntry, 0, vis)
	bufLine := m.topLine
	for len(layout) < vis {
		if bufLine >= m.buf.LineCount() {
			layout = append(layout, layoutEntry{bufLine: bufLine})
			bufLine++
			continue
		}
		runes := []rune(m.buf.Line(bufLine))
		exp, _ := expandTabsRemap(runes)
		chunks := visualChunks(len(exp), cw)
		for chunk := 0; chunk < chunks && len(layout) < vis; chunk++ {
			layout = append(layout, layoutEntry{
				bufLine:    bufLine,
				chunk:      chunk,
				chunkStart: chunk * cw,
			})
		}
		bufLine++
	}
	return layout
}

// screenRowOf returns the screen row index in layout where the given bufLine
// and visual column visCol fall, or -1 if not currently visible.
func screenRowOf(layout []layoutEntry, bufLine, visCol, cw int) int {
	chunk := 0
	if cw > 0 {
		chunk = visCol / cw
	}
	for i, e := range layout {
		if e.bufLine == bufLine && e.chunk == chunk {
			return i
		}
	}
	return -1
}

// cursorVisualRowFromTop returns how many screen rows below topLine the
// cursor currently sits (accounting for soft-wrap).
func (m Model) cursorVisualRowFromTop(cw int) int {
	row := 0
	for l := m.topLine; l < m.cursor.Line && l < m.buf.LineCount(); l++ {
		runes := []rune(m.buf.Line(l))
		exp, _ := expandTabsRemap(runes)
		row += visualChunks(len(exp), cw)
	}
	if m.cursor.Line < m.buf.LineCount() {
		runes := []rune(m.buf.Line(m.cursor.Line))
		_, colMap := expandTabsRemap(runes)
		curVisCol := 0
		if m.cursor.Col < len(colMap) {
			curVisCol = colMap[m.cursor.Col]
		}
		if cw > 0 {
			row += curVisCol / cw
		}
	}
	return row
}

// findTopLineForCursor returns the buffer line that should be at the top of
// the viewport so the cursor falls within the last visible row.
func (m Model) findTopLineForCursor(cw, vis int) int {
	// Determine which wrap chunk of the cursor's line the cursor is on.
	cursorChunk := 0
	if m.cursor.Line < m.buf.LineCount() {
		runes := []rune(m.buf.Line(m.cursor.Line))
		_, colMap := expandTabsRemap(runes)
		curVisCol := 0
		if m.cursor.Col < len(colMap) {
			curVisCol = colMap[m.cursor.Col]
		}
		if cw > 0 {
			cursorChunk = curVisCol / cw
		}
	}
	// Number of rows above the cursor's chunk that we can fill.
	rowsAbove := vis - 1 - cursorChunk
	if rowsAbove <= 0 {
		return m.cursor.Line
	}
	l := m.cursor.Line - 1
	for l >= 0 {
		runes := []rune(m.buf.Line(l))
		exp, _ := expandTabsRemap(runes)
		chunks := visualChunks(len(exp), cw)
		if chunks >= rowsAbove {
			return l
		}
		rowsAbove -= chunks
		l--
	}
	return max(0, l+1)
}

func (m *Model) scrollToCursor() {
	if m.cursor.Line < m.topLine {
		m.topLine = m.cursor.Line
		return
	}
	cw := m.contentWidth()
	vis := m.visibleLines()
	curVisRow := m.cursorVisualRowFromTop(cw)
	if curVisRow < vis {
		return
	}
	m.topLine = m.findTopLineForCursor(cw, vis)
	if m.topLine < 0 {
		m.topLine = 0
	}
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

// gutterDecorAt returns the gutter ClientDecoration for lineNum, or nil.
func (m Model) gutterDecorAt(lineNum int) *ClientDecoration {
	for i := range m.decorations {
		if m.decorations[i].Kind == ClientDecorationGutter && int(m.decorations[i].Line) == lineNum {
			return &m.decorations[i]
		}
	}
	return nil
}

// gutterWidth returns the number of columns reserved for line numbers (and plugin markers).
func (m Model) gutterWidth() int {
	hasPluginGutter := m.reservePluginGutter || m.hasGutterDecorations()
	if m.cfg == nil || !m.cfg.LineNumbers {
		if hasPluginGutter {
			return 3 // space + up to 2 chars
		}
		return 0
	}
	w := len(fmt.Sprint(m.displayLineCount())) + 1
	if hasPluginGutter {
		w += 3 // space + up to 2 chars
	}
	return w
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

// underlineRange is an underline decoration applied additively over syntax highlighting.
// StartSeq opens the underline (e.g. "\x1b[4m"); the close is always "\x1b[24m".
type underlineRange struct {
	StartCol int
	EndCol   int
	StartSeq string
}

// renderLineRunes writes runes with selection, cursor, highlight-span, underline,
// and overlay label rendering applied in a single pass.
// overlays must be sorted by col ascending.
// selA/selB are inclusive selected column bounds (-1,-1 for no selection).
// curCol is the cursor column (-1 if cursor is not on this line).
func renderLineRunes(sb *strings.Builder, runes []rune, selA, selB, curCol int, spans []highlight.Span, overlays []lineOverlay, underlines []underlineRange) {
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

	underlineIdxAt := func(col int) int {
		for i, u := range underlines {
			if col >= u.StartCol && col < u.EndCol {
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
			ui := underlineIdxAt(i)
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
				if underlineIdxAt(j) != ui {
					break
				}
				j++
			}
			text := string(runes[i:j])
			// Emit syntax color first, then underline on top (additive).
			if si >= 0 {
				sb.WriteString(spans[si].ANSI)
			}
			if ui >= 0 {
				sb.WriteString(underlines[ui].StartSeq)
			}
			sb.WriteString(text)
			// Close underline first (preserves color), then reset color.
			if ui >= 0 {
				sb.WriteString("\x1b[24m") // underline off only
			}
			if si >= 0 {
				sb.WriteString(highlight.ANSIReset)
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

// renderLineChunk renders one wrap-chunk of a buffer line. overlays must have
// chunk-relative column indices (i.e. already adjusted by chunkStart). Each
// row is padded to m.width so prior terminal content is always overwritten.
func (m Model) renderLineChunk(entry layoutEntry, cw int, overlays []lineOverlay) string {
	lineNum := entry.bufLine
	chunk := entry.chunk
	chunkStart := entry.chunkStart
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
		if lineNum == m.cursor.Line && chunk == 0 {
			sb.WriteString(cursorStyle.Render(" "))
		} else {
			sb.WriteString("~")
		}
		return padToWidth(sb.String())
	}

	if gutterW > 0 {
		hasPluginGutter := m.hasGutterDecorations()
		if chunk == 0 {
			numW := gutterW
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
			if hasPluginGutter {
				decor := m.gutterDecorAt(lineNum)
				if decor == nil || decor.Text == "" {
					sb.WriteString(gutterStyle.Render("   "))
				} else if decor.TextColor != "" {
					// Solid 1-cell colored bar: wider than a thin │ line, narrower than a 2-cell block.
					barStyle := lipgloss.NewStyle().Background(lipgloss.Color(decor.TextColor))
					sb.WriteString(" ")
					sb.WriteString(barStyle.Render(" "))
					sb.WriteString(" ")
				} else {
					label := decor.Text
					if len([]rune(label)) > 2 {
						label = string([]rune(label)[:2])
					}
					sb.WriteString(" ")
					sb.WriteString(decorOverlayStyle.Render(fmt.Sprintf("%-2s", label)))
				}
			}
		} else {
			sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
		}
	}

	line := m.buf.Line(lineNum)
	runes := []rune(line)
	expandedRunes, colMap := expandTabsRemap(runes)

	// Slice to this chunk's visual column range.
	chunkEnd := min(chunkStart+cw, len(expandedRunes))
	chunkRunes := expandedRunes[chunkStart:chunkEnd]

	// Remap selection columns to chunk-relative visual cols.
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
		if selA >= chunkStart+cw || selB < chunkStart {
			selA, selB = -1, -1
		} else {
			selA = max(0, selA-chunkStart)
			selB = min(cw-1, selB-chunkStart)
		}
	}

	// Remap cursor column to chunk-relative visual col.
	curCol := -1
	if lineNum == m.cursor.Line {
		c := min(m.cursor.Col, len(runes))
		absVisCol := colMap[c]
		if absVisCol >= chunkStart && absVisCol < chunkStart+cw {
			curCol = absVisCol - chunkStart
		}
		// Show cursor at end of last chunk when cursor is past all content.
		if chunk > 0 && absVisCol == len(expandedRunes) && chunkStart+cw >= len(expandedRunes) {
			curCol = absVisCol - chunkStart
		}
	}

	// Remap highlight spans to chunk-relative visual cols.
	var remappedSpans []highlight.Span
	if spans := m.hlSpans[lineNum]; len(spans) > 0 {
		for _, s := range spans {
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
			if newStart >= chunkStart+cw || newEnd <= chunkStart {
				continue
			}
			remappedSpans = append(remappedSpans, highlight.Span{
				StartCol: max(0, newStart-chunkStart),
				EndCol:   min(cw, newEnd-chunkStart),
				ANSI:     s.ANSI,
			})
		}
	}

	// Collect underline decoration ranges for this line (separate from syntax spans).
	// Diagnostic underlines are prepended so they take precedence over plugin underlines.
	var underlines []underlineRange
	for _, d := range m.diagnostics {
		// Handle single-line and multi-line diagnostics.
		var startCol, endCol int
		switch {
		case d.Line == lineNum && d.EndLine == lineNum:
			startCol, endCol = d.Col, d.EndCol
		case d.Line == lineNum && d.EndLine > lineNum:
			startCol, endCol = d.Col, len(runes)
		case d.Line < lineNum && d.EndLine == lineNum:
			startCol, endCol = 0, d.EndCol
		case d.Line < lineNum && d.EndLine > lineNum:
			startCol, endCol = 0, len(runes)
		default:
			continue
		}
		if startCol >= endCol {
			endCol = startCol + 1 // ensure at least one char is underlined
		}
		startVis := len(expandedRunes)
		if startCol < len(colMap) {
			startVis = colMap[startCol]
		}
		endVis := len(expandedRunes)
		if endCol < len(colMap) {
			endVis = colMap[endCol]
		}
		if startVis >= chunkStart+cw || endVis <= chunkStart {
			continue
		}
		seq := underlineANSI(diagUnderlineStyle, diagColor(d.Severity))
		underlines = append(underlines, underlineRange{
			StartCol: max(0, startVis-chunkStart),
			EndCol:   min(cw, endVis-chunkStart),
			StartSeq: seq,
		})
	}
	for _, d := range m.decorations {
		if d.Kind != ClientDecorationUnderline || int(d.Line) != lineNum {
			continue
		}
		ansiSeq := underlineANSI(d.UnderlineStyle, d.UnderlineColor)
		if ansiSeq == "" {
			continue
		}
		startVis := len(expandedRunes)
		if int(d.Col) < len(colMap) {
			startVis = colMap[d.Col]
		}
		endVis := len(expandedRunes)
		if int(d.EndCol) < len(colMap) {
			endVis = colMap[d.EndCol]
		}
		if startVis >= chunkStart+cw || endVis <= chunkStart {
			continue
		}
		underlines = append(underlines, underlineRange{
			StartCol: max(0, startVis-chunkStart),
			EndCol:   min(cw, endVis-chunkStart),
			StartSeq: ansiSeq,
		})
	}

	renderLineRunes(&sb, chunkRunes, selA, selB, curCol, remappedSpans, overlays, underlines)
	return padToWidth(sb.String())
}

// renderFixPopup builds styled lines for the fix suggestion popup.
func renderFixPopup(items []ClientFixItem, selected, maxW int) []string {
	const maxVisible = 10

	labelW := 0
	for _, it := range items {
		if w := len([]rune(it.Label)); w > labelW {
			labelW = w
		}
	}
	labelW = min(labelW, 50)
	// innerW = 1(lead) + labelW + 1(trail)
	innerW := 2 + labelW
	if innerW+2 > maxW {
		innerW = max(10, maxW-2)
		labelW = innerW - 2
	}

	title := "Fix"
	titleR := []rune(title)
	dashCount := max(0, innerW-len(titleR))
	top := popupBorderStyle.Render(bdrTL + string(titleR) + strings.Repeat(bdrH, dashCount) + bdrTR)
	lines := []string{top}

	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	end := min(start+maxVisible, len(items))

	for i := start; i < end; i++ {
		lr := []rune(items[i].Label)
		if len(lr) > labelW {
			lr = lr[:labelW]
		}
		trail := max(1, innerW-1-len(lr))
		if i == selected {
			content := " " + string(lr) + strings.Repeat(" ", trail)
			lines = append(lines, popupBorderStyle.Render(bdrV)+selectionStyle.Render(content)+popupBorderStyle.Render(bdrV))
		} else {
			lines = append(lines,
				popupBorderStyle.Render(bdrV)+
					popupTextStyle.Render(" "+string(lr)+strings.Repeat(" ", trail))+
					popupBorderStyle.Render(bdrV))
		}
	}
	lines = append(lines, popupBorderStyle.Render(bdrBL+strings.Repeat(bdrH, innerW)+bdrBR))
	return lines
}

// renderDiagPopup builds styled lines for the diagnostic detail popup (Shift+E).
// It shows all diagnostics for a single line, full terminal width.
func renderDiagPopup(diags []ClientDiag, termW int) []string {
	innerW := max(10, termW-2)

	title := "Diagnostics"
	dashes := max(0, innerW-len([]rune(title)))
	top := popupBorderStyle.Render(bdrTL) +
		popupTextStyle.Render(title) +
		popupBorderStyle.Render(strings.Repeat(bdrH, dashes)+bdrTR)
	lines := []string{top}

	for _, d := range diags {
		// Choose marker style by severity.
		var markerStyle lipgloss.Style
		switch d.Severity {
		case 1:
			markerStyle = diagErrorStyle.Background(popupBg)
		case 2:
			markerStyle = diagWarnStyle.Background(popupBg)
		default:
			markerStyle = diagInfoStyle.Background(popupBg)
		}

		// Build source tag plain text.
		srcText := ""
		if d.Source != "" {
			srcText = "[" + d.Source + "] "
		}

		// Compute how much room is left for the message.
		// Row layout (visible chars): 1(space) + 1(●) + 1(space) + len(srcText) + len(msg) + trailing
		used := 3 + len([]rune(srcText))
		avail := max(0, innerW-used)
		msgRunes := []rune(d.Message)
		if len(msgRunes) > avail {
			msgRunes = append([]rune(string(msgRunes[:max(0, avail-1)])), '…')
		}
		trail := max(0, innerW-used-len(msgRunes))

		row := popupBorderStyle.Render(bdrV) +
			popupTextStyle.Render(" ") +
			markerStyle.Render("●") +
			popupTextStyle.Render(" "+srcText+string(msgRunes)+strings.Repeat(" ", trail)) +
			popupBorderStyle.Render(bdrV)
		lines = append(lines, row)
	}

	lines = append(lines, popupBorderStyle.Render(bdrBL+strings.Repeat(bdrH, innerW)+bdrBR))
	return lines
}

// underlineANSI builds the SGR sequence for an underline decoration.
// Returns "" for UnderlineNone or unknown styles.
func underlineANSI(style ClientUnderlineStyle, hexColor string) string {
	var base string
	switch style {
	case ClientUnderlineCurly:
		base = "\x1b[4:3m"
	case ClientUnderlineStraight:
		base = "\x1b[4m"
	default:
		return ""
	}
	if len(hexColor) != 7 || hexColor[0] != '#' {
		return base
	}
	r, rerr := strconv.ParseUint(hexColor[1:3], 16, 8)
	g, gerr := strconv.ParseUint(hexColor[3:5], 16, 8)
	b, berr := strconv.ParseUint(hexColor[5:7], 16, 8)
	if rerr != nil || gerr != nil || berr != nil {
		return base
	}
	return base + fmt.Sprintf("\x1b[58:2::%d:%d:%dm", r, g, b)
}

// renderLine renders screen row i for backward compatibility with tests. In
// production code View() calls renderLineChunk directly via the layout.
func (m Model) renderLine(i int, overlays []lineOverlay) string {
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	if i < len(layout) {
		return m.renderLineChunk(layout[i], cw, overlays)
	}
	return m.renderLineChunk(layoutEntry{bufLine: m.topLine + i}, cw, overlays)
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
	top := popupBorderStyle.Render(bdrTL) +
		popupTextStyle.Render(titleStr) +
		popupBorderStyle.Render(strings.Repeat(bdrH, remaining)+bdrTR)

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
		row := popupBorderStyle.Render(bdrV) +
			popupKeyStyle.Render(keyPart) +
			popupTextStyle.Render(sep+label+strings.Repeat(" ", padW)) +
			popupBorderStyle.Render(bdrV)
		lines = append(lines, row)
	}

	bottom := popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
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
	top := popupBorderStyle.Render(bdrTL) +
		popupTextStyle.Render(title) +
		popupBorderStyle.Render(strings.Repeat(bdrH, metricsInnerW-len([]rune(title)))+bdrTR)
	lines := []string{top}
	for _, r := range rows {
		// Right-align value in an 8-char field so the panel width never changes.
		valField := fmt.Sprintf("%8s", r.val)
		line := popupBorderStyle.Render(bdrV) +
			popupTextStyle.Render(" "+r.label+" ") +
			popupKeyStyle.Render(valField) +
			popupTextStyle.Render(" ") +
			popupBorderStyle.Render(bdrV)
		lines = append(lines, line)
	}
	lines = append(lines, popupBorderStyle.Render(bdrBL+strings.Repeat(bdrH, metricsInnerW)+bdrBR))
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

// hoverBodyLines renders markdown content through glamour and returns the
// resulting lines. Used both to count total lines (for scroll clamping) and
// as the source for renderHoverPopup.
func hoverBodyLines(content string, maxW int) []string {
	renderW := min(76, maxW-4)
	if renderW < 20 {
		renderW = 20
	}
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
	return strings.Split(body, "\n")
}

// renderHoverPopup renders LSP hover content as markdown inside a rounded border.
// scroll is the number of content lines to skip from the top.
// maxH is the maximum number of rendered lines to return (0 = unlimited).
func renderHoverPopup(content string, maxW, scroll, maxH int) []string {
	bodyLines := hoverBodyLines(content, maxW)
	totalLines := len(bodyLines)

	// Lines available for content inside the border (border takes top + bottom row).
	contentH := maxH - 2
	needsScrolling := maxH > 2 && totalLines > contentH

	if needsScrolling {
		// Clamp scroll so we never go past the last full window.
		scroll = min(scroll, max(0, totalLines-contentH))
		bodyLines = bodyLines[scroll:]
		// Clip to window height...
		if len(bodyLines) > contentH {
			bodyLines = bodyLines[:contentH]
		}
		// ...and pad to the same fixed height when near the end, so the popup
		// doesn't shrink as the user scrolls toward the last lines.
		for len(bodyLines) < contentH {
			bodyLines = append(bodyLines, "")
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(bdrLipgloss).
		BorderForeground(popupBorderStyle.GetForeground())

	lines := strings.Split(boxStyle.Render(strings.Join(bodyLines, "\n")), "\n")
	// Trim any trailing empty lines that lipgloss may append.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// Scroll indicator when content doesn't all fit.
	if needsScrolling {
		shown := min(scroll+contentH, totalLines)
		indicator := popupBorderStyle.Render(fmt.Sprintf(" ↑↓ j/k   %d/%d lines ", shown, totalLines))
		lines = append(lines, indicator)
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
	top := popupBorderStyle.Render(bdrTL + string(titleR) + strings.Repeat(bdrH, dashCount) + bdrTR)
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
			lines = append(lines, popupBorderStyle.Render(bdrV)+selectionStyle.Render(content)+popupBorderStyle.Render(bdrV))
		} else {
			lines = append(lines,
				popupBorderStyle.Render(bdrV)+
					popupKeyStyle.Render(" "+kind+" ")+
					popupTextStyle.Render(string(lr)+detailPart+strings.Repeat(" ", trail))+
					popupBorderStyle.Render(bdrV))
		}
	}
	lines = append(lines, popupBorderStyle.Render(bdrBL+strings.Repeat(bdrH, innerW)+bdrBR))
	return lines
}

// buildRowOverlays groups overlay decorations by visible screen row. Overlay
// column values are stored chunk-relative so renderLineRunes can consume them
// directly. The returned slice has length len(layout).
func (m Model) buildRowOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	vis := len(layout)
	rows := make([][]lineOverlay, vis)
	for _, d := range m.decorations {
		if d.Kind != ClientDecorationOverlay || d.Text == "" {
			continue
		}
		lineStr := m.buf.Line(int(d.Line))
		_, colMap := expandTabsRemap([]rune(lineStr))
		visCol := int(d.Col)
		if int(d.Col) < len(colMap) {
			visCol = colMap[d.Col]
		}
		row := screenRowOf(layout, int(d.Line), visCol, cw)
		if row < 0 || row >= vis {
			continue
		}
		chunkStart := layout[row].chunkStart
		styledText := decorOverlayStyle.Render(d.Text)
		rows[row] = append(rows[row], lineOverlay{col: visCol - chunkStart, text: styledText, w: lipgloss.Width(styledText)})
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

// buildSearchOverlays builds per-row overlays for all search matches. Overlay
// column values are chunk-relative. Returns nil when there are no matches.
func (m Model) buildSearchOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	if len(m.searchMatches) == 0 {
		return nil
	}
	vis := len(layout)
	rows := make([][]lineOverlay, vis)
	for i, sm := range m.searchMatches {
		lineRunes := []rune(m.buf.Line(sm.line))
		_, colMap := expandTabsRemap(lineRunes)
		visCol := sm.col
		if sm.col < len(colMap) {
			visCol = colMap[sm.col]
		}
		row := screenRowOf(layout, sm.line, visCol, cw)
		if row < 0 || row >= vis {
			continue
		}
		chunkStart := layout[row].chunkStart
		matchEnd := min(sm.col+sm.length, len(lineRunes))
		style := searchMatchStyle
		if i == m.searchIdx {
			style = searchCurrentStyle
		}

		// For the current match, leave the cursor column uncovered so the cursor
		// remains visible on top of the highlight.
		if i == m.searchIdx && sm.line == m.cursor.Line {
			c := min(m.cursor.Col, len(lineRunes))
			cursorVisCol := colMap[c]
			if m.cursor.Col >= sm.col && m.cursor.Col < matchEnd {
				if m.cursor.Col > sm.col {
					rows[row] = append(rows[row], lineOverlay{
						col:  visCol - chunkStart,
						text: style.Render(string(lineRunes[sm.col:m.cursor.Col])),
						w:    m.cursor.Col - sm.col,
					})
				}
				if m.cursor.Col+1 < matchEnd {
					rows[row] = append(rows[row], lineOverlay{
						col:  cursorVisCol + 1 - chunkStart,
						text: style.Render(string(lineRunes[m.cursor.Col+1 : matchEnd])),
						w:    matchEnd - (m.cursor.Col + 1),
					})
				}
				continue
			}
		}

		rows[row] = append(rows[row], lineOverlay{
			col:  visCol - chunkStart,
			text: style.Render(string(lineRunes[sm.col:matchEnd])),
			w:    sm.length,
		})
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

// ---- Help popup ----

type helpEntry struct {
	key  string // left column
	desc string // right column; empty = section header
}

// helpEntries is the complete reference displayed by the ? popup.
var helpEntries = []helpEntry{
	{key: "Navigation"},
	{key: "h / ←", desc: "Move left"},
	{key: "j / ↓", desc: "Move down"},
	{key: "k / ↑", desc: "Move up"},
	{key: "l / →", desc: "Move right"},
	{key: "b", desc: "Move to previous word start"},
	{key: "e", desc: "Move to word end"},
	{key: "0 / ^ / Home", desc: "Line start"},
	{key: "$ / End", desc: "Line end"},
	{key: "G", desc: "End of file"},
	{key: "Ctrl+f / PgDn", desc: "Page down"},
	{key: "Ctrl+b / PgUp", desc: "Page up"},
	{key: ""},
	{key: "Go to (g…)"},
	{key: "gg", desc: "Top of file"},
	{key: "ge", desc: "End of file"},
	{key: "gd", desc: "Go to definition"},
	{key: "gh", desc: "Line start"},
	{key: "gl", desc: "Line end"},
	{key: "gs", desc: "First non-whitespace"},
	{key: "gb", desc: "Open buffer picker"},
	{key: ""},
	{key: "Editing"},
	{key: "i", desc: "Insert before cursor"},
	{key: "a", desc: "Insert after cursor"},
	{key: "A", desc: "Insert at line end"},
	{key: "o", desc: "New line below"},
	{key: "O", desc: "New line above"},
	{key: "d", desc: "Delete selection"},
	{key: "c", desc: "Change selection (delete + insert)"},
	{key: "y", desc: "Yank (copy) selection"},
	{key: "u", desc: "Undo"},
	{key: "U", desc: "Redo"},
	{key: ""},
	{key: "Selection"},
	{key: "w", desc: "Select word"},
	{key: "W", desc: "Extend word forward"},
	{key: "x", desc: "Select line"},
	{key: "X", desc: "Extend line backward"},
	{key: "%", desc: "Select all"},
	{key: ";", desc: "Clear selection"},
	{key: "Alt+;", desc: "Flip selection (swap anchor/head)"},
	{key: "mi / mw", desc: "Select inside object / word"},
	{key: ""},
	{key: "Search"},
	{key: "/", desc: "Start search"},
	{key: "n", desc: "Next match"},
	{key: "N", desc: "Previous match"},
	{key: ""},
	{key: "Multi-cursor"},
	{key: "Ctrl+d", desc: "Add cursor at next occurrence"},
	{key: "C", desc: "Add cursor below"},
	{key: "Alt+s", desc: "Split selection into cursors"},
	{key: ""},
	{key: "LSP / Diagnostics"},
	{key: "K", desc: "Hover documentation"},
	{key: "E", desc: "Toggle diagnostic detail"},
	{key: "F", desc: "Fix suggestions"},
	{key: ""},
	{key: "Files & Buffers"},
	{key: "Ctrl+p", desc: "File picker"},
	{key: "Ctrl+s", desc: "Save"},
	{key: "]b / [b", desc: "Next / previous buffer"},
	{key: ":"},
	{key: "  w", desc: "Save"},
	{key: "  q", desc: "Close buffer"},
	{key: "  wq", desc: "Save and close"},
	{key: "  e <path>", desc: "Open file"},
	{key: "  themes", desc: "List available themes"},
	{key: "  theme <name>", desc: "Switch theme"},
}

const helpKeyW = 18 // fixed left-column width for key strings

// helpPopupLines returns the pre-rendered text lines of the help popup body
// (without the border). Called from both handleKey (for scroll-clamping) and
// renderHelpPopup (for display).
func helpPopupLines() []string {
	lines := make([]string, 0, len(helpEntries))
	for _, e := range helpEntries {
		if e.desc == "" {
			if e.key == "" {
				// blank spacer
				lines = append(lines, "")
			} else {
				// section header
				lines = append(lines, popupKeyStyle.Render(e.key))
			}
		} else {
			key := e.key
			if len([]rune(key)) > helpKeyW {
				key = string([]rune(key)[:helpKeyW])
			}
			padded := fmt.Sprintf("  %-*s  ", helpKeyW, key)
			lines = append(lines, popupTextStyle.Render(padded)+popupTextStyle.Render(e.desc))
		}
	}
	return lines
}

// renderHelpPopup renders the help popup as a slice of styled, full-width lines.
// innerW is the content width (excluding borders).
func renderHelpPopup(width, scroll, maxH int) []string {
	innerW := min(width-4, 64) // cap at 64 so it doesn't sprawl on huge terminals
	if innerW < 30 {
		innerW = max(30, width-4)
	}

	bodyLines := helpPopupLines()
	total := len(bodyLines)
	contentH := maxH - 2
	if contentH < 1 {
		contentH = 1
	}
	// Clamp scroll.
	if scroll > total-contentH {
		scroll = max(0, total-contentH)
	}
	end := min(scroll+contentH, total)
	visible := bodyLines[scroll:end]
	// Pad to fixed height so the box doesn't shrink near the bottom.
	for len(visible) < contentH {
		visible = append(visible, "")
	}

	// Pad each line to innerW visual columns.
	padded := make([]string, len(visible))
	for i, line := range visible {
		lw := lipgloss.Width(line)
		if lw < innerW {
			line += popupTextStyle.Render(strings.Repeat(" ", innerW-lw))
		}
		padded[i] = line
	}

	title := "Help — ? to close"
	titleRunes := []rune(title)
	dashes := max(0, innerW-len(titleRunes))
	top := popupBorderStyle.Render(bdrTL+string(titleRunes)) +
		popupBorderStyle.Render(strings.Repeat(bdrH, dashes)+bdrTR)

	out := []string{top}
	for _, line := range padded {
		out = append(out, popupBorderStyle.Render(bdrV)+line+popupBorderStyle.Render(bdrV))
	}

	needsScroll := total > contentH
	var bottom string
	if needsScroll {
		shown := min(scroll+contentH, total)
		indicator := fmt.Sprintf(" j/k scroll   %d/%d ", shown, total)
		indicatorRunes := []rune(indicator)
		remainDashes := max(0, innerW-len(indicatorRunes))
		bottom = popupBorderStyle.Render(bdrBL+indicator+strings.Repeat(bdrH, remainDashes)+bdrBR)
	} else {
		bottom = popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
	}
	out = append(out, bottom)
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
	cw := m.contentWidth()
	layout := m.buildScreenLayout(vis, cw)
	lines := make([]string, vis)
	rowOverlays := m.buildRowOverlays(layout, cw)
	if searchOverlays := m.buildSearchOverlays(layout, cw); searchOverlays != nil {
		for i := range vis {
			if len(searchOverlays[i]) > 0 {
				rowOverlays[i] = mergeOverlays(searchOverlays[i], rowOverlays[i])
			}
		}
	}
	if extraOverlays := m.buildExtraCursorOverlays(layout, cw); extraOverlays != nil {
		for i := range vis {
			if len(extraOverlays[i]) > 0 {
				rowOverlays[i] = mergeOverlays(rowOverlays[i], extraOverlays[i])
			}
		}
	}
	for i := range vis {
		lines[i] = m.renderLineChunk(layout[i], cw, rowOverlays[i])
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
		popup := renderCmdCompletionPopup(m.cmdBuf, m.cmdCompletionIdx, m.width)
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

	// Overlay hover popup (centered, scrollable).
	if m.hoverContent != nil {
		maxPopH := max(6, vis-4) // leave at least 2 rows margin top+bottom
		popup := renderHoverPopup(*m.hoverContent, m.width, m.hoverScroll, maxPopH)
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
		cursorScreenRow := screenRowOf(layout, m.cursor.Line, cursorVisCol, cw)
		if cursorScreenRow < 0 {
			cursorScreenRow = m.cursorVisualRowFromTop(cw)
		}
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

	// Overlay fix popup near the decorated word.
	if len(m.fixItems) > 0 && m.fixDecor != nil {
		popup := renderFixPopup(m.fixItems, m.fixIdx, m.width)
		popH := len(popup)
		popW := lipgloss.Width(popup[0])

		gutterW := m.gutterWidth()
		lineStr := m.buf.Line(int(m.fixDecor.Line))
		_, colMap := expandTabsRemap([]rune(lineStr))
		visCol := int(m.fixDecor.Col)
		if int(m.fixDecor.Col) < len(colMap) {
			visCol = colMap[m.fixDecor.Col]
		}
		popCol := gutterW + visCol
		if popCol+popW > m.width {
			popCol = max(0, m.width-popW)
		}

		decorScreenRow := screenRowOf(layout, int(m.fixDecor.Line), visCol, cw)
		if decorScreenRow < 0 {
			decorScreenRow = m.cursorVisualRowFromTop(cw)
		}
		startRow := decorScreenRow + 1
		if startRow+popH > vis {
			startRow = decorScreenRow - popH
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

	// Overlay diagnostic detail popup (Shift+E) above the status bar, full width.
	if m.diagPopup {
		if diags := m.diagsAtPos(m.cursor.Line, m.cursor.Col); len(diags) > 0 {
			popup := renderDiagPopup(diags, m.width)
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

	// Overlay Save As dialog (centered).
	if m.saveAsInput != nil {
		popup := renderSaveAsDialog(*m.saveAsInput, m.width)
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

	// Overlay help popup (centered).
	if m.helpVisible {
		maxPopH := max(6, vis-4)
		popup := renderHelpPopup(m.width, m.helpScroll, maxPopH)
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

	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(m.renderStatusBar())
	return sb.String()
}

// renderSaveAsDialog renders a centered "Save As" input dialog.
func renderSaveAsDialog(input string, maxW int) []string {
	const minInnerW = 40
	innerW := max(minInnerW, maxW/2)
	fieldW := innerW - 2 // 1 space padding on each side

	// Scroll so the cursor is always visible at the right.
	runes := []rune(input)
	if len(runes) >= fieldW {
		runes = runes[len(runes)-(fieldW-1):]
	}
	// fieldContent is exactly innerW runes: space + text + | cursor + trailing spaces + space
	trailing := strings.Repeat(" ", max(0, fieldW-len(runes)-1))
	fieldContent := " " + string(runes) + "|" + trailing + " "

	titleSuffix := bdrH + " Save As "
	titleSuffixW := len([]rune(titleSuffix))
	dashes := strings.Repeat(bdrH, max(0, innerW-titleSuffixW))

	hint := "Enter: Save   Esc: Cancel"
	hintW := len([]rune(hint))
	hintPad := strings.Repeat(" ", max(0, innerW-hintW))

	top := popupBorderStyle.Render(bdrTL + titleSuffix + dashes + bdrTR)
	mid := popupBorderStyle.Render(bdrV) + popupTextStyle.Render(fieldContent) + popupBorderStyle.Render(bdrV)
	bot := popupBorderStyle.Render(bdrV) + popupKeyStyle.Render(hint) + popupBorderStyle.Render(hintPad+bdrV)
	btm := popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
	return []string{top, mid, bot, btm}
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

	// Diagnostic counts for the whole file, prepended before the LSP segment.
	var errCnt, warnCnt, infoCnt int
	for _, d := range m.diagnostics {
		switch d.Severity {
		case 1:
			errCnt++
		case 2:
			warnCnt++
		default:
			infoCnt++
		}
	}
	if infoCnt > 0 {
		right = barDiagInfoStyle.Render(fmt.Sprintf(" %dI ", infoCnt)) + right
	}
	if warnCnt > 0 {
		right = barDiagWarnStyle.Render(fmt.Sprintf(" %dW ", warnCnt)) + right
	}
	if errCnt > 0 {
		right = barDiagErrorStyle.Render(fmt.Sprintf(" %dE ", errCnt)) + right
	}

	// Plugin status bar decorations (e.g. git branch) go on the right, before file type.
	for _, d := range m.decorations {
		if d.Kind == ClientDecorationStatusBar && d.Text != "" {
			right = barStyle.Render(d.Text) + right
		}
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
		centerContent = m.filePath
		if m.buf.Dirty() {
			centerContent += " [+]"
		}
		if len(m.searchMatches) > 0 {
			centerContent += fmt.Sprintf("   [%d/%d]", m.searchIdx+1, len(m.searchMatches))
		}
		if len(m.extraCursors) > 0 {
			centerContent += fmt.Sprintf("   %d cursors", 1+len(m.extraCursors))
		}
	}

	centerW := max(0, m.width-leftW-rightW)
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + right
}
