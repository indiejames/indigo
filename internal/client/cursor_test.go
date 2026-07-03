package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// --- moveCursor ---

func TestMoveCursorDown(t *testing.T) {
	m := newTestModel("line1\nline2\nline3\n")
	m.moveCursor(1, 0)
	if m.cursor.Line != 1 {
		t.Errorf("cursor.Line = %d, want 1", m.cursor.Line)
	}
}

func TestMoveCursorClampsBelowBuffer(t *testing.T) {
	m := newTestModel("line1\nline2\n")
	m.moveCursor(99, 0)
	last := m.buf.LineCount() - 1
	if m.cursor.Line != last {
		t.Errorf("cursor.Line = %d, want %d", m.cursor.Line, last)
	}
}

func TestMoveCursorClampsAboveBuffer(t *testing.T) {
	m := newTestModel("abc\ndef\n")
	m.cursor.Line = 1
	m.moveCursor(-99, 0)
	if m.cursor.Line != 0 {
		t.Errorf("cursor.Line = %d, want 0", m.cursor.Line)
	}
}

func TestMoveCursorNormalModeClampsCol(t *testing.T) {
	// Normal mode: maxCol = lineLen-1
	m := newTestModel("hello\n")
	m.moveCursor(0, 10)
	// "hello" has len 5, maxCol = 4 in normal mode
	if m.cursor.Col != 4 {
		t.Errorf("cursor.Col = %d, want 4", m.cursor.Col)
	}
}

func TestMoveCursorInsertModeAllowsEndCol(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.moveCursor(0, 10)
	// Insert mode: maxCol = lineLen = 5
	if m.cursor.Col != 5 {
		t.Errorf("cursor.Col = %d, want 5", m.cursor.Col)
	}
}

// --- clampCursor ---

func TestClampCursorBeyondBuffer(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 99, Col: 99}
	m.clampCursor()
	if m.cursor.Line >= m.buf.LineCount() {
		t.Errorf("cursor.Line = %d, should be < %d", m.cursor.Line, m.buf.LineCount())
	}
}

func TestClampCursorBeyondLineLen(t *testing.T) {
	m := newTestModel("hi\n")
	m.cursor = document.Pos{Line: 0, Col: 99}
	m.clampCursor()
	if m.cursor.Col > m.buf.LineLen(0) {
		t.Errorf("cursor.Col = %d, should be <= %d", m.cursor.Col, m.buf.LineLen(0))
	}
}

// --- scrollToCursor ---

func TestScrollToCursorScrollsDown(t *testing.T) {
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.height = 4 // visibleLines = 3
	m.cursor.Line = 5
	m.topLine = 0
	m.scrollToCursor()
	if m.topLine == 0 {
		t.Error("topLine should have advanced to scroll cursor into view")
	}
	if m.cursor.Line < m.topLine || m.cursor.Line >= m.topLine+m.visibleLines() {
		t.Errorf("cursor %d not in view [%d, %d)", m.cursor.Line, m.topLine, m.topLine+m.visibleLines())
	}
}

func TestScrollToCursorScrollsUp(t *testing.T) {
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.height = 4
	m.topLine = 4
	m.cursor.Line = 1
	m.scrollToCursor()
	if m.topLine != 1 {
		t.Errorf("topLine = %d, want 1", m.topLine)
	}
}

// --- displayLineCount ---

func TestDisplayLineCountWithTrailingNewline(t *testing.T) {
	m := newTestModel("a\nb\n")
	// LineCount=3 (3 lines: "a", "b", ""), trailing empty → displayLineCount=2
	got := m.displayLineCount()
	if got != 2 {
		t.Errorf("displayLineCount = %d, want 2", got)
	}
}

func TestDisplayLineCountNoTrailingNewline(t *testing.T) {
	m := newTestModel("a\nb")
	// LineCount=2 ("a", "b"), no trailing empty → displayLineCount=2
	got := m.displayLineCount()
	if got != 2 {
		t.Errorf("displayLineCount = %d, want 2", got)
	}
}

// --- selectionCols ---

func TestSelectionColsNoSelection(t *testing.T) {
	m := newTestModel("hello\n")
	a, b := m.selectionCols(0, 5)
	if a != -1 || b != -1 {
		t.Errorf("no sel: got (%d, %d), want (-1, -1)", a, b)
	}
}

func TestSelectionColsSameLine(t *testing.T) {
	m := newTestModel("hello world\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 0, Col: 6},
	}
	a, b := m.selectionCols(0, 11)
	if a != 2 || b != 6 {
		t.Errorf("selectionCols = (%d, %d), want (2, 6)", a, b)
	}
}

func TestSelectionColsLineNotSelected(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 4},
	}
	a, b := m.selectionCols(1, 5)
	if a != -1 || b != -1 {
		t.Errorf("unselected line: got (%d, %d), want (-1, -1)", a, b)
	}
}

func TestSelectionColsMultiLineStartLine(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 1, Col: 3},
	}
	a, b := m.selectionCols(0, 5) // start line: from col 2 to end
	if a != 2 {
		t.Errorf("start line selA = %d, want 2", a)
	}
	if b != 4 { // lineLen-1 = 4
		t.Errorf("start line selB = %d, want 4", b)
	}
}

