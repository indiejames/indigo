package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// --- Selection.ordered ---

func TestSelectionOrderedAnchorFirst(t *testing.T) {
	s := &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 0, Col: 7},
	}
	start, end := s.ordered()
	if start != s.Anchor || end != s.Head {
		t.Errorf("ordered() = (%v, %v), want (%v, %v)", start, end, s.Anchor, s.Head)
	}
}

func TestSelectionOrderedHeadFirst(t *testing.T) {
	s := &Selection{
		Anchor: document.Pos{Line: 0, Col: 7},
		Head:   document.Pos{Line: 0, Col: 2},
	}
	start, end := s.ordered()
	if start != s.Head || end != s.Anchor {
		t.Errorf("ordered() reversed: start = %v, want %v", start, s.Head)
	}
}

func TestSelectionOrderedMultiLine(t *testing.T) {
	s := &Selection{
		Anchor: document.Pos{Line: 2, Col: 0},
		Head:   document.Pos{Line: 0, Col: 5},
	}
	start, end := s.ordered()
	if start.Line != 0 || end.Line != 2 {
		t.Errorf("multiline ordered: start.Line=%d end.Line=%d, want 0,2", start.Line, end.Line)
	}
}

// --- selectWord ---

func TestSelectWordFresh(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.selectWord()
	if m.sel == nil {
		t.Fatal("selectWord: sel is nil")
	}
	if m.sel.Anchor.Col != 0 || m.sel.Head.Col != 4 {
		t.Errorf("selectWord: [%d,%d], want [0,4]", m.sel.Anchor.Col, m.sel.Head.Col)
	}
}

func TestSelectWordAdvances(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	// First call: select "hello"
	m.selectWord()
	// Second call: advance to "world"
	m.selectWord()
	if m.sel == nil {
		t.Fatal("selectWord advance: sel is nil")
	}
	if m.sel.Anchor.Col != 6 || m.sel.Head.Col != 10 {
		t.Errorf("advance: [%d,%d], want [6,10]", m.sel.Anchor.Col, m.sel.Head.Col)
	}
}

func TestSelectWordOnSpace(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 5} // on space
	m.selectWord()
	if m.sel == nil {
		t.Fatal("selectWord on space: sel is nil")
	}
	// Skips space and selects "world"
	if m.sel.Anchor.Col != 6 {
		t.Errorf("selectWord on space: start = %d, want 6", m.sel.Anchor.Col)
	}
}

// --- selectLine ---

func TestSelectLine(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.cursor = document.Pos{Line: 0, Col: 2}
	m.selectLine()
	if m.sel == nil {
		t.Fatal("selectLine: sel is nil")
	}
	if !m.sel.IsLine {
		t.Error("selectLine: IsLine should be true")
	}
	if m.sel.Anchor.Col != 0 {
		t.Errorf("selectLine: Anchor.Col = %d, want 0", m.sel.Anchor.Col)
	}
	if m.sel.Head.Col != 4 { // "hello" len=5, maxCol=4
		t.Errorf("selectLine: Head.Col = %d, want 4", m.sel.Head.Col)
	}
}

func TestSelectLineEmptyLine(t *testing.T) {
	m := newTestModel("hello\n\nworld\n")
	m.cursor = document.Pos{Line: 1, Col: 0} // empty line
	m.selectLine()
	if m.sel == nil {
		t.Fatal("selectLine empty: sel is nil")
	}
	if !m.sel.IsLine {
		t.Error("selectLine empty: IsLine should be true")
	}
	if m.sel.Anchor.Col != 0 || m.sel.Head.Col != 0 {
		t.Errorf("selectLine empty: [%d,%d], want [0,0]", m.sel.Anchor.Col, m.sel.Head.Col)
	}
}

// --- selectLine extend ---

func TestSelectLineExtendForward(t *testing.T) {
	m := newTestModel("hello\nworld\nfoo\n")
	m.cursor = document.Pos{Line: 0}
	m.selectLine()
	if m.sel.Head.Line != 0 {
		t.Fatalf("first x: Head.Line = %d, want 0", m.sel.Head.Line)
	}
	m.selectLine() // second press
	if m.sel.Head.Line != 1 {
		t.Errorf("second x: Head.Line = %d, want 1", m.sel.Head.Line)
	}
	if m.sel.Anchor.Line != 0 {
		t.Errorf("second x: Anchor.Line = %d, want 0", m.sel.Anchor.Line)
	}
}

// --- extendLineBackward ---

func TestExtendLineBackward(t *testing.T) {
	m := newTestModel("alpha\nbeta\ngamma\n")
	m.cursor = document.Pos{Line: 1}
	m.selectLine()
	m.extendLineBackward()
	start, _ := m.sel.ordered()
	if start.Line != 0 {
		t.Errorf("extendLineBackward: start.Line = %d, want 0", start.Line)
	}
}

