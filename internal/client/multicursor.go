package client

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// selectNextOccurrence implements Ctrl+D (VS Code Cmd+D behaviour).
//
// First call: selects the complete word containing (or adjacent to) the cursor.
//   - Cursor on a word char → whole word via findWholeWordAt (scans ← and →).
//   - Cursor just after a word → whole word to the left.
//   - Otherwise → first word to the right via findWordAt.
//
// Subsequent calls: add a new cursor+selection at the next occurrence of the
// already-selected text, wrapping around to the top of the buffer.
//
// Returns true if any state changed.
func selectNextOccurrence(m *Model) bool {
	if m.sel == nil {
		runes := []rune(m.buf.Line(m.cursor.Line))
		if len(runes) == 0 {
			return false
		}
		col := min(m.cursor.Col, len(runes)-1)
		var start, end int
		var found bool
		switch {
		case isWordChar(runes[col]):
			start, end, found = findWholeWordAt(runes, col)
		case col > 0 && isWordChar(runes[col-1]):
			start, end, found = findWholeWordAt(runes, col-1)
		default:
			start, end, found = findWordAt(runes, col)
		}
		if !found {
			return false
		}
		m.sel = &Selection{
			Anchor: document.Pos{Line: m.cursor.Line, Col: start},
			Head:   document.Pos{Line: m.cursor.Line, Col: end},
		}
		m.cursor = m.sel.Head
		m.scrollToCursor()
		return true
	}

	text := m.selectedText()
	if text == "" {
		return false
	}
	textRunes := []rune(text)
	n := len(textRunes)

	// Search from just after the end of the last extra cursor's selection,
	// or after the primary selection if no extra cursors yet.
	var searchLine, searchCol int
	if len(m.extraCursors) > 0 {
		last := m.extraCursors[len(m.extraCursors)-1]
		if last.sel != nil {
			_, end := last.sel.ordered()
			searchLine = end.Line
			searchCol = end.Col + 1
		} else {
			searchLine = last.pos.Line
			searchCol = last.pos.Col + 1
		}
	} else {
		_, end := m.sel.ordered()
		searchLine = end.Line
		searchCol = end.Col + 1
	}

	found, fLine, fCol := findOccurrence(m, textRunes, searchLine, searchCol)
	if !found {
		// Wrap around from beginning.
		found, fLine, fCol = findOccurrence(m, textRunes, 0, 0)
	}
	if !found {
		return false
	}
	if isAlreadySelected(m, fLine, fCol) {
		// Found an occurrence that already has a cursor on it — either the
		// wrap-around search cycled all the way back to one, or (once a
		// wrap has added a cursor earlier in the buffer than a
		// previously-added one) a plain forward search re-found it. Either
		// way every occurrence is already selected, so don't add a
		// duplicate ExtraCursor at the same position (a following
		// multi-cursor insert would then double-insert there).
		return false
	}

	newSel := &Selection{
		Anchor: document.Pos{Line: fLine, Col: fCol},
		Head:   document.Pos{Line: fLine, Col: fCol + n - 1},
	}
	m.extraCursors = append(m.extraCursors, ExtraCursor{
		pos:     newSel.Head,
		sel:     newSel,
		goalCol: -1,
	})
	return true
}

// isAlreadySelected reports whether (line, col) is the start of the primary
// selection or any extra cursor's selection.
func isAlreadySelected(m *Model, line, col int) bool {
	if selectionStartsAt(m.sel, line, col) {
		return true
	}
	for _, ec := range m.extraCursors {
		if selectionStartsAt(ec.sel, line, col) {
			return true
		}
	}
	return false
}

func selectionStartsAt(sel *Selection, line, col int) bool {
	if sel == nil {
		return false
	}
	start, _ := sel.ordered()
	return start.Line == line && start.Col == col
}

