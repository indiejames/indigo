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

	if m.mode == ModeInsert {
		cursorStyle = insertCursorStyle
	} else {
		cursorStyle = normalCursorStyle
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
	if inlayOverlays := m.buildInlayHintOverlays(layout, cw); inlayOverlays != nil {
		for i := range vis {
			if len(inlayOverlays[i]) > 0 {
				rowOverlays[i] = mergeOverlays(rowOverlays[i], inlayOverlays[i])
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
	matchLine, matchCol, matchOK := matchingPairPos(m)
	for i := range vis {
		lines[i] = m.renderLineChunk(layout[i], cw, rowOverlays[i], matchLine, matchCol, matchOK)
	}
	m.applyRulerColumn(lines, layout, cw)

	// Overlay prefix-command popup in the bottom-right corner.
	if len(m.prefixSeq) > 0 {
		if cmd, ok := m.resolveCommand(m.prefixSeq); ok && len(cmd.children) > 0 {
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

	// Overlay message-log popup (space l, centered).
	if m.msgLogVisible {
		maxPopH := max(6, vis-4)
		popup := renderMessageLogPopup(m.width, m.msgLogScroll, maxPopH, m.messageLog)
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

	// Overlay error toast (bottom, above the status bar): non-modal, doesn't
	// intercept keys, auto-dismisses via tickMsg (see pushStatus/toastDuration).
	if m.status != "" && isErrMessage(m.status) {
		popup := renderToast(m.status, m.width)
		popH := len(popup)
		popW := lipgloss.Width(popup[0])
		popCol := max(0, (m.width-popW)/2)
		startRow := max(0, vis-popH)
		for pi, popLine := range popup {
			if row := startRow + pi; row >= 0 && row < vis {
				lines[row] = overlayRight(lines[row], popLine, popCol)
			}
		}
	}

	// Overlay severe-error modal (centered, topmost, drawn last): must be
	// dismissed via Enter/Esc — see handleKey's severeErr gate. maxPopH
	// mirrors the help/message-log popups' floor so the dialog (including
	// its dismiss-hint footer) always fits within the visible area.
	if m.severeErr != "" {
		maxPopH := max(6, vis-4)
		popup := renderSevereErrorPopup(m.severeErr, m.width, maxPopH)
		popH := len(popup)
		popW := lipgloss.Width(popup[0])
		popCol := max(0, (m.width-popW)/2)
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

// effectiveFileTypeName is fileTypeName, but honoring a ":set ft=<key>"
// override (see Model.langOverride) over the name derived from filePath.
func (m Model) effectiveFileTypeName() string {
	if m.langOverride != "" {
		return fileTypeNameForKey(m.langOverride)
	}
	return fileTypeName(m.filePath)
}

// fileTypeName maps a file extension to a human-readable language name.
func fileTypeName(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if name, ok := extDisplayName(ext); ok {
		return name
	}
	if isGitCommitFileName(strings.ToLower(filepath.Base(path))) {
		return "Git Commit"
	}
	if ext != "" {
		return ext
	}
	return "Plain Text"
}

// fileTypeNameForKey is fileTypeName for a registry key typed directly
// (e.g. a ":set ft=<key>" override) rather than derived from a file path.
// The key is tried both as a bare extension (extDisplayName) and, since a
// filename-style language key like "dockerfile" or "commit_editmsg" carries
// no extension to trim, directly against the same names fileTypeName
// matches by base filename. An unrecognized key is shown verbatim (its
// registry entry, if any, still drives real highlighting/indent/comments —
// this only affects the status bar label).
func fileTypeNameForKey(key string) string {
	key = strings.ToLower(strings.TrimPrefix(key, "."))
	if name, ok := extDisplayName(key); ok {
		return name
	}
	if isGitCommitFileName(key) {
		return "Git Commit"
	}
	if key != "" {
		return key
	}
	return "Plain Text"
}

// extDisplayName maps a bare file extension (no leading dot, e.g. "go",
// "rs") to its human-readable language name.
func extDisplayName(ext string) (string, bool) {
	switch ext {
	case "go":
		return "Go", true
	case "rs":
		return "Rust", true
	case "ts":
		return "TypeScript", true
	case "tsx":
		return "TSX", true
	case "js":
		return "JavaScript", true
	case "jsx":
		return "JSX", true
	case "py":
		return "Python", true
	case "c":
		return "C", true
	case "cpp", "cc", "cxx":
		return "C++", true
	case "h":
		return "C Header", true
	case "hpp":
		return "C++ Header", true
	case "java":
		return "Java", true
	case "rb":
		return "Ruby", true
	case "lua":
		return "Lua", true
	case "zig":
		return "Zig", true
	case "md":
		return "Markdown", true
	case "toml":
		return "TOML", true
	case "json":
		return "JSON", true
	case "yaml", "yml":
		return "YAML", true
	case "sh", "bash":
		return "Shell", true
	case "html", "htm":
		return "HTML", true
	case "css":
		return "CSS", true
	case "sql":
		return "SQL", true
	case "proto":
		return "Protobuf", true
	case "capnp":
		return "Cap'n Proto", true
	}
	return "", false
}

// isGitCommitFileName reports whether name (already lowercased) is one of
// git's own commit-message edit files.
func isGitCommitFileName(name string) bool {
	switch name {
	case "commit_editmsg", "merge_msg", "squash_msg", "tag_editmsg":
		return true
	}
	return false
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

// statusBarColumn returns the 1-based column shown in the status bar, per
// Config.CursorColumnStyle: "buffer" is the raw rune offset (m.cursor.Col),
// matching Helix; anything else (including a nil config, e.g. in tests) is
// the tab-expanded visual column, matching VS Code.
func (m Model) statusBarColumn() int {
	if m.cfg != nil && m.cfg.CursorColumnStyle == "buffer" {
		return m.cursor.Col + 1
	}
	runes := []rune(m.buf.Line(m.cursor.Line))
	_, colMap := expandTabsRemap(runes)
	if m.cursor.Col < len(colMap) {
		return colMap[m.cursor.Col] + 1
	}
	return m.cursor.Col + 1
}

// renderWorkspaceDiagSegment renders the fixed-width, project-wide
// diagnostic summary segment (see workspaceDiagSummaryMsg). Colored by the
// highest severity present, blank (all spaces, same width) when the
// workspace has nothing to report or no summary has arrived yet.
func (m Model) renderWorkspaceDiagSegment() string {
	s := m.workspaceDiagSummary
	total := s.ErrorCount + s.WarningCount + s.InfoCount
	if total <= 0 {
		return barStyle.Render("        ")
	}
	style := barDiagInfoStyle
	switch {
	case s.ErrorCount > 0:
		style = barDiagErrorStyle
	case s.WarningCount > 0:
		style = barDiagWarnStyle
	}
	return style.Render(fmt.Sprintf(" WS %3d ", min(total, 999)))
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
	modeSeg := ms.Render("  " + modeLabel + "  ")
	modeW := lipgloss.Width(modeSeg)

	// Right side: [lsp] [file type] [diag counts] [line:col], all fixed
	// width in normal use, so this whole group anchors solidly to the
	// terminal's right edge and nothing to its left ever has to react to it:
	// LSP name is set once on attach, file type never changes for an open
	// file, line:col is a contiguous "line:col" string right-aligned inside
	// a fixed-width field (digits shift within their own reserved space
	// rather than resizing it), and diag counts below use fixed-width slots
	// per severity so an error/warning appearing or clearing never changes
	// this group's total width either.
	posText := fmt.Sprintf("%d:%d", m.cursor.Line+1, m.statusBarColumn())
	posStr := fmt.Sprintf("  %10s  ", truncateCenter(posText, 10))
	right := barStyle.Render(posStr)

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
	diagSlot := func(cnt int, letter string, style lipgloss.Style) string {
		if cnt <= 0 {
			return barStyle.Render("    ")
		}
		return style.Render(fmt.Sprintf("%2d%s ", min(cnt, 99), letter))
	}
	diagSeg := barStyle.Render(" ") +
		diagSlot(errCnt, "E", barDiagErrorStyle) +
		diagSlot(warnCnt, "W", barDiagWarnStyle) +
		diagSlot(infoCnt, "I", barDiagInfoStyle) +
		barStyle.Render(" ")
	right = diagSeg + right

	// Workspace-wide diagnostic summary — a separate segment from the
	// per-buffer E/W/I slots above so "is this file clean" and "is the
	// project clean" stay visually distinct (see PLAN.md's workspace
	// diagnostics status-bar design note). Fixed width like the per-buffer
	// slots, blank (not hidden) when there's nothing to report, so it never
	// changes this group's total width.
	right = m.renderWorkspaceDiagSegment() + right

	ftName := m.effectiveFileTypeName()
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

	// Plugin-contributed segments (git branch, claude indicator, ...) get
	// their own right-justified zone ahead of the lsp/file-type/diag/pos
	// group. Budgeted as a third of whatever's left after mode+right and
	// computed before the file path claims its space, so a chatty plugin can
	// degrade gracefully instead of crowding the path out entirely on a
	// narrow terminal; overflow is ellipsized rather than pushed onto other
	// segments.
	avail := max(0, m.width-modeW-rightW)
	var pluginParts []string
	for _, d := range m.decorations {
		if d.Kind == ClientDecorationStatusBar && d.Text != "" {
			pluginParts = append(pluginParts, d.Text)
		}
	}
	var pluginSeg string
	if len(pluginParts) > 0 {
		joined := truncateCenter(strings.Join(pluginParts, "  "), avail/3)
		pluginSeg = barStyle.Render("  " + joined + "  ")
	}
	pluginW := lipgloss.Width(pluginSeg)

	// Left side: mode, then file path, anchored so neither one moves when
	// the right side or the plugin zone changes width — only the flexible
	// center zone between them absorbs that.
	dp := displayPath(m.workDir, m.filePath)
	dirtyMark := ""
	if m.buf.Dirty() {
		dirtyMark = " [+]"
	}
	pathBudget := max(0, avail-pluginW-1)
	pathSeg := barStyle.Render(" " + truncateCenter(dp+dirtyMark, pathBudget))
	left := modeSeg + pathSeg
	leftW := lipgloss.Width(left)

	var centerContent string
	switch {
	case m.recoveryPrompt:
		centerContent = "Recovery file found!   Use it [y]   Ignore and delete [n]"
	case m.status != "" && !isErrMessage(m.status):
		// Error-class status text renders as the toast overlay instead (see
		// View()) — the center segment truncates long messages and this spot
		// is easy to miss once attention moves elsewhere.
		centerContent = m.status
	default:
		if len(m.searchMatches) > 0 {
			centerContent = fmt.Sprintf("[%d/%d]", m.searchIdx+1, len(m.searchMatches))
		}
		if len(m.extraCursors) > 0 {
			if centerContent != "" {
				centerContent += "   "
			}
			centerContent += fmt.Sprintf("%d cursors", 1+len(m.extraCursors))
		}
	}

	centerW := max(0, m.width-leftW-pluginW-rightW)
	centerContent = truncateCenter(centerContent, centerW)
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + pluginSeg + right
}
