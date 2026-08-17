package client

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/indiejames/indigo/internal/document"
)

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
	matchLine, matchCol, matchOK := matchingPairPos(m)
	if i < len(layout) {
		return m.renderLineChunk(layout[i], cw, overlays, matchLine, matchCol, matchOK)
	}
	return m.renderLineChunk(layoutEntry{bufLine: m.topLine + i}, cw, overlays, matchLine, matchCol, matchOK)
}

// renderPopupBox builds the styled lines of a popup menu with rounded borders.
func renderPopupBox(title string, items []command, maxW int) []string {
	// Compute needed inner width.
	minInner := len([]rune(title))
	for _, item := range items {
		w := len([]rune(fmt.Sprintf("  %s  %s  ", item.key, item.label)))
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
		keyPart := fmt.Sprintf("  %s", item.key)
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
	// Row layout: lead(1) + kind(kindW) + sep(1) + label + [detailSep + detail] + trail(1).
	// detailSep must match the "  " the row renderer puts before detail, or the
	// row overflows the right border by one column.
	const detailSep = 2
	innerW := 1 + kindW + 1 + labelW + 1
	if hasDetail {
		innerW += detailSep + detailW
	}
	// Cap to screen: total box = innerW+2 must fit in maxW.
	if innerW+2 > maxW {
		innerW = max(20, maxW-2)
		// Trim detail first, then label.
		available := innerW - 1 - kindW - 1 - 1 // subtract lead + kind + sep + trail
		if hasDetail {
			detailW = min(detailW, available/3)
			labelW = available - detailW - detailSep
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
			innerW += detailSep + detailW
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

// buildInlayHintOverlays converts the current viewport's inlay hints into
// per-row overlays. Unlike every other overlay producer in this file, these
// carry w: 0 — a plugin/search/indent-guide overlay's w tells renderLineRunes
// how many real rune positions to skip (the overlay replaces existing
// content), but an inlay hint is virtual text the server infers; it must
// never consume or hide a real character. w: 0 makes renderLineRunes draw the
// hint and then continue rendering the real content at that same column
// unaffected — a pure insertion. Getting this wrong would silently eat real
// characters equal to the hint's width, hiding actual code.
func (m Model) buildInlayHintOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	if m.cfg == nil || !m.cfg.InlayHints || len(m.inlayHints) == 0 {
		return nil
	}
	vis := len(layout)
	rows := make([][]lineOverlay, vis)
	for _, h := range m.inlayHints {
		if h.Label == "" {
			continue
		}
		if h.Line < 0 || h.Line >= m.buf.LineCount() {
			continue // stale hint from before an edit shortened the buffer
		}
		lineRunes := []rune(m.buf.Line(h.Line))
		if h.Col < 0 || h.Col > len(lineRunes) {
			continue // stale hint from before an edit shortened this line
		}
		_, colMap := expandTabsRemap(lineRunes)
		visCol := h.Col
		if h.Col < len(colMap) {
			visCol = colMap[h.Col]
		}
		row := screenRowOf(layout, h.Line, visCol, cw)
		if row < 0 || row >= vis {
			continue
		}
		text := h.Label
		if h.PaddingLeft {
			text = " " + text
		}
		if h.PaddingRight {
			text += " "
		}
		chunkStart := layout[row].chunkStart
		rows[row] = append(rows[row], lineOverlay{
			col:  visCol - chunkStart,
			text: inlayHintStyle.Render(text),
			w:    0,
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

// buildSearchOverlays builds per-row overlays for all search matches. During
// a live search-and-replace preview (m.searchReplacing), each match is
// restyled red and its computed replacement text is injected right after it
// in green — a git-diff-style pair that previews the edit without touching
// the buffer. Overlay column values are chunk-relative. Returns nil when
// there are no matches.
func (m Model) buildSearchOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	if len(m.searchMatches) == 0 {
		return nil
	}
	vis := len(layout)
	rows := make([][]lineOverlay, vis)
	for i, sm := range m.searchMatches {
		lineRunes := []rune(m.buf.Line(sm.line))
		// Search matches are computed once, at search time, and aren't
		// recalculated when the buffer changes afterward (e.g. an LSP
		// refactor edit shrinking or shifting lines out from under a
		// still-active search). A match whose column no longer fits within
		// its (now shorter) line is stale — skip it rather than slicing
		// lineRunes out of bounds below.
		if sm.col > len(lineRunes) {
			continue
		}
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
		current := searchCurrentStyle
		if m.searchReplacing {
			style, current = replaceOldStyle, replaceOldCurrentStyle
		}
		if i == m.searchIdx {
			style = current
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
				m.appendReplacePreview(rows, layout, cw, colMap[matchEnd], sm)
				continue
			}
		}

		rows[row] = append(rows[row], lineOverlay{
			col:  visCol - chunkStart,
			text: style.Render(string(lineRunes[sm.col:matchEnd])),
			w:    sm.length,
		})
		m.appendReplacePreview(rows, layout, cw, colMap[matchEnd], sm)
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

// appendReplacePreview injects sm's replacement text, styled green, as a
// zero-width overlay (see lineOverlay.w) right after its matched span, so it
// renders inline without displacing or overwriting any real character. A
// no-op outside search-and-replace mode, and for an empty replacement (a
// deletion has nothing to preview inserting).
//
// The injection point gets its own screenRowOf lookup rather than reusing
// the match's row: a match sitting right at a soft-wrap boundary can have
// its end land on the next screen row, and reusing the wrong chunkStart
// would place the green text at a nonsense column.
func (m Model) appendReplacePreview(rows [][]lineOverlay, layout []layoutEntry, cw, matchEndVisCol int, sm substituteMatch) {
	if !m.searchReplacing || sm.replacement == "" {
		return
	}
	row := screenRowOf(layout, sm.line, matchEndVisCol, cw)
	if row < 0 || row >= len(rows) {
		return
	}
	chunkStart := layout[row].chunkStart
	rows[row] = append(rows[row], lineOverlay{
		col:  matchEndVisCol - chunkStart,
		text: replaceNewStyle.Render(sm.replacement),
		w:    0,
	})
}

// buildIndentGuideOverlays returns per-row overlays that draw a dim vertical
// bar (│) at each tab-stop in the leading whitespace of lines.
// Blank (or whitespace-only) lines have no indentation of their own, so they
// borrow the deeper of the nearest non-blank line above and below — this
// lets a guide flow through a gap in a block, including a run of blank lines
// right before that block's closing brace.
func (m Model) buildIndentGuideOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	if m.cfg == nil || !m.cfg.IndentGuides {
		return nil
	}
	vis := len(layout)
	rows := make([][]lineOverlay, vis)
	guideText := indentGuideStyle.Render("▏")
	indentWidth := m.effectiveIndentSettings().Width
	if indentWidth <= 0 {
		indentWidth = tabWidth
	}
	for row, entry := range layout {
		bufLine := entry.bufLine
		if bufLine >= m.buf.LineCount() {
			continue
		}
		contentStart := m.lineIndentStop(bufLine)
		if contentStart < 0 {
			contentStart = m.blankLineIndentStop(bufLine)
		}
		if contentStart <= 0 {
			continue
		}

		chunkStart := entry.chunkStart

		// Draw one guide at each indent stop from 0 up to (but not including)
		// contentStart, spaced by the buffer's own indent width so guides
		// land where this file's blocks actually nest (e.g. every 2 columns
		// for a 2-space-indented file, not a hardcoded 4).
		for guideCol := 0; guideCol < contentStart; guideCol += indentWidth {
			if guideCol < chunkStart || guideCol >= chunkStart+cw {
				continue
			}
			rows[row] = append(rows[row], lineOverlay{
				col:   guideCol - chunkStart,
				text:  guideText,
				w:     1,
				plain: "▏",
			})
		}
		// Overlays are added in ascending column order already; no sort needed.
	}
	return rows
}

// applyRulerColumn splices the ruler into every visible row's already fully
// rendered string, at a fixed terminal column — rather than competing for a
// spot in the buffer-column bookkeeping renderLineRunes uses while building
// each row (like indent guides, search highlights, etc. do). A ruler needs
// to behave like most editors' column guides: a straight line pinned to a
// fixed screen column, regardless of what's drawn on that particular row.
// The lineOverlay-based approach that predates this got that wrong for rows
// with inlay hints: an inlay hint is deliberately a w:0 overlay (it renders
// real, visible glyphs but must never displace real content, so it doesn't
// advance that bookkeeping) — so a later overlay computed from buffer
// columns, like the ruler, silently rendered further right on the terminal
// than intended, by exactly the hint's rendered width.
func (m Model) applyRulerColumn(lines []string, layout []layoutEntry, cw int) {
	if m.cfg == nil || m.cfg.RulerColumn <= 0 {
		return
	}
	rulerCol := m.cfg.RulerColumn - 1
	gutterW := m.gutterWidth()

	// Collect every cursor's visual column, keyed by buffer line, so the
	// ruler skips an extra cursor's cell too — not just the primary
	// cursor's — whenever one sits on the ruler column.
	cursorVisCols := make(map[int][]int)
	addCursor := func(pos document.Pos) {
		if pos.Line >= m.buf.LineCount() {
			return
		}
		runes := []rune(m.buf.Line(pos.Line))
		_, colMap := expandTabsRemap(runes)
		if pos.Col < len(colMap) {
			cursorVisCols[pos.Line] = append(cursorVisCols[pos.Line], colMap[pos.Col])
		}
	}
	addCursor(m.cursor)
	for _, ec := range m.extraCursors {
		addCursor(ec.pos)
	}

	for row, entry := range layout {
		if entry.bufLine >= m.buf.LineCount() ||
			rulerCol < entry.chunkStart || rulerCol >= entry.chunkStart+cw {
			continue
		}
		// Never draw over a cursor's own cell — it would otherwise vanish
		// under the ruler's fixed background whenever they coincide.
		skip := false
		for _, c := range cursorVisCols[entry.bufLine] {
			if c == rulerCol {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		lines[row] = overlayRulerColumn(lines[row], gutterW+rulerCol-entry.chunkStart)
	}
}

// overlayRulerColumn tints a single ANSI-aware visual column of an
// already-rendered row, replacing whatever glyph (real character, inlay
// hint, decoration, ...) is actually there — or padding with the ruler's
// blank glyph if the row's real content doesn't reach that far.
func overlayRulerColumn(line string, col int) string {
	before := ansi.Truncate(line, col, "")
	if bw := lipgloss.Width(before); bw < col {
		before += strings.Repeat(" ", col-bw)
	}
	glyph := ansi.Strip(ansi.Cut(line, col, col+1))
	if glyph == "" {
		glyph = " "
	}
	after := ansi.TruncateLeft(line, col+1, "")
	return before + rulerStyle.Render(glyph) + after
}

// lineIndentStop returns the visual column of the first non-space rune on
// bufLine, or -1 if the line is empty or all whitespace.
func (m Model) lineIndentStop(bufLine int) int {
	runes := []rune(m.buf.Line(bufLine))
	expandedRunes, _ := expandTabsRemap(runes)
	for i, r := range expandedRunes {
		if r != ' ' {
			return i
		}
	}
	return -1
}

// blankLineIndentStop computes the effective indent stop for a blank line by
// looking outward to the nearest non-blank line above and below and taking
// the deeper of the two, so guides keep flowing through a gap for as long as
// either side is still inside that block — e.g. a run of blank lines right
// before a closing brace still shows the guide for the block that brace
// closes, matching the indented content above it. Returns 0 (no guides) if
// neither side has one.
func (m Model) blankLineIndentStop(bufLine int) int {
	prev := -1
	for l := bufLine - 1; l >= 0; l-- {
		if s := m.lineIndentStop(l); s >= 0 {
			prev = s
			break
		}
	}
	next := -1
	for l := bufLine + 1; l < m.buf.LineCount(); l++ {
		if s := m.lineIndentStop(l); s >= 0 {
			next = s
			break
		}
	}
	if prev < 0 {
		prev = 0
	}
	if next < 0 {
		next = 0
	}
	return max(prev, next)
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
	{key: "0 / Home", desc: "Line start"},
	{key: "^", desc: "First non-blank character in line"},
	{key: "$ / End", desc: "Line end"},
	{key: "G", desc: "End of file"},
	{key: "Ctrl+f / PgDn", desc: "Page down"},
	{key: "Ctrl+b / PgUp", desc: "Page up"},
	{key: "-", desc: "Jump to previous edit location"},
	{key: "= / +", desc: "Jump to next edit location"},
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
	{key: "Case conversion (~…)"},
	{key: "~s", desc: "snake_case"},
	{key: "~S", desc: "SCREAMING_SNAKE_CASE"},
	{key: "~c", desc: "camelCase"},
	{key: "~p", desc: "PascalCase"},
	{key: "~k", desc: "kebab-case"},
	{key: "~d", desc: "dot.case"},
	{key: ""},
	{key: "Editing"},
	{key: "i", desc: "Insert before cursor"},
	{key: "a", desc: "Insert after cursor"},
	{key: "A", desc: "Insert at line end"},
	{key: "o", desc: "New line below"},
	{key: "O", desc: "New line above"},
	{key: "d", desc: "Delete selection"},
	{key: "c", desc: "Change selection (delete + insert)"},
	{key: "J", desc: "Join line with the line below"},
	{key: "y", desc: "Yank (copy) selection"},
	{key: "u", desc: "Undo"},
	{key: "U", desc: "Redo"},
	{key: ">", desc: "Indent selected line(s)"},
	{key: "<", desc: "Unindent selected line(s)"},
	{key: ""},
	{key: "Selection"},
	{key: "w", desc: "Next word start"},
	{key: "W", desc: "Extend selection to next word start"},
	{key: "x", desc: "Select line"},
	{key: "X", desc: "Extend line backward"},
	{key: "z", desc: "Set mark at cursor (Esc clears)"},
	{key: "Z", desc: "Select from mark to cursor"},
	{key: "ma", desc: "Select around matching object"},
	{key: "mi", desc: "Select inside object / word"},
	{key: "%", desc: "Select all"},
	{key: ";", desc: "Clear selection"},
	{key: "Alt+;", desc: "Flip selection (swap anchor/head)"},
	{key: "shift+end", desc: "Select to end of line"},
	{key: "shift+home", desc: "Select to beginning of line"},
	{key: "shift+right", desc: "Extend selection right one character"},
	{key: "shift+left", desc: "Extend selection left one character"},
	{key: ""},
	{key: "Search"},
	{key: "/", desc: "Start search"},
	{key: "/pat/repl", desc: "Live replace preview; Enter applies"},
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
	{key: "SPC a", desc: "Code actions (fixes & refactors)"},
	{key: ""},
	{key: "Sort (s…)"},
	{key: "a", desc: "Sort selected lines ascending"},
	{key: "d", desc: "Sort selected lines descending"},
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
func helpPopupLines(pluginBindings []ClientPluginBinding) []string {
	lines := make([]string, 0, len(helpEntries)+len(pluginBindings)+4)
	for _, e := range helpEntries {
		if e.desc == "" {
			if e.key == "" {
				lines = append(lines, "")
			} else {
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

	// Append plugin key bindings grouped by plugin name.
	if len(pluginBindings) > 0 {
		// Group by plugin name preserving first-seen order.
		seen := make(map[string]bool)
		type group struct {
			name     string
			bindings []ClientPluginBinding
		}
		var groups []group
		for _, b := range pluginBindings {
			if !seen[b.PluginName] {
				seen[b.PluginName] = true
				groups = append(groups, group{name: b.PluginName})
			}
			for i := range groups {
				if groups[i].name == b.PluginName {
					groups[i].bindings = append(groups[i].bindings, b)
					break
				}
			}
		}
		lines = append(lines, "")
		lines = append(lines, popupKeyStyle.Render("Plugins"))
		for _, g := range groups {
			// Plugin name sub-header (indented).
			lines = append(lines, popupTextStyle.Render("  "+g.name))
			for _, b := range g.bindings {
				key := b.Key
				if len([]rune(key)) > helpKeyW-2 {
					key = string([]rune(key)[:helpKeyW-2])
				}
				padded := fmt.Sprintf("    %-*s  ", helpKeyW-2, key)
				lines = append(lines, popupTextStyle.Render(padded)+popupTextStyle.Render(b.Description))
			}
		}
	}

	return lines
}

// renderHelpPopup renders the help popup as a slice of styled, full-width lines.
// innerW is the content width (excluding borders).
func renderHelpPopup(width, scroll, maxH int, pluginBindings []ClientPluginBinding) []string {
	innerW := min(width-4, 64) // cap at 64 so it doesn't sprawl on huge terminals
	if innerW < 30 {
		innerW = max(30, width-4)
	}

	bodyLines := helpPopupLines(pluginBindings)
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
		bottom = popupBorderStyle.Render(bdrBL + indicator + strings.Repeat(bdrH, remainDashes) + bdrBR)
	} else {
		bottom = popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
	}
	out = append(out, bottom)
	return out
}

// messageLogInnerWidth mirrors the innerW calculation renderMessageLogPopup
// uses, so handleKey can compute the same content width for scroll-clamping
// without duplicating the popup's box-sizing logic.
func messageLogInnerWidth(width int) int {
	innerW := min(width-4, 100) // cap so it doesn't sprawl on huge terminals
	if innerW < 30 {
		innerW = max(30, width-4)
	}
	return innerW
}

// messageLogMaxScroll returns the maximum valid scroll offset for the
// message-log popup at the given terminal size, mirroring the box-sizing
// renderMessageLogPopup and the space-l/scroll handlers in keys.go use.
func messageLogMaxScroll(termW, termH int, log []logEntry) int {
	logLines := messageLogPopupLines(log, messageLogInnerWidth(termW))
	maxPopH := max(6, termH-5) // matches vis-4 in View() where vis = m.height-1
	contentH := maxPopH - 2
	return max(0, len(logLines)-contentH)
}

// messageLogPopupLines formats each logged status message as "HH:MM:SS  text",
// truncated to fit innerW columns, styled red for error ("E:"/"ERR:") entries.
func messageLogPopupLines(log []logEntry, innerW int) []string {
	if len(log) == 0 {
		return []string{popupTextStyle.Render("No messages yet.")}
	}
	lines := make([]string, len(log))
	for i, e := range log {
		ts := e.at.Format("15:04:05") + "  "
		avail := max(0, innerW-len([]rune(ts)))
		textRunes := []rune(e.text)
		if len(textRunes) > avail {
			textRunes = append([]rune(string(textRunes[:max(0, avail-1)])), '…')
		}
		style := popupTextStyle
		if e.isErr {
			style = diagErrorStyle.Background(popupBg)
		}
		lines[i] = popupTextStyle.Render(ts) + style.Render(string(textRunes))
	}
	return lines
}

// renderMessageLogPopup renders the message-log popup (space l) as a slice of
// styled, full-width lines. Mirrors renderHelpPopup's layout/scroll handling.
func renderMessageLogPopup(width, scroll, maxH int, log []logEntry) []string {
	innerW := messageLogInnerWidth(width)

	bodyLines := messageLogPopupLines(log, innerW)
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

	title := "Messages — q to close"
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
		bottom = popupBorderStyle.Render(bdrBL + indicator + strings.Repeat(bdrH, remainDashes) + bdrBR)
	} else {
		bottom = popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
	}
	out = append(out, bottom)
	return out
}
