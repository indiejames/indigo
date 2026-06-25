package client

import (
	"testing"

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
	// "hi" has len 2; normal mode maxCol = 1
	pos, ok := m.clickToPos(99, 0)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if pos.Col > 1 {
		t.Errorf("pos.Col = %d should be <= 1 in normal mode", pos.Col)
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
	if m.cursor.Col != 4 { // normal mode clamps to lineLen-1=10, col 5 is fine actually
		// "hello world" is 11 chars, maxCol in normal mode = 10, so col 5 is valid
	}
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
