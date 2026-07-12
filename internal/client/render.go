package client

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

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

// hasLeftGutterDecorations reports whether any plugin decoration uses the leftGutter kind.
func (m Model) hasLeftGutterDecorations() bool {
	for _, d := range m.decorations {
		if d.Kind == ClientDecorationLeftGutter {
			return true
		}
	}
	return false
}

// leftGutterDecorAt returns the leftGutter ClientDecoration for lineNum, or nil.
func (m Model) leftGutterDecorAt(lineNum int) *ClientDecoration {
	for i := range m.decorations {
		if m.decorations[i].Kind == ClientDecorationLeftGutter && int(m.decorations[i].Line) == lineNum {
			return &m.decorations[i]
		}
	}
	return nil
}

// gutterWidth returns the number of columns reserved for line numbers and gutter markers.
//
// Layout (when line numbers are enabled):
//
//	[2-cell left gutter][line-number + space][2-cell right gutter (optional)]
//
// The left gutter is always present when line numbers are enabled, so line
// numbers never shift when bookmarks or marks appear or disappear.
// The right gutter reserves space for plugin decorations (LSP, etc.).
func (m Model) gutterWidth() int {
	const leftW = 2 // bookmark / Vim-mark column; always stable
	hasRightGutter := m.reservePluginGutter || m.hasGutterDecorations()
	if m.cfg == nil || !m.cfg.LineNumbers {
		// No line numbers: fall back to minimal — only show gutters when needed.
		hasLeftContent := m.mark != nil || m.hasLeftGutterDecorations()
		if !hasLeftContent && !hasRightGutter {
			return 0
		}
		w := leftW
		if hasRightGutter {
			w += 2
		}
		return w
	}
	// Line numbers enabled: left gutter is always present.
	w := leftW + len(fmt.Sprint(m.displayLineCount())) + 1
	if hasRightGutter {
		w += 2 // narrower than the old 3-cell right gutter
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
		const leftW = 2
		hasRightGutter := m.reservePluginGutter || m.hasGutterDecorations()
		if chunk == 0 {
			// Left gutter: Vim mark (◆) takes priority over plugin markers; blank otherwise.
			if m.mark != nil && lineNum == m.mark.Line {
				markGutterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00"))
				sb.WriteString(markGutterStyle.Render("◆ "))
			} else if lm := m.leftGutterDecorAt(lineNum); lm != nil {
				color := lm.TextColor
				if color == "" {
					color = "#5588FF"
				}
				lmStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
				sb.WriteString(lmStyle.Render(lm.Text + " "))
			} else {
				sb.WriteString(gutterStyle.Render("  "))
			}

			// Line number (gutterW minus the fixed left and optional right columns).
			numW := gutterW - leftW
			if hasRightGutter {
				numW -= 2
			}
			if numW > 0 {
				numStr := fmt.Sprintf("%*d ", numW-1, lineNum+1)
				if lineNum == m.cursor.Line {
					sb.WriteString(gutterCurStyle.Render(numStr))
				} else {
					sb.WriteString(gutterStyle.Render(numStr))
				}
			}

			// Right gutter: plugin decorations (2 cells — 1 space + 1 marker).
			if hasRightGutter {
				decor := m.gutterDecorAt(lineNum)
				if decor == nil || decor.Text == "" {
					sb.WriteString(gutterStyle.Render("  "))
				} else if decor.TextColor != "" {
					barStyle := lipgloss.NewStyle().Background(lipgloss.Color(decor.TextColor))
					sb.WriteString(" ")
					sb.WriteString(barStyle.Render(" "))
				} else {
					label := decor.Text
					if len([]rune(label)) > 1 {
						label = string([]rune(label)[:1])
					}
					sb.WriteString(" ")
					sb.WriteString(decorOverlayStyle.Render(label))
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
