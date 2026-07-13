package client

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// displayPath returns a project-relative path for status bar display.
// Falls back to the base name if Rel fails (e.g. file outside the project).
func displayPath(workDir, filePath string) string {
	if workDir != "" && filePath != "" {
		if rel, err := filepath.Rel(workDir, filePath); err == nil {
			return rel
		}
	}
	return filepath.Base(filePath)
}

// truncateCenter clamps s to width runes, adding "…" if cut.
func truncateCenter(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

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
	if guideOverlays := m.buildIndentGuideOverlays(layout, cw); guideOverlays != nil {
		for i := range vis {
			if len(guideOverlays[i]) > 0 {
				rowOverlays[i] = mergeOverlays(guideOverlays[i], rowOverlays[i])
			}
		}
	}
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

	// Overlay fix/action popup near the decorated word (or cursor for action-only items).
	if len(m.fixItems) > 0 {
		popup := renderFixPopup(m.fixItems, m.fixIdx, m.width)
		popH := len(popup)
		popW := lipgloss.Width(popup[0])

		gutterW := m.gutterWidth()
		var anchorLine int
		var anchorCol int
		if m.fixDecor != nil {
			lineStr := m.buf.Line(int(m.fixDecor.Line))
			_, colMap := expandTabsRemap([]rune(lineStr))
			visCol := int(m.fixDecor.Col)
			if int(m.fixDecor.Col) < len(colMap) {
				visCol = colMap[m.fixDecor.Col]
			}
			anchorLine = int(m.fixDecor.Line)
			anchorCol = visCol
		} else {
			anchorLine = m.cursor.Line
			anchorCol = m.cursor.Col
		}
		popCol := gutterW + anchorCol
		if popCol+popW > m.width {
			popCol = max(0, m.width-popW)
		}

		decorScreenRow := screenRowOf(layout, anchorLine, anchorCol, cw)
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
		popup := renderHelpPopup(m.width, m.helpScroll, maxPopH, m.pluginBindings)
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

	dp := displayPath(m.workDir, m.filePath)
	dirtyMark := ""
	if m.buf.Dirty() {
		dirtyMark = " [+]"
	}

	var centerContent string
	switch {
	case m.recoveryPrompt:
		centerContent = "Recovery file found!   Use it [y]   Ignore and delete [n]"
case m.status != "":
		centerContent = dp + dirtyMark + "   " + m.status
	default:
		centerContent = dp + dirtyMark
		if len(m.searchMatches) > 0 {
			centerContent += fmt.Sprintf("   [%d/%d]", m.searchIdx+1, len(m.searchMatches))
		}
		if len(m.extraCursors) > 0 {
			centerContent += fmt.Sprintf("   %d cursors", 1+len(m.extraCursors))
		}
	}

	centerW := max(0, m.width-leftW-rightW)
	centerContent = truncateCenter(centerContent, centerW)
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + right
}