// findOccurrence searches for textRunes in the buffer starting at (fromLine, fromCol).
func findOccurrence(m *Model, textRunes []rune, fromLine, fromCol int) (bool, int, int) {
	n := len(textRunes)
	for line := fromLine; line < m.buf.LineCount(); line++ {
		lineRunes := []rune(m.buf.Line(line))
		startCol := 0
		if line == fromLine {
			startCol = fromCol
		}
		for col := startCol; col+n <= len(lineRunes); col++ {
			match := true
			for i := range textRunes {
				if lineRunes[col+i] != textRunes[i] {
					match = false
					break
				}
			}
			if match {
				return true, line, col
			}
		}
	}
	return false, 0, 0
}

// addCursorBelow adds an extra cursor on the line below the last cursor (primary
// or last extra), at the same column. Used by the "C" key in Normal mode.
func addCursorBelow(m *Model) {
	refLine := m.cursor.Line
	refCol := m.cursor.Col
	if len(m.extraCursors) > 0 {
		last := m.extraCursors[len(m.extraCursors)-1]
		refLine = last.pos.Line
		refCol = last.pos.Col
	}
	nextLine := refLine + 1
	if nextLine >= m.buf.LineCount() {
		return
	}
	lineLen := m.buf.LineLen(nextLine)
	newCol := refCol
	if lineLen > 0 && newCol >= lineLen {
		newCol = lineLen - 1
	} else if lineLen == 0 {
		newCol = 0
	}
	m.extraCursors = append(m.extraCursors, ExtraCursor{
		pos:     document.Pos{Line: nextLine, Col: newCol},
		goalCol: -1,
	})
}

// splitSelectionIntoCursors splits a multi-line primary selection into one cursor
// per line. Each line gets its own selection spanning the full line content
// (start of line for intermediate lines; up to end.Col for the last line).
// Used by Alt+s in Normal mode.
func splitSelectionIntoCursors(m *Model) {
	if m.sel == nil {
		return
	}
	start, end := m.sel.ordered()
	if start.Line == end.Line {
		return // single-line: nothing to split
	}

	m.extraCursors = nil

	// Primary cursor covers the start line.
	startLineLen := m.buf.LineLen(start.Line)
	startLineEnd := max(0, startLineLen-1)
	m.sel = &Selection{
		Anchor: document.Pos{Line: start.Line, Col: start.Col},
		Head:   document.Pos{Line: start.Line, Col: startLineEnd},
	}
	m.cursor = m.sel.Head

	// Extra cursors for lines start.Line+1 through end.Line.
	for l := start.Line + 1; l <= end.Line; l++ {
		lineLen := m.buf.LineLen(l)
		var endCol int
		if l == end.Line {
			endCol = end.Col
		} else {
			endCol = max(0, lineLen-1)
		}
		if lineLen == 0 {
			m.extraCursors = append(m.extraCursors, ExtraCursor{
				pos:     document.Pos{Line: l, Col: 0},
				goalCol: -1,
			})
		} else {
			m.extraCursors = append(m.extraCursors, ExtraCursor{
				pos:     document.Pos{Line: l, Col: endCol},
				goalCol: -1,
				sel: &Selection{
					Anchor: document.Pos{Line: l, Col: 0},
					Head:   document.Pos{Line: l, Col: endCol},
				},
			})
		}
	}
}

// deleteAllCursorSelections deletes the selection at every cursor (primary and
// extra), processed back-to-front so earlier deletions don't shift later cursor
// positions. When currentGroup is nil (normal-mode delete), a transient group
// is opened so all deletions land in one undo entry. When currentGroup is
// already set (inside an insert session, e.g. `c`), inverses accumulate there.
func deleteAllCursorSelections(m Model) (Model, tea.Cmd) {
	type entry struct {
		cursor    document.Pos
		sel       *Selection
		isPrimary bool
	}

	entries := []entry{{m.cursor, m.sel, true}}
	for _, ec := range m.extraCursors {
		entries = append(entries, entry{ec.pos, ec.sel, false})
	}

	sort.Slice(entries, func(i, j int) bool {
		ci, cj := entries[i].cursor, entries[j].cursor
		if ci.Line != cj.Line {
			return ci.Line > cj.Line
		}
		return ci.Col > cj.Col
	})

	snapBefore := m.cursorSnap() // snapshot pre-delete cursor state for undo
	openedGroup := m.currentGroup == nil
	if openedGroup {
		m.currentGroup = []document.Op{}
	}

	var cmds []tea.Cmd
	type result struct {
		cursor    document.Pos
		isPrimary bool
	}
	results := make([]result, len(entries))

	for i, e := range entries {
		m.cursor = e.cursor
		m.sel = e.sel
		var cmd tea.Cmd
		m, cmd = m.deleteSelection()
		cmds = append(cmds, cmd)
		results[i] = result{m.cursor, e.isPrimary}
	}

	if openedGroup && len(m.currentGroup) > 0 {
		m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: snapBefore})
		m.currentGroup = nil
	}

	m.extraCursors = nil
	for _, r := range results {
		if r.isPrimary {
			m.cursor = r.cursor
		} else {
			m.extraCursors = append(m.extraCursors, ExtraCursor{pos: r.cursor, goalCol: -1})
		}
	}
	m.sel = nil
	m.scrollToCursor()

	return m, tea.Batch(cmds...)
}

