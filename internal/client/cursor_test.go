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
