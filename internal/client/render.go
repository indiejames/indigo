package client

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/indiejames/indigo/internal/highlight"
)

const tabWidth = 4

// diagUnderlineStyle is the underline style used for LSP diagnostics.
// Set once at startup based on terminal capabilities.
// TODO Add a better check for terminal capabilities
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

	// replaceOldStyle/replaceOldCurrentStyle restyle a matched span during a
	// live search-and-replace preview (/pattern/replacement); replaceNewStyle
	// is injected immediately after it to show what it would become — a
	// git-diff-style red/green pair. Nothing here touches the buffer.
	replaceOldStyle        = lipgloss.NewStyle().Background(lipgloss.Color("#5A1E1E")).Foreground(lipgloss.Color("#FFAAAA"))
	replaceOldCurrentStyle = lipgloss.NewStyle().Background(lipgloss.Color("#8A2A2A")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	replaceNewStyle        = lipgloss.NewStyle().Background(lipgloss.Color("#1E5A2A")).Foreground(lipgloss.Color("#AAFFAA"))
)

// inlayHintStyle renders LSP inlay hints (inferred types, parameter names) —
// dim and unobtrusive, since they're virtual text the server infers, not code
// the user wrote.
var inlayHintStyle = lipgloss.NewStyle().Faint(true)

// lineOverlay describes a styled text injection at a visual column in the content area.
// col is an index into the tab-expanded rune slice for the line; w is how many of those
// rune positions the text visually occupies (i.e. positions to skip in the underlying content).
type lineOverlay struct {
	col  int
	text string
	w    int
	// plain is the unstyled glyph to draw instead of text when col falls
	// inside the active selection, so the overlay merges into the selection
	// highlight instead of keeping its own fixed style on top of it. Empty
	// for overlay kinds (search matches, inlay hints, decorations) whose
	// style is meant to stay put regardless of selection.
	plain string
}

// metricsInnerW is the fixed visible width between the box borders.
// Layout per row: 1 space + 9 label + 1 space + 8 value (right-aligned) + 1 space = 20.
const metricsInnerW = 20

func (m *Model) moveCursor(dLine, dCol int) {
	line := max(0, m.cursor.Line+dLine)
	if line >= m.buf.LineCount() {
		line = max(0, m.buf.LineCount()-1)
	}
	maxCol := m.buf.LineLen(line)
	if m.mode == ModeNormal {
		maxCol = m.normalLineEnd(line)
	}

	col := m.cursor.Col + dCol
	if dLine != 0 && dCol == 0 {
		// Vertical movement: stick to the desired ("goal") column across a
		// run of consecutive Up/Down (or PageUp/PageDown) presses, so
		// passing through a shorter line and clamping to its last character
		// doesn't lose the original column — the next long-enough line
		// snaps back to it. goalColActive tells Update()'s tea.KeyMsg
		// dispatcher this was a legitimate vertical move, so it should keep
		// goalCol instead of resetting it for the next keypress.
		//
		// goalCol is tracked in tab-expanded *visual* columns, not rune
		// columns: cursor.Col is a rune index (the right addressing scheme
		// for buffer edits), but a rune index means something different on
		// every line that has a different number of tabs before it, so
		// comparing raw rune columns across lines misaligns the cursor on
		// any line pair whose tab layout differs before the cursor.
		if m.goalCol < 0 {
			curRunes := []rune(m.buf.Line(m.cursor.Line))
			_, curColMap := expandTabsRemap(curRunes)
			m.goalCol = curColMap[min(m.cursor.Col, len(curColMap)-1)]
		}
		targetRunes := []rune(m.buf.Line(line))
		_, targetColMap := expandTabsRemap(targetRunes)
		col = runeColForVisualCol(targetRunes, targetColMap, m.goalCol)
		m.goalColActive = true
	} else if dCol != 0 {
		// Explicit horizontal movement: the new position becomes the goal
		// for any following vertical move, discarding whatever was
		// remembered before.
		m.goalCol = -1
	}
	col = max(0, min(col, maxCol))
	m.cursor.Line = line
	m.cursor.Col = col
	m.scrollToCursor()
}

// normalLineEnd returns the rightmost column the Normal-mode cursor may rest
// on for line. When a following line exists, that is one past the last
// character — i.e. on the line break itself, which can then be navigated
// onto and deleted like any other character (Helix-style). The final line of
// the buffer has no line break to rest on, so it stays clamped to the last
// character.
func (m *Model) normalLineEnd(line int) int {
	lineLen := m.buf.LineLen(line)
	if line < m.buf.LineCount()-1 {
		return lineLen
	}
	return max(0, lineLen-1)
}