// applyInsertToAllCursors inserts the same fixed text at all cursor
// positions, processed front-to-back so each insert's column delta is
// carried forward for subsequent cursors on the same line.
func applyInsertToAllCursors(m Model, text string) (Model, tea.Cmd) {
	return applyInsertTextToAllCursors(m, func(int, int) string { return text })
}

// applyInsertTextToAllCursors is applyInsertToAllCursors generalized to
// compute the inserted text per cursor via textFor(line, col) — needed for
// tab-stop-aware Tab, where the number of spaces depends on each cursor's
// own visual column. textFor is called with each cursor's line/col in the
// buffer's current state at the point that cursor's insert is about to
// apply (i.e. already reflecting any earlier cursors' inserts on that line),
// so it sees the same content that cursor's own edit will land next to.
func applyInsertTextToAllCursors(m Model, textFor func(line, col int) string) (Model, tea.Cmd) {
	if len(m.extraCursors) == 0 {
		text := textFor(m.cursor.Line, m.cursor.Col)
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: text,
		}
		m.cursor.Col += len([]rune(text))
		return applyOp(m, op)
	}

	snapBefore := m.cursorSnap()
	type entry struct {
		origLine, origCol int
		isPrimary         bool
		extraIdx          int
	}

	entries := []entry{{m.cursor.Line, m.cursor.Col, true, -1}}
	for i, ec := range m.extraCursors {
		entries = append(entries, entry{ec.pos.Line, ec.pos.Col, false, i})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].origLine != entries[j].origLine {
			return entries[i].origLine < entries[j].origLine
		}
		return entries[i].origCol < entries[j].origCol
	})

	var cmds []tea.Cmd
	colAdj := map[int]int{} // origLine → cumulative column delta
	lineAdj := 0

	type newPos struct{ line, col int }
	newPositions := make([]newPos, len(entries))

	for i, e := range entries {
		adjLine := e.origLine + lineAdj
		adjCol := e.origCol + colAdj[e.origLine]

		text := textFor(adjLine, adjCol)
		textRunes := []rune(text)
		isNewline := text == "\n"

		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: adjLine,
			InsertCol:  adjCol,
			InsertText: text,
		}
		al, d := opLineDelta(op)
		// Shift immediately, one op at a time, in the same adjusted
		// coordinate space each op is applied in — a single combined
		// shift (min atLine, summed delta) would over-shift an overlay
		// line sitting between two edit points on different lines.
		m = m.shiftLSPOverlayLines(al, d)
		inv := inverseOp(m, op)
		if m.currentGroup != nil {
			m.currentGroup = append(m.currentGroup, inv)
		} else {
			m.undoStack = append(m.undoStack, undoEntry{ops: []document.Op{inv}, before: snapBefore})
		}
		m.redoStack = nil
		m.buf.Apply(op)
		cmds = append(cmds, m.sendToServer(op))

		if isNewline {
			newPositions[i] = newPos{adjLine + 1, 0}
			lineAdj++
			// Clear column adjustment for this line since it was split.
			delete(colAdj, e.origLine)
		} else {
			newPositions[i] = newPos{adjLine, adjCol + len(textRunes)}
			colAdj[e.origLine] += len(textRunes)
		}
	}

	m.sel = nil
	for i, e := range entries {
		np := newPositions[i]
		if e.isPrimary {
			m.cursor = document.Pos{Line: np.line, Col: np.col}
		} else {
			m.extraCursors[e.extraIdx].pos = document.Pos{Line: np.line, Col: np.col}
			m.extraCursors[e.extraIdx].sel = nil
		}
	}
	m.scrollToCursor()

	var refreshCmd tea.Cmd
	m, refreshCmd = m.scheduleLSPOverlayRefresh()
	cmds = append(cmds, refreshCmd)

	return m, tea.Batch(append(cmds, m.reparseHighlight())...)
}

