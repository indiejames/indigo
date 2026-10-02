package client

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// searchMatchAtCursor returns the currently active / search match when the
// cursor sits exactly on its start — true right after committing a search
// (Enter) or cycling with n/N, before the cursor has moved elsewhere.
// selectNextOccurrence uses this to seed Ctrl+D from the actual search
// match instead of the generic word-boundary heuristic, so a punctuation-
// or regex-shaped match (not just a whole word) seeds the multi-cursor flow
// correctly too. A zero-length match can't seed a text-based multi-select,
// so that's excluded here same as selectAllSearchMatches excludes it there.
func searchMatchAtCursor(m *Model) (substituteMatch, bool) {
	if m.searchIdx < 0 || m.searchIdx >= len(m.searchMatches) {
		return substituteMatch{}, false
	}
	sm := m.searchMatches[m.searchIdx]
	if sm.line != m.cursor.Line || sm.col != m.cursor.Col || sm.length <= 0 {
		return substituteMatch{}, false
	}
	return sm, true
}

// selectNextOccurrence implements Ctrl+D (VS Code Cmd+D behaviour).
//
// First call: selects the complete word containing (or adjacent to) the
// cursor — unless the cursor is sitting on the current / search match (see
// searchMatchAtCursor), in which case that match is selected instead, and
// the search overlay/state is cleared the same way Alt+Enter clears it
// (see selectAllSearchMatches) since Ctrl+D takes over as the driver of
// where the cursor goes next.
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
		if sm, ok := searchMatchAtCursor(m); ok {
			m.sel = &Selection{
				Anchor: document.Pos{Line: sm.line, Col: sm.col},
				Head:   document.Pos{Line: sm.line, Col: sm.col + sm.length - 1},
			}
			m.cursor = m.sel.Head
			*m = m.withClearedSearch()
			m.scrollToCursor()
			return true
		}
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