// --- selectWord cross-line ---

func TestSelectWordCrossesLine(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.selectWord() // select "hello"
	m.selectWord() // should advance to "world" on line 1
	if m.sel == nil {
		t.Fatal("cross-line selectWord: sel is nil")
	}
	if m.sel.Anchor.Line != 1 {
		t.Errorf("cross-line: Anchor.Line = %d, want 1", m.sel.Anchor.Line)
	}
}

// --- extendWordForward / extendWordBackward ---

func TestExtendWordForward(t *testing.T) {
	m := newTestModel("foo bar baz\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.extendWordForward()
	if m.sel == nil {
		t.Fatal("extendWordForward: sel is nil")
	}
	// Should extend to end of "foo" (col 2).
	if m.sel.Head.Col != 2 {
		t.Errorf("extendWordForward: Head.Col = %d, want 2", m.sel.Head.Col)
	}
	m.extendWordForward()
	// Second call extends to end of "bar" (col 6).
	if m.sel.Head.Col != 6 {
		t.Errorf("extendWordForward x2: Head.Col = %d, want 6", m.sel.Head.Col)
	}
}

func TestExtendWordBackward(t *testing.T) {
	m := newTestModel("foo bar\n")
	m.cursor = document.Pos{Line: 0, Col: 6} // end of "bar"
	m.extendWordBackward()
	if m.sel == nil {
		t.Fatal("extendWordBackward: sel is nil")
	}
	// Head moves back to start of "bar" (col 4).
	if m.sel.Head.Col != 4 {
		t.Errorf("extendWordBackward: Head.Col = %d, want 4", m.sel.Head.Col)
	}
}

// --- moveToPrevWordStart / moveToWordEnd ---

func TestMoveToPrevWordStart(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 8} // mid "world"
	m.moveToPrevWordStart()
	if m.cursor.Col != 6 {
		t.Errorf("moveToPrevWordStart: Col = %d, want 6", m.cursor.Col)
	}
	m.moveToPrevWordStart()
	if m.cursor.Col != 0 {
		t.Errorf("moveToPrevWordStart x2: Col = %d, want 0", m.cursor.Col)
	}
}

func TestMoveToWordEnd(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.moveToWordEnd()
	if m.cursor.Col != 4 {
		t.Errorf("moveToWordEnd: Col = %d, want 4", m.cursor.Col)
	}
	m.moveToWordEnd()
	if m.cursor.Col != 10 {
		t.Errorf("moveToWordEnd x2: Col = %d, want 10", m.cursor.Col)
	}
}

// --- selectAll / flipSelection ---

func TestSelectAll(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.selectAll()
	if m.sel == nil {
		t.Fatal("selectAll: sel is nil")
	}
	start, end := m.sel.ordered()
	if start.Line != 0 || start.Col != 0 {
		t.Errorf("selectAll start = %v, want {0,0}", start)
	}
	// "hello\nworld\n" has a trailing empty line; end must be past line 0.
	if end.Line < 1 {
		t.Errorf("selectAll end.Line = %d, want >= 1", end.Line)
	}
}

func TestFlipSelection(t *testing.T) {
	m := newTestModel("hello world\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 5},
	}
	m.cursor = document.Pos{Line: 0, Col: 5}
	m.flipSelection()
	if m.cursor.Col != 0 {
		t.Errorf("flipSelection: cursor.Col = %d, want 0", m.cursor.Col)
	}
	if m.sel.Anchor.Col != 5 || m.sel.Head.Col != 0 {
		t.Errorf("flipSelection: Anchor=%d Head=%d, want 5,0", m.sel.Anchor.Col, m.sel.Head.Col)
	}
}

// --- deleteSelection (safe cases only) ---

func TestDeleteSelectionNilSelEmptyLine(t *testing.T) {
	// An empty line's cursor rests on its own line break; deleting it joins
	// with the next line.
	m := newTestModel("\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, cmd := m.deleteSelection()
	if cmd == nil {
		t.Error("deleteSelection on empty line with a next line should join, not no-op")
	}
	if m2.buf.LineCount() != 1 {
		t.Errorf("LineCount() = %d, want 1", m2.buf.LineCount())
	}
}

func TestDeleteSelectionNilSelEmptyLineAtEOF(t *testing.T) {
	// The truly-final empty line has no line break to delete: no-op.
	m := newTestModel("")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, cmd := m.deleteSelection()
	if cmd != nil {
		t.Error("deleteSelection at true EOF should return nil cmd")
	}
	_ = m2
}