// applyBackspaceToAllCursors deletes the character before each cursor,
// processed back-to-front so earlier deletes don't shift later cursor positions.
func applyBackspaceToAllCursors(m Model) (Model, tea.Cmd) {
	snapBefore := m.cursorSnap()
	type entry struct {
		line, col int
		isPrimary bool
		extraIdx  int
	}

	entries := []entry{{m.cursor.Line, m.cursor.Col, true, -1}}
	for i, ec := range m.extraCursors {
		entries = append(entries, entry{ec.pos.Line, ec.pos.Col, false, i})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].line != entries[j].line {
			return entries[i].line > entries[j].line
		}
		return entries[i].col > entries[j].col
	})

	var cmds []tea.Cmd
	type newPos struct{ line, col int }
	newPositions := make([]newPos, len(entries))

	for i, e := range entries {
		if e.line == 0 && e.col == 0 {
			newPositions[i] = newPos{0, 0}
			continue
		}
		var fromLine, fromCol, toLine, toCol int
		if e.col > 0 {
			fromLine, fromCol = e.line, e.col-1
			toLine, toCol = e.line, e.col
		} else {
			prevLen := m.buf.LineLen(e.line - 1)
			fromLine, fromCol = e.line-1, prevLen
			toLine, toCol = e.line, 0
		}
		op := document.Op{
			ClientID: m.rpc.ClientID(),
			Type:     document.OpDelete,
			FromLine: fromLine, FromCol: fromCol,
			ToLine: toLine, ToCol: toCol,
		}
		al, d := opLineDelta(op)
		// Shift immediately, one op at a time (back-to-front, matching
		// application order) — see the identical comment in
		// applyInsertToAllCursors for why a single combined shift is wrong.
		m = m.shiftLSPOverlayLines(al, d)
		inv := inverseOp(m, op)
		if m.currentGroup != nil {
			m.currentGroup = append(m.currentGroup, inv)
		} else {
			m.undoStack = append(m.undoStack, undoEntry{ops: []document.Op{inv}, before: snapBefore})
		}
		m.redoStack = nil
		m.buf.Apply(op)
		cmds = append(cmds, m.sendToServer(op))
		newPositions[i] = newPos{fromLine, fromCol}
	}

	for i, e := range entries {
		np := newPositions[i]
		if e.isPrimary {
			m.cursor = document.Pos{Line: np.line, Col: np.col}
		} else {
			m.extraCursors[e.extraIdx].pos = document.Pos{Line: np.line, Col: np.col}
		}
	}
	m.scrollToCursor()

	var refreshCmd tea.Cmd
	m, refreshCmd = m.scheduleLSPOverlayRefresh()
	cmds = append(cmds, refreshCmd)

	return m, tea.Batch(append(cmds, m.reparseHighlight())...)
}

