package client

import (
	"testing"

	"github.com/indiejames/twist/internal/document"
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

// --- deleteSelection (safe cases only) ---

func TestDeleteSelectionNilSelEmptyLine(t *testing.T) {
	// On an empty line with nil selection, deleteSelection is a no-op.
	m := newTestModel("\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, cmd := m.deleteSelection()
	if cmd != nil {
		t.Error("deleteSelection on empty line should return nil cmd")
	}
	_ = m2
}
