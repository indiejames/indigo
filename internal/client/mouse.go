package client

import (
	"time"

	"github.com/indiejames/indigo/internal/document"
)

const doubleClickWindow = 400 * time.Millisecond

// clickToPos converts a terminal (x, y) coordinate to a buffer position.
// Returns ok=false if the click is outside the text area (e.g. on the status bar).
//
// With soft-wrap, multiple screen rows can belong to the same buffer line, so
// we consult the same layout that View() builds rather than using topLine+y.
func (m *Model) clickToPos(x, y int) (document.Pos, bool) {
	if y >= m.height-1 {
		return document.Pos{}, false
	}
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	if y >= len(layout) {
		return document.Pos{}, false
	}

	entry := layout[y]
	lineNum := entry.bufLine

	// Clamp to displayable lines (exclude phantom trailing line).
	disp := m.displayLineCount()
	if lineNum >= disp {
		lineNum = max(0, disp-1)
	}

	// Convert screen x to a visual column within the full (unwrapped) line.
	contentX := max(0, x-m.gutterWidth())
	absVisCol := entry.chunkStart + contentX

	// Map the visual column back to a buffer column via colMap.
	lineRunes := []rune(m.buf.Line(lineNum))
	_, colMap := expandTabsRemap(lineRunes)
	col := runeColForVisualCol(lineRunes, colMap, absVisCol)

	maxCol := m.buf.LineLen(lineNum)
	if m.mode == ModeNormal {
		maxCol = m.normalLineEnd(lineNum)
	}
	col = min(col, maxCol)

	return document.Pos{Line: lineNum, Col: col}, true
}

func (m *Model) handleMousePress(x, y int) {
	pos, ok := m.clickToPos(x, y)
	if !ok {
		return
	}
	m.goalCol = -1 // clicking sets an explicit column; forget any remembered Up/Down goal
	if m.snippetOn {
		*m = m.exitSnippet()
	}
	now := time.Now()
	isDoubleClick := now.Sub(m.lastClickAt) <= doubleClickWindow && m.lastClickPos == pos
	m.lastClickAt = now
	m.lastClickPos = pos

	if isDoubleClick {
		m.cursor = pos
		m.sel = nil
		runes := []rune(m.buf.Line(pos.Line))
		if s, e, ok := findWholeWordAt(runes, pos.Col); ok {
			m.sel = &Selection{
				Anchor: document.Pos{Line: pos.Line, Col: s},
				Head:   document.Pos{Line: pos.Line, Col: e},
			}
			m.cursor = m.sel.Head
		}
		m.dragging = false
		return
	}
	m.cursor = pos
	m.sel = &Selection{Anchor: pos, Head: pos}
	m.dragging = true
}

func (m *Model) handleMouseDrag(x, y int) {
	pos, ok := m.clickToPos(x, y)
	if !ok {
		return
	}
	m.goalCol = -1
	m.cursor = pos
	if m.sel != nil {
		m.sel.Head = pos
	}
}

// wheelScrollLines is how many buffer lines one wheel tick scrolls.
const wheelScrollLines = 3

// scrollWheel moves the viewport by delta buffer lines (positive = down),
// dragging the cursor along only as much as needed to keep it on screen.
// The cursor column is clamped manually — clampCursor would call
// scrollToCursor and undo the scroll.
func (m *Model) scrollWheel(delta int) {
	maxTop := max(0, m.displayLineCount()-1)
	m.topLine = min(maxTop, max(0, m.topLine+delta))
	m.topChunk = 0 // reset to chunk 0 when scrolling by whole lines

	layout := m.buildScreenLayout(m.visibleLines(), m.contentWidth())
	if len(layout) == 0 {
		return
	}
	lastVisible := layout[len(layout)-1].bufLine
	switch {
	case m.cursor.Line < m.topLine:
		m.cursor.Line = m.topLine
	case m.cursor.Line > lastVisible:
		m.cursor.Line = lastVisible
	default:
		return // cursor still visible — leave it alone
	}
	maxCol := m.buf.LineLen(m.cursor.Line)
	if m.mode == ModeNormal {
		maxCol = m.normalLineEnd(m.cursor.Line)
	}
	m.cursor.Col = min(m.cursor.Col, max(0, maxCol))
}