// moveCursorChar moves the Normal-mode cursor by one character, treating the
// line break between two lines as a single crossable character: moving right
// from the last character steps onto the line break, then a further right
// step crosses onto the next line; moving left is the mirror image.
func (m *Model) moveCursorChar(delta int) {
	line, col := m.cursor.Line, m.cursor.Col
	switch {
	case delta > 0:
		if end := m.normalLineEnd(line); col < end {
			col++
		} else if line < m.buf.LineCount()-1 {
			line++
			col = 0
		}
	case delta < 0:
		if col > 0 {
			col--
		} else if line > 0 {
			line--
			col = m.normalLineEnd(line)
		}
	}
	m.cursor.Line, m.cursor.Col = line, col
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
	startChunk := m.topChunk
	for len(layout) < vis {
		if bufLine >= m.buf.LineCount() {
			layout = append(layout, layoutEntry{bufLine: bufLine})
			bufLine++
			continue
		}
		runes := []rune(m.buf.Line(bufLine))
		exp, _ := expandTabsRemap(runes)
		chunks := visualChunks(len(exp), cw)
		// For the first line (topLine), start from topChunk instead of 0
		firstChunk := 0
		if bufLine == m.topLine {
			firstChunk = min(startChunk, max(0, chunks-1))
		}
		for chunk := firstChunk; chunk < chunks && len(layout) < vis; chunk++ {
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

// cursorVisualRowFromTop returns how many screen rows below topLine/topChunk
// the cursor currently sits (accounting for soft-wrap).
func (m Model) cursorVisualRowFromTop(cw int) int {
	row := 0
	for l := m.topLine; l < m.cursor.Line && l < m.buf.LineCount(); l++ {
		runes := []rune(m.buf.Line(l))
		exp, _ := expandTabsRemap(runes)
		chunks := visualChunks(len(exp), cw)
		// For topLine, skip topChunk chunks since they're above the viewport
		if l == m.topLine {
			row += max(0, chunks-m.topChunk)
		} else {
			row += chunks
		}
	}
	if m.cursor.Line < m.buf.LineCount() {
		runes := []rune(m.buf.Line(m.cursor.Line))
		_, colMap := expandTabsRemap(runes)
		curVisCol := 0
		if m.cursor.Col < len(colMap) {
			curVisCol = colMap[m.cursor.Col]
		}
		curChunk := 0
		if cw > 0 {
			curChunk = curVisCol / cw
		}
		// If cursor is on topLine, adjust for topChunk
		if m.cursor.Line == m.topLine {
			row += max(0, curChunk-m.topChunk)
		} else {
			row += curChunk
		}
	}
	return row
}

// chunkOfCol returns the 0-based wrap chunk that column col falls in on the
// given buffer line, at content width cw.
func (m Model) chunkOfCol(line, col, cw int) int {
	if line < 0 || line >= m.buf.LineCount() {
		return 0
	}
	runes := []rune(m.buf.Line(line))
	_, colMap := expandTabsRemap(runes)
	curVisCol := 0
	if col < len(colMap) {
		curVisCol = colMap[col]
	}
	if cw <= 0 {
		return 0
	}
	return curVisCol / cw
}

// findTopLineForCursor returns the buffer line and chunk that should be at
// the top of the viewport so the cursor falls within the last visible row.
func (m Model) findTopLineForCursor(cw, vis int) (int, int) {
	cursorChunk := m.chunkOfCol(m.cursor.Line, m.cursor.Col, cw)
	return m.findTopLineForRow(m.cursor.Line, cursorChunk, cw, vis)
}

// findTopLineForRow returns the buffer line and chunk that should be at the
// top of the viewport so that the given wrap chunk of the given buffer line
// falls within the last visible row. findTopLineForCursor is the common case
// (anchored on the cursor's own chunk); scrollToShowLineTail anchors on a
// different chunk of the same line to bring a line's later wrap chunks into
// view instead.
func (m Model) findTopLineForRow(line, chunk, cw, vis int) (int, int) {
	// Number of rows above the target chunk that we can fill.
	rowsAbove := vis - 1 - chunk
	if rowsAbove <= 0 {
		// The target line's chunks alone need all visible rows or more.
		// Start from the chunk that puts the target chunk at the bottom.
		targetChunk := max(0, chunk-(vis-1))
		return line, targetChunk
	}
	l := line - 1
	for l >= 0 {
		runes := []rune(m.buf.Line(l))
		exp, _ := expandTabsRemap(runes)
		chunks := visualChunks(len(exp), cw)
		if chunks == rowsAbove {
			return l, 0
		}
		if chunks > rowsAbove {
			// Line l alone has more wrap chunks than the remaining budget.
			// We can now start from a specific chunk within line l to show
			// exactly rowsAbove chunks above the target, filling the viewport.
			startChunk := chunks - rowsAbove
			return l, startChunk
		}
		rowsAbove -= chunks
		l--
	}
	return max(0, l+1), 0
}

// scrollToShowLineTail scrolls so that as much of the given (possibly
// soft-wrapped) line is visible as fits, anchored on its last visual chunk.
// Used by "go to last line": landing cursor at column 0 of a long wrapped
// final line otherwise leaves its tail permanently hidden below the
// viewport — scrollToCursor only guarantees the cursor's own chunk (the
// first) is visible, and unlike any other line there's no line below the
// last one to move the cursor into that would trigger further scrolling.
func (m *Model) scrollToShowLineTail(line int) {
	if line < 0 || line >= m.buf.LineCount() {
		return
	}
	cw := m.contentWidth()
	vis := m.visibleLines()
	runes := []rune(m.buf.Line(line))
	exp, _ := expandTabsRemap(runes)
	lastChunk := visualChunks(len(exp), cw) - 1
	topLine, topChunk := m.findTopLineForRow(line, lastChunk, cw, vis)
	// Showing the tail must never scroll the cursor's own chunk out of
	// view: if the line has more chunks than fit in the viewport at all,
	// anchoring on the tail and anchoring on the cursor can't both be
	// satisfied, and cursor visibility wins. When the anchor line is the
	// cursor's own line, cap topChunk at the cursor's chunk.
	if topLine == m.cursor.Line {
		if cursorChunk := m.chunkOfCol(m.cursor.Line, m.cursor.Col, cw); topChunk > cursorChunk {
			topChunk = cursorChunk
		}
	}
	if topLine > m.topLine || (topLine == m.topLine && topChunk > m.topChunk) {
		m.topLine = topLine
		m.topChunk = topChunk
	}
}

func (m *Model) scrollToCursor() {
	if m.cursor.Line < m.topLine {
		m.topLine = m.cursor.Line
		m.topChunk = 0
		return
	}
	cw := m.contentWidth()
	vis := m.visibleLines()
	curVisRow := m.cursorVisualRowFromTop(cw)
	if curVisRow < vis {
		return
	}
	topLine, topChunk := m.findTopLineForCursor(cw, vis)
	m.topLine = max(0, topLine)
	m.topChunk = max(0, topChunk)
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
	if selA > selB {
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
			// Wide runes (CJK, emoji, ...) occupy two terminal cells;
			// combining runes (accents stacked onto the previous rune)
			// occupy zero, so they don't advance the visual column at all.
			vcol += runewidth.RuneWidth(r)
		}
	}
	colMap[len(runes)] = vcol
	return
}

// runeColForVisualCol converts a tab-expanded visual column back to the
// rightmost rune column (per runes/colMap, as returned by expandTabsRemap)
// whose visual column doesn't exceed it — i.e. it rounds down to the
// character containing that visual position, the same rule a mouse click
// landing inside a tab's expanded width resolves to (see clickToPos). A
// visual column past the end of the line resolves to the line's length.
func runeColForVisualCol(runes []rune, colMap []int, visualCol int) int {
	col := 0
	for i := 0; i < len(runes); i++ {
		if colMap[i] <= visualCol {
			col = i
		} else {
			break
		}
	}
	if len(colMap) > 0 && visualCol >= colMap[len(runes)] {
		col = len(runes)
	}
	return col
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

	// overlayText picks ovl's rendering: its own fixed style normally, or its
	// plain glyph re-rendered with selectionStyle when ovl's column falls
	// inside the active selection, so it merges into the highlight instead of
	// keeping its own style on top of it (see lineOverlay.plain).
	overlayText := func(ovl lineOverlay) string {
		if ovl.plain != "" && hasSel && ovl.col >= selA && ovl.col <= selB {
			return selectionStyle.Render(ovl.plain)
		}
		return ovl.text
	}

	i := 0
	for i < n {
		// Drain and inject overlays whose column ≤ i (handles skipped positions too).
		// Skip any overlay that lands exactly on the cursor column so the cursor
		// renders on top rather than being hidden by the overlay.
		for oi < len(overlays) && overlays[oi].col <= i {
			if overlays[oi].col == i {
				if hasCursor && i == curCol {
					oi++
					continue
				}
				sb.WriteString(overlayText(overlays[oi]))
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
		i++ // keep i in sync with sb so the trailing overlay loop below doesn't misjudge padding
	} else if n == 0 && hasSel && selA <= selB && oi >= len(overlays) {
		// A blank selected line with no overlays left to draw still needs
		// one highlighted cell so the selection is visible on this row; when
		// overlays remain, the loop below draws the row (and its padding)
		// instead so this placeholder doesn't add a spurious extra column.
		sb.WriteString(selectionStyle.Render(" "))
		i++
	}
	// Write overlays that fall at or past end of content, padding with spaces
	// to reach each one's column. Past the end of real content there are no
	// characters to anchor against, so without this the gap between two such
	// overlays (e.g. two indent guides on a blank line) collapses to zero
	// instead of the columns apart they're meant to be.
	for ; oi < len(overlays); oi++ {
		if overlays[oi].col > i {
			end := overlays[oi].col
			for pos := i; pos < end; {
				switch {
				case hasSel && pos >= selA && pos <= selB:
					segEnd := min(end, selB+1)
					sb.WriteString(selectionStyle.Render(strings.Repeat(" ", segEnd-pos)))
					pos = segEnd
				case hasSel && pos < selA:
					segEnd := min(end, selA)
					sb.WriteString(strings.Repeat(" ", segEnd-pos))
					pos = segEnd
				default:
					sb.WriteString(strings.Repeat(" ", end-pos))
					pos = end
				}
			}
			i = overlays[oi].col
		}
		sb.WriteString(overlayText(overlays[oi]))
		i += overlays[oi].w
	}
}

// renderLineChunk renders one wrap-chunk of a buffer line. overlays must have
// chunk-relative column indices (i.e. already adjusted by chunkStart). Each
// row is padded to m.width so prior terminal content is always overwritten.
func (m Model) renderLineChunk(entry layoutEntry, cw int, overlays []lineOverlay, matchLine, matchCol int, matchOK bool) string {
	lineNum := entry.bufLine
	chunk := entry.chunk
	chunkStart := entry.chunkStart
	bufLineCount := m.buf.LineCount()
	dispLineCount := m.displayLineCount()
	gutterW := m.gutterWidth()
	var sb strings.Builder

	isFlash := lineNum == m.cursor.Line && m.flashTick > 0

	padToWidth := func(s string) string {
		if m.width <= 0 {
			return s
		}
		w := lipgloss.Width(s)
		if w < m.width {
			pad := strings.Repeat(" ", m.width-w)
			if isFlash {
				return s + flashPadStyle.Render(pad)
			}
			return s + pad
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
		selA, selB := m.selectionCols(lineNum, 0)
		switch {
		case lineNum == m.cursor.Line && chunk == 0:
			sb.WriteString(cursorStyle.Render(" "))
		case selA >= 0 && selA <= selB:
			sb.WriteString(selectionStyle.Render(" "))
		default:
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
				switch {
				case isFlash:
					sb.WriteString(flashGutterStyle.Width(numW).Render(numStr))
				case lineNum == m.cursor.Line:
					sb.WriteString(gutterCurStyle.Render(numStr))
				default:
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

	// Remap highlight spans to chunk-relative visual cols. Semantic-token
	// spans, if any, are prepended ahead of tree-sitter's own spans for this
	// line so they win for the identifier-ish positions they cover — safe to
	// simply prepend (no priority-number reconciliation needed) because a
	// position can never simultaneously be, say, a tree-sitter comment/string
	// AND a semantic-token variable/function.
	var remappedSpans []highlight.Span
	spans := m.hlSpans[lineNum]
	if sem := m.semanticSpans[lineNum]; len(sem) > 0 {
		spans = append(append([]highlight.Span(nil), sem...), spans...)
	}
	if len(spans) > 0 {
		for _, s := range spans {
			if s.StartCol >= len(colMap) {
				continue // stale span whose start no longer exists on this line
			}
			newStart := colMap[s.StartCol]
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
	if matchOK && matchLine == lineNum {
		startVis := len(expandedRunes)
		if matchCol < len(colMap) {
			startVis = colMap[matchCol]
		}
		endVis := len(expandedRunes)
		if matchCol+1 < len(colMap) {
			endVis = colMap[matchCol+1]
		}
		if startVis < chunkStart+cw && endVis > chunkStart {
			seq := underlineANSI(ClientUnderlineStraight, activeMatchPair)
			underlines = append(underlines, underlineRange{
				StartCol: max(0, startVis-chunkStart),
				EndCol:   min(cw, endVis-chunkStart),
				StartSeq: seq,
			})
		}
	}
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