// selectAllSearchMatches converts every match from the active / search
// (m.searchMatches, already computed by updateSearch) into a cursor: the
// first match becomes the primary selection, the rest become extra
// cursors — the same one-cursor-per-occurrence shape Ctrl+D
// (selectNextOccurrence) builds up manually one occurrence at a time, but
// all at once. A zero-length match (a regex like \b with no width) gets a
// bare cursor with no selection, same as a bare ExtraCursor elsewhere.
// Returns false (no state changed) if there are no matches.
func selectAllSearchMatches(m *Model) bool {
	if len(m.searchMatches) == 0 {
		return false
	}

	toCursor := func(mt substituteMatch) (document.Pos, *Selection) {
		if mt.length <= 0 {
			return document.Pos{Line: mt.line, Col: mt.col}, nil
		}
		sel := &Selection{
			Anchor: document.Pos{Line: mt.line, Col: mt.col},
			Head:   document.Pos{Line: mt.line, Col: mt.col + mt.length - 1},
		}
		return sel.Head, sel
	}

	pos, sel := toCursor(m.searchMatches[0])
	m.cursor = pos
	m.sel = sel

	m.extraCursors = nil
	for _, mt := range m.searchMatches[1:] {
		p, s := toCursor(mt)
		m.extraCursors = append(m.extraCursors, ExtraCursor{pos: p, sel: s, goalCol: -1})
	}

	m.scrollToCursor()
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

// cursorTextsInDocumentOrder returns what each cursor (primary and extra)
// currently covers, ordered by position in the buffer rather than by the order
// the cursors were created — a Ctrl+D that wrapped, or a cursor added above,
// otherwise puts the clipboard in an order that looks arbitrary to the user.
//
// Each cursor contributes its selected text. bareCursors decides what a cursor
// with *no* selection contributes, and the two callers deliberately differ —
// each matching its own single-cursor behaviour, so one cursor is the N=1 case
// of the same rule:
//
//   - yank passes true: `y` with no selection copies the character under the
//     cursor (TestExecuteYankNoSelectionCopiesCharUnderCursor).
//   - cut passes false: `v` with no selection deletes the character under the
//     cursor without touching the clipboard, so that a run of bare cuts does
//     not flood it (TestDeleteAllCursorSelectionsNoSelectionDoesNotTouchClipboard
//     and its single-cursor sibling).
//
// Do not "unify" those into one rule — both are tested on purpose, and an
// earlier version of this helper lost the cut half by assuming they matched.
func cursorTextsInDocumentOrder(m Model, bareCursors bool) []string {
	type at struct {
		cursor document.Pos
		sel    *Selection
	}
	all := []at{{m.cursor, m.sel}}
	for _, ec := range m.extraCursors {
		all = append(all, at{ec.pos, ec.sel})
	}
	sort.Slice(all, func(i, j int) bool {
		ci, cj := all[i].cursor, all[j].cursor
		if ci.Line != cj.Line {
			return ci.Line < cj.Line
		}
		return ci.Col < cj.Col
	})

	var parts []string
	for _, a := range all {
		cm := m
		cm.cursor = a.cursor
		cm.sel = a.sel
		var text string
		switch {
		case a.sel != nil:
			text = cm.selectedText()
		case bareCursors:
			text = cm.charUnderCursor()
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return parts
}

// yankAllCursorSelections copies every cursor's text to the clipboard as one
// newline-joined string, the same shape the multi-cursor cut produces.
//
// Written because yank had no multi-cursor path at all: with several cursors
// open it copied the primary cursor's selection and silently dropped the rest
// — the very "all but one cursor's text is lost" failure the clipboard comment
// in deleteAllCursorSelections below exists to prevent on the cut path.
//
// One clipboard write, for the same reason cut does it once: the OS clipboard
// holds a single string, so a write per cursor would leave only whichever
// landed last.
func yankAllCursorSelections(m Model) (Model, tea.Cmd) {
	parts := cursorTextsInDocumentOrder(m, true)
	if len(parts) == 0 {
		return m, nil
	}
	if err := clipboardWriter(strings.Join(parts, "\n")); err != nil {
		return m.pushStatus("clipboard: " + err.Error()), nil
	}
	m.multiYank = parts // so a later paste can distribute them one per cursor
	m = m.pushStatus(fmt.Sprintf("copied %d selections", len(parts)))
	// Cleared at every cursor, matching what single-cursor yank does with its
	// one selection.
	m.sel = nil
	for i := range m.extraCursors {
		m.extraCursors[i].sel = nil
	}
	return m, nil
}

// distributableYank returns the per-cursor pieces to paste when the clipboard
// still holds exactly what the last multi-cursor yank or cut wrote and there
// is one piece per cursor, or nil when it does not.
//
// Both conditions are VS Code's: it distributes a multi-cursor copy one
// selection per cursor only when the counts match, and pastes the whole
// clipboard at every cursor otherwise — including when the text came from
// another application, however many lines it has. Splitting on newlines
// instead would mean three lines copied from a browser landing one-per-cursor,
// which is not what either editor promises.
func (m Model) distributableYank(clipboard string) []string {
	if len(m.multiYank) != len(m.extraCursors)+1 {
		return nil
	}
	if strings.Join(m.multiYank, "\n") != clipboard {
		return nil // the clipboard moved on; treat it as ordinary text
	}
	return m.multiYank
}

// pasteAtAllCursors pastes the clipboard at every cursor: one piece per cursor
// when the clipboard came from a matching multi-cursor yank or cut, otherwise
// the whole text at each.
//
// Like single-cursor paste, this inserts at the cursor rather than replacing a
// selection — VS Code replaces, indigo does not, and that difference is
// deliberately left alone here so `p` means one thing whatever the cursor
// count.
func pasteAtAllCursors(m Model, text string) (Model, tea.Cmd) {
	// Before the count check below, not after: cursors can coincide — a
	// remote delete spanning two of them collapses both onto the deletion's
	// start — and they are then one caret on screen. Deduping afterwards
	// would leave distributableYank comparing N pieces against N cursors,
	// agree to distribute, and then hand the pieces to M < N cursors with
	// the tail silently dropped.
	m.dedupeCursors(dedupeByPosition)
	if parts := m.distributableYank(text); parts != nil {
		return applyInsertTextToAllCursors(m, func(idx, _, _ int) string { return parts[idx] })
	}
	return applyInsertToAllCursors(m, text)
}

// deleteAllCursorSelections deletes the selection at every cursor (primary and
// extra), processed back-to-front so earlier deletions don't shift later cursor
// positions. When currentGroup is nil (normal-mode delete), a transient group
// is opened so all deletions land in one undo entry. When currentGroup is
// already set (inside an insert session, e.g. `c`), inverses accumulate there.
// copyToClipboard selects cut vs. plain-delete semantics for all cursors at
// once — d/c pass false, the explicit cut command passes true.
func deleteAllCursorSelections(m Model, copyToClipboard bool) (Model, tea.Cmd) {
	type entry struct {
		cursor    document.Pos
		sel       *Selection
		isPrimary bool
	}

	entries := []entry{{m.cursor, m.sel, true}}
	for _, ec := range m.extraCursors {
		entries = append(entries, entry{ec.pos, ec.sel, false})
	}

	// For the cut path: collect every cursor's cut text in document order and
	// write it to the clipboard as one combined cut before any deletion
	// happens. Each deleteSelectionRaw call below only removes its own
	// cursor's text from the buffer — if it also wrote the clipboard
	// individually (as cutSelection does for a single cursor), every write
	// but the last processed would be clobbered, silently losing all but one
	// cursor's cut text.
	// Collected through the same helper yank uses, so the two cannot disagree
	// about ordering or about what a selection contributes. bareCursors is
	// false here: a cut with no selection deletes the character under the
	// cursor without copying it — see the helper for why that differs from
	// yank.
	if copyToClipboard {
		if parts := cursorTextsInDocumentOrder(m, false); len(parts) > 0 {
			_ = clipboardWriter(strings.Join(parts, "\n")) // cut semantics; a failed copy must not block the delete
			m.multiYank = parts                            // see Model.multiYank: lets a later paste distribute these
		}
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
		m, cmd = m.deleteSelectionRaw()
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
	return applyInsertTextToAllCursors(m, func(int, int, int) string { return text })
}

// applyInsertTextToAllCursors is applyInsertToAllCursors generalized to
// compute the inserted text per cursor via textFor(line, col) — needed for
// tab-stop-aware Tab, where the number of spaces depends on each cursor's
// own visual column. textFor is called with each cursor's line/col in the
// buffer's current state at the point that cursor's insert is about to
// apply (i.e. already reflecting any earlier cursors' inserts on that line),
// so it sees the same content that cursor's own edit will land next to.
func applyInsertTextToAllCursors(m Model, textFor func(idx, line, col int) string) (Model, tea.Cmd) {
	// Coincident cursors insert once. Two carets at one position are one
	// caret on screen, so inserting per cursor doubles what the user typed —
	// reachable whenever a remote delete spans two cursors, which collapses
	// both onto the deletion's start (document.ShiftPos). Before the
	// single-cursor check below, so a pair that collapses to one takes the
	// simple path.
	m.dedupeCursors(dedupeByPosition)
	if len(m.extraCursors) == 0 {
		text := textFor(0, m.cursor.Line, m.cursor.Col)
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
		pos       document.Pos
		isPrimary bool
		extraIdx  int
	}

	entries := []entry{{m.cursor, true, -1}}
	for i, ec := range m.extraCursors {
		entries = append(entries, entry{ec.pos, false, i})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].pos.Line != entries[j].pos.Line {
			return entries[i].pos.Line < entries[j].pos.Line
		}
		return entries[i].pos.Col < entries[j].pos.Col
	})

	var cmds []tea.Cmd
	final := make([]document.Pos, len(entries))

	// Positions still to come are carried forward through document.ShiftPos
	// rather than through per-line column/line deltas tracked by hand.
	// ShiftPos is the operational transform's own arithmetic, so it is right
	// for any insert — the hand-rolled version only knew how to move a
	// position past a bare "\n" and placed every cursor after it wrongly for
	// text that spanned lines in any other shape, which is exactly what
	// pasting at several cursors produces.
	for i := range entries {
		pos := entries[i].pos
		text := textFor(i, pos.Line, pos.Col)

		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: pos.Line,
			InsertCol:  pos.Col,
			InsertText: text,
		}
		al, d := opLineDelta(op)
		// Shift immediately, one op at a time, in the same coordinate space
		// each op is applied in — a single combined shift (min atLine, summed
		// delta) would over-shift an overlay line sitting between two edit
		// points on different lines.
		m = m.shiftLSPOverlayLines(al, d)
		inv := inverseOp(m, op)
		if m.currentGroup != nil {
			m.currentGroup = append(m.currentGroup, inv)
		} else {
			m.undoStack = append(m.undoStack, undoEntry{ops: []document.Op{inv}, before: snapBefore})
		}
		m.redoStack = nil
		m.buf.Apply(op)
		var sendCmd tea.Cmd
		m, sendCmd = m.sendToServer(op)
		cmds = append(cmds, sendCmd)

		// This cursor lands at the far end of its own inserted text.
		if nl := strings.Count(text, "\n"); nl > 0 {
			lastLine := text[strings.LastIndex(text, "\n")+1:]
			final[i] = document.Pos{Line: pos.Line + nl, Col: len([]rune(lastLine))}
		} else {
			final[i] = document.Pos{Line: pos.Line, Col: pos.Col + len([]rune(text))}
		}
		// Everything not yet processed sits after this insert and moves with
		// it. Already-placed cursors sit before it and do not.
		for j := i + 1; j < len(entries); j++ {
			entries[j].pos = document.ShiftPos(entries[j].pos, op)
		}
	}

	m.sel = nil
	for i, e := range entries {
		if e.isPrimary {
			m.cursor = final[i]
		} else {
			m.extraCursors[e.extraIdx].pos = final[i]
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
		var sendCmd tea.Cmd
		m, sendCmd = m.sendToServer(op)
		cmds = append(cmds, sendCmd)
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

// cursorDedupeMode picks what "the same place" means to dedupeCursors, and the
// two callers genuinely need different answers.
//
// A selection command compares ranges: two cursors covering the same text are
// duplicates even with anchor and head swapped, which position alone would
// miss. An insertion compares positions: it inserts at the cursor and ignores
// selections entirely, so two cursors at one position must insert once however
// their selections differ.
type cursorDedupeMode int

const (
	// dedupeBySelection keys on the selected range, falling back to position
	// for a cursor with no selection. For selection commands.
	dedupeBySelection cursorDedupeMode = iota
	// dedupeByPosition keys on position alone, ignoring selections. For
	// anything that edits at the cursor.
	dedupeByPosition
)

// dedupeCursors drops extra cursors that ended up in the same place as the
// primary cursor or an earlier extra — the same selected range when they have
// a selection, the same position when they do not. The primary cursor is
// always kept.
//
// The mi/ma text objects need this and the word/whitespace ones always did.
// Several cursors inside one function all resolve to that same function, and N
// identical selections are not N selections: a following `d` would delete the
// range once and then delete whatever slid into those coordinates N-1 times
// more. Collapsing to one cursor is both what the user means and what VS Code
// does with overlapping multi-cursor selections.
func (m *Model) dedupeCursors(mode cursorDedupeMode) {
	if len(m.extraCursors) == 0 {
		return
	}
	type key struct {
		pos        document.Pos
		start, end document.Pos
		hasSel     bool
	}
	// A selection is keyed by its range alone: two cursors covering the same
	// range necessarily share a head, so the position adds nothing, and
	// keying on it as well would keep both if one had been flipped.
	keyFor := func(pos document.Pos, sel *Selection) key {
		if sel == nil || mode == dedupeByPosition {
			return key{pos: pos}
		}
		s, e := sel.ordered()
		return key{start: s, end: e, hasSel: true}
	}

	seen := map[key]bool{keyFor(m.cursor, m.sel): true}
	kept := make([]ExtraCursor, 0, len(m.extraCursors))
	for _, ec := range m.extraCursors {
		k := keyFor(ec.pos, ec.sel)
		if seen[k] {
			continue
		}
		seen[k] = true
		kept = append(kept, ec)
	}
	m.extraCursors = kept
}

// perCursor turns a selection command written for a single cursor into one
// that runs at every cursor and then collapses duplicates.
//
// It runs fn against a copy of the model positioned at each cursor and takes
// back only the cursor and selection, which is all a selection command
// changes. A command that needs to change anything else — the buffer, the
// mode, the viewport beyond scrollToCursor — must not be wrapped this way;
// use applyToAllCursors directly and handle its own state.
//
// Commands that already call applyToAllCursors internally must not be wrapped
// either: applyToAllCursors invokes fn for the primary cursor with
// extraCursors still populated, so nesting would apply the inner command to
// every cursor again for each outer one.
func perCursor(fn func(Model) (tea.Model, tea.Cmd)) func(Model) (tea.Model, tea.Cmd) {
	return func(m Model) (tea.Model, tea.Cmd) {
		var cmds []tea.Cmd
		m.applyToAllCursors(func(mp *Model) {
			out, cmd := fn(*mp)
			r := out.(Model)
			mp.cursor, mp.sel = r.cursor, r.sel
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		})
		m.dedupeCursors(dedupeBySelection)
		return m, tea.Batch(cmds...)
	}
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