// applyToAllCursors calls fn on the primary cursor then on each extra cursor in
// turn, swapping cursor/sel state before and after each call. The viewport
// (topLine, topChunk) follows only the primary cursor; extra cursors do not scroll.
func (m *Model) applyToAllCursors(fn func(*Model)) {
	fn(m) // primary cursor
	saved := append([]ExtraCursor(nil), m.extraCursors...)
	for i, ec := range saved {
		savedCursor := m.cursor
		savedSel := m.sel
		savedTopLine := m.topLine
		savedTopChunk := m.topChunk
		savedGoalCol := m.goalCol
		m.cursor = ec.pos
		m.sel = ec.sel
		m.goalCol = ec.goalCol // each extra cursor tracks its own sticky column
		m.extraCursors = nil   // prevent fn from seeing/modifying extra cursors
		fn(m)
		saved[i].pos = m.cursor
		saved[i].sel = m.sel
		saved[i].goalCol = m.goalCol
		m.cursor = savedCursor
		m.sel = savedSel
		m.topLine = savedTopLine
		m.topChunk = savedTopChunk
		m.goalCol = savedGoalCol
	}
	m.extraCursors = saved
}

// buildExtraCursorOverlays returns per-screen-row overlays for extra cursors
// and their selections. Returns nil when there are no extra cursors.
//
// When an extra cursor has a selection, the selection text is rendered with the
// cursor character embedded at the head position (mirroring how renderLineRunes
// handles the primary cursor). A single overlay covers the whole range, so the
// cursor is never overwritten by a subsequent selection overlay.
func (m Model) buildExtraCursorOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	if len(m.extraCursors) == 0 {
		return nil
	}
	vis := len(layout)
	rows := make([][]lineOverlay, vis)

	for _, ec := range m.extraCursors {
		if ec.pos.Line >= m.buf.LineCount() {
			continue
		}
		lineRunes := []rune(m.buf.Line(ec.pos.Line))
		expandedRunes, colMap := expandTabsRemap(lineRunes)

		// Cursor visual column (may be past end = space cursor).
		curVisCol := len(expandedRunes)
		if ec.pos.Col < len(colMap) {
			curVisCol = colMap[ec.pos.Col]
		}

		// If the extra cursor has a same-line selection, render the selection text
		// with the cursor character highlighted at the head position in one overlay.
		if ec.sel != nil && ec.sel.Anchor.Line == ec.sel.Head.Line {
			start, end := ec.sel.ordered()
			selVisStart := 0
			if start.Col < len(colMap) {
				selVisStart = colMap[start.Col]
			}
			selVisEnd := len(expandedRunes)
			if end.Col+1 <= len(lineRunes) {
				selVisEnd = colMap[end.Col+1]
			}
			if selVisStart < selVisEnd {
				row := screenRowOf(layout, start.Line, selVisStart, cw)
				if row >= 0 && row < vis {
					chunkStart := layout[row].chunkStart
					clipStart := max(selVisStart, chunkStart)
					clipEnd := min(selVisEnd, chunkStart+cw, len(expandedRunes))
					if clipStart < clipEnd {
						var sb strings.Builder
						for c := clipStart; c < clipEnd; c++ {
							ch := string(expandedRunes[c : c+1])
							if c == curVisCol {
								sb.WriteString(cursorStyle.Render(ch))
							} else {
								sb.WriteString(selectionStyle.Render(ch))
							}
						}
						rows[row] = append(rows[row], lineOverlay{
							col:  clipStart - chunkStart,
							text: sb.String(),
							w:    clipEnd - clipStart,
						})
						continue // cursor embedded in selection; no separate cursor overlay
					}
				}
			}
		}

		// Cursor-only overlay (no selection, or selection didn't produce an overlay).
		refVisCol := curVisCol
		if len(expandedRunes) > 0 {
			refVisCol = min(curVisCol, len(expandedRunes)-1)
		}
		row := screenRowOf(layout, ec.pos.Line, refVisCol, cw)
		if row < 0 || row >= vis {
			continue
		}
		chunkStart := layout[row].chunkStart
		chunkCol := curVisCol - chunkStart
		if chunkCol < 0 || chunkCol >= cw {
			continue
		}
		var cursorText string
		if curVisCol < len(expandedRunes) {
			cursorText = cursorStyle.Render(string(expandedRunes[curVisCol : curVisCol+1]))
		} else {
			cursorText = cursorStyle.Render(" ")
		}
		rows[row] = append(rows[row], lineOverlay{
			col:  chunkCol,
			text: cursorText,
			w:    1,
		})
	}

	// Sort overlays per row.
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
