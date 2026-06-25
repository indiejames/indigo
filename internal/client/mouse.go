package client

import "github.com/indiejames/indigo/internal/document"

// clickToPos converts a terminal (x, y) coordinate to a buffer position.
// Returns ok=false if the click is outside the text area (e.g. on the status bar).
func (m *Model) clickToPos(x, y int) (document.Pos, bool) {
	if y >= m.height-1 {
		return document.Pos{}, false
	}
	lineNum := m.topLine + y
	disp := m.displayLineCount()
	if lineNum >= disp {
		lineNum = max(0, disp-1)
	}
	col := max(0, x-m.gutterWidth())
	lineLen := m.buf.LineLen(lineNum)
	maxCol := lineLen
	if m.mode == ModeNormal && maxCol > 0 {
		maxCol--
	}
	col = min(col, maxCol)
	return document.Pos{Line: lineNum, Col: col}, true
}

func (m *Model) handleMousePress(x, y int) {
	pos, ok := m.clickToPos(x, y)
	if !ok {
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
	m.cursor = pos
	if m.sel != nil {
		m.sel.Head = pos
	}
}
