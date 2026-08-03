package client

import (
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
)

func TestClickToPosStatusBar(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	// y == height-1 is the status bar row
	_, ok := m.clickToPos(0, m.height-1)
	if ok {
		t.Error("click on status bar row should return ok=false")
	}
}

func TestClickToPosInTextArea(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	// cfg is nil → gutterWidth = 0
	pos, ok := m.clickToPos(3, 0)
	if !ok {
		t.Fatal("click in text area should return ok=true")
	}
	if pos.Line != 0 {
		t.Errorf("pos.Line = %d, want 0", pos.Line)
	}
	if pos.Col != 3 {
		t.Errorf("pos.Col = %d, want 3", pos.Col)
	}
}

func TestClickToPosClampsToBeyondLastLine(t *testing.T) {
	m := newTestModel("hello\n")
	// Click below the last line
	pos, ok := m.clickToPos(0, 10)
	if !ok {
		t.Fatal("click below last line should still return ok=true")
	}
	disp := m.displayLineCount()
	if pos.Line >= disp {
		t.Errorf("pos.Line = %d should be < displayLineCount %d", pos.Line, disp)
	}
}

func TestClickToPosClampsColToLineLen(t *testing.T) {
	m := newTestModel("hi\n")
	// "hi" has len 2; a following line exists, so normal mode maxCol = 2
	// (the cursor may rest on the line break itself).
	pos, ok := m.clickToPos(99, 0)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if pos.Col > 2 {
		t.Errorf("pos.Col = %d should be <= 2 in normal mode", pos.Col)
	}
}

func TestHandleMousePressSetsSelection(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.handleMousePress(2, 0)
	want := document.Pos{Line: 0, Col: 2}
	if m.cursor != want {
		t.Errorf("cursor = %v, want %v", m.cursor, want)
	}
	if m.sel == nil {
		t.Fatal("sel should be set after mouse press")
	}
	if m.sel.Anchor != want || m.sel.Head != want {
		t.Errorf("sel = {%v, %v}, want both %v", m.sel.Anchor, m.sel.Head, want)
	}
	if !m.dragging {
		t.Error("dragging should be true after mouse press")
	}
}

func TestHandleMousePressOnStatusBar(t *testing.T) {
	m := newTestModel("hello\n")
	origCursor := m.cursor
	m.handleMousePress(0, m.height-1)
	if m.cursor != origCursor {
		t.Error("click on status bar should not move cursor")
	}
	if m.sel != nil {
		t.Error("click on status bar should not set selection")
	}
}

func TestHandleMouseDragUpdatesHead(t *testing.T) {
	m := newTestModel("hello world\n")
	// Set up a press first
	m.handleMousePress(0, 0)
	// Now drag to col 5
	m.handleMouseDrag(5, 0)
	if m.sel == nil {
		t.Fatal("sel should remain set after drag")
	}
	if m.sel.Head.Col != 5 {
		t.Errorf("sel.Head.Col = %d, want 5", m.sel.Head.Col)
	}
}

func TestHandleMouseDragWithoutPress(t *testing.T) {
	m := newTestModel("hello\n")
	// Drag with no prior press (sel == nil): should not panic
	m.handleMouseDrag(3, 0)
	// sel remains nil — no crash
	if m.sel != nil {
		t.Error("drag without press should not create selection")
	}
}

func TestDoubleClickSelectsWord(t *testing.T) {
	m := newTestModel("hello world\n")
	// First click at col 2 (inside "hello")
	m.handleMousePress(2, 0)
	// Second click at same position within the double-click window
	m.lastClickAt = time.Now().Add(-100 * time.Millisecond)
	m.handleMousePress(2, 0)

	if m.sel == nil {
		t.Fatal("double-click should set a selection")
	}
	if m.dragging {
		t.Error("dragging should be false after double-click")
	}
	start, end := m.sel.ordered()
	got := []rune(m.buf.Line(0))[start.Col : end.Col+1]
	if string(got) != "hello" {
		t.Errorf("selected %q, want %q", string(got), "hello")
	}
}

// TestClickToPosWrappedLine verifies that a click on screen row 1 (the second
// chunk of a soft-wrapped line) resolves to the correct buffer line, not
// topLine+1 (which would be the wrong line when the first line wraps).
func TestClickToPosWrappedLine(t *testing.T) {
	// Two lines: line 0 wraps, line 1 is short.
	// With width=10 and no gutter, line 0 occupies rows 0+1, line 1 is row 2.
	long := "0123456789AB" // 12 chars → 2 chunks at cw=10
	m := newTestModel(long + "\nhello\n")
	m.width = 10
	m.height = 6

	// Click on screen row 2 (which is buffer line 1 "hello"), col 1.
	pos, ok := m.clickToPos(1, 2)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if pos.Line != 1 {
		t.Errorf("pos.Line = %d, want 1 (was topLine+y = %d before fix)", pos.Line, m.topLine+2)
	}
	if pos.Col != 1 {
		t.Errorf("pos.Col = %d, want 1", pos.Col)
	}
}

func TestDoubleClickOutsideWindowDoesNotSelect(t *testing.T) {
	m := newTestModel("hello world\n")
	m.handleMousePress(2, 0)
	// Second click after the window has expired
	m.lastClickAt = time.Now().Add(-(doubleClickWindow + time.Millisecond))
	m.handleMousePress(2, 0)

	// Should behave like a normal single click: point selection, dragging true
	if !m.dragging {
		t.Error("slow second click should still set dragging")
	}
	if m.sel != nil && m.sel.Anchor != m.sel.Head {
		t.Error("slow second click should not expand selection to a word")
	}
}

// Regression test: mouse wheel must scroll the viewport, dragging the cursor
// only as far as needed to keep it on screen.
func TestScrollWheel(t *testing.T) {
	var content string
	for i := 0; i < 100; i++ {
		content += "line\n"
	}
	m := newTestModel(content) // height 24 → 23 visible rows

	// Scroll down: viewport moves, cursor (line 0) gets pulled to topLine.
	m.scrollWheel(wheelScrollLines)
	if m.topLine != wheelScrollLines {
		t.Errorf("topLine = %d, want %d", m.topLine, wheelScrollLines)
	}
	if m.cursor.Line != m.topLine {
		t.Errorf("cursor.Line = %d, want %d (pulled to top)", m.cursor.Line, m.topLine)
	}

	// Cursor in the middle of the view stays put on a small scroll.
	m.cursor.Line = m.topLine + 10 // 13, still visible after scrolling to 6
	m.scrollWheel(wheelScrollLines)
	if m.cursor.Line != 13 {
		t.Errorf("cursor.Line = %d, want 13 (untouched)", m.cursor.Line)
	}

	// Scroll up past the start clamps to 0.
	m.scrollWheel(-1000)
	if m.topLine != 0 {
		t.Errorf("topLine = %d, want 0 after clamped scroll up", m.topLine)
	}

	// Scroll down past the end clamps to the last displayable line.
	m.scrollWheel(100000)
	if want := m.displayLineCount() - 1; m.topLine != want {
		t.Errorf("topLine = %d, want %d after clamped scroll down", m.topLine, want)
	}
}