func TestSelectionColsMultiLineEndLine(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 1, Col: 3},
	}
	a, b := m.selectionCols(1, 5) // end line: from col 0 to col 3
	if a != 0 || b != 3 {
		t.Errorf("end line selectionCols = (%d, %d), want (0, 3)", a, b)
	}
}

func TestSelectionColsLineSelection(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 4},
		IsLine: true,
	}
	a, b := m.selectionCols(0, 5)
	if a != 0 {
		t.Errorf("line sel selA = %d, want 0", a)
	}
	if b != 4 {
		t.Errorf("line sel selB = %d, want 4", b)
	}
}

func TestSelectionColsEmptyLine(t *testing.T) {
	m := newTestModel("hello\n\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 1, Col: 0},
		Head:   document.Pos{Line: 1, Col: 0},
	}
	// Empty line: lineLen=0 → returns -1,-1
	a, b := m.selectionCols(1, 0)
	if a != -1 || b != -1 {
		t.Errorf("empty line: got (%d, %d), want (-1, -1)", a, b)
	}
}

// --- soft-wrap helpers ---

func TestVisualChunks(t *testing.T) {
	cases := []struct {
		expandedLen, cw, want int
	}{
		{0, 80, 1},   // empty line still takes one row
		{80, 80, 1},  // exact fit
		{81, 80, 2},  // one char overflow → second chunk
		{160, 80, 2}, // exactly two chunks
		{161, 80, 3}, // three chunks
		{5, 80, 1},   // short line
	}
	for _, c := range cases {
		got := visualChunks(c.expandedLen, c.cw)
		if got != c.want {
			t.Errorf("visualChunks(%d, %d) = %d, want %d", c.expandedLen, c.cw, got, c.want)
		}
	}
}

func TestBuildScreenLayoutNoWrap(t *testing.T) {
	// Five short lines, each fits in one screen row.
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.height = 6 // vis = 5
	m.width = 80
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	if len(layout) != vis {
		t.Fatalf("layout len = %d, want %d", len(layout), vis)
	}
	for i, e := range layout {
		if e.bufLine != i || e.chunk != 0 || e.chunkStart != 0 {
			t.Errorf("layout[%d] = %+v, want {bufLine:%d chunk:0 chunkStart:0}", i, e, i)
		}
	}
}

func TestBuildScreenLayoutWraps(t *testing.T) {
	// One line that is 100 chars wide, viewport is 40 cols → 3 chunks.
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel(string(longLine) + "\n")
	m.height = 6 // vis = 5
	m.width = 40 // cw = 40 (no gutter when LineNumbers disabled)
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	// 100 chars at cw=40 → ceil(100/40)=3 chunks.
	if layout[0].bufLine != 0 || layout[0].chunk != 0 || layout[0].chunkStart != 0 {
		t.Errorf("chunk0: %+v", layout[0])
	}
	if layout[1].bufLine != 0 || layout[1].chunk != 1 || layout[1].chunkStart != 40 {
		t.Errorf("chunk1: %+v", layout[1])
	}
	if layout[2].bufLine != 0 || layout[2].chunk != 2 || layout[2].chunkStart != 80 {
		t.Errorf("chunk2: %+v", layout[2])
	}
}

func TestScrollToCursorWrappedLine(t *testing.T) {
	// Line is 100 chars; viewport is 40 cols wide and 4 rows tall (vis=3).
	// Cursor at col 85 (chunk 2, chunkStart 80) — the third visual row.
	// With topLine=0 the cursor is at visual row 2, which fits in vis=3. No scroll needed.
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel(string(longLine) + "\n")
	m.height = 4
	m.width = 40
	m.cursor.Line = 0
	m.cursor.Col = 85
	m.topLine = 0
	m.scrollToCursor()
	if m.topLine != 0 {
		t.Errorf("topLine = %d, want 0 (cursor fits in first 3 visual rows)", m.topLine)
	}
}

func TestCursorVisualRowFromTop(t *testing.T) {
	// Short lines: cursor at buffer line 2 is visual row 2 from topLine 0.
	m := newTestModel("a\nb\nc\nd\n")
	m.width = 80
	m.cursor.Line = 2
	m.topLine = 0
	got := m.cursorVisualRowFromTop(m.contentWidth())
	if got != 2 {
		t.Errorf("short lines: cursorVisualRowFromTop = %d, want 2", got)
	}
}

func TestScreenRowOf(t *testing.T) {
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel(string(longLine) + "\n")
	m.height = 6
	m.width = 40
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)

	// visCol=0 → chunk 0 → row 0
	if r := screenRowOf(layout, 0, 0, cw); r != 0 {
		t.Errorf("visCol=0: row=%d, want 0", r)
	}
	// visCol=40 → chunk 1 → row 1
	if r := screenRowOf(layout, 0, 40, cw); r != 1 {
		t.Errorf("visCol=40: row=%d, want 1", r)
	}
	// visCol=80 → chunk 2 → row 2
	if r := screenRowOf(layout, 0, 80, cw); r != 2 {
		t.Errorf("visCol=80: row=%d, want 2", r)
	}
	// bufLine not in layout → -1
	if r := screenRowOf(layout, 99, 0, cw); r != -1 {
		t.Errorf("missing bufLine: row=%d, want -1", r)
	}
}
