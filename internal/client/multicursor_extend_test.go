package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// extendAllFixture gives two identical lines with a cursor at the end of each,
// so a word extension must produce the same selection on both.
func extendAllFixture() Model {
	m := newTestModel("alpha beta\nalpha beta\n")
	m.cursor = document.Pos{Line: 0, Col: 10} // end of "alpha beta"
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 10}, goalCol: -1}}
	return m
}

// B (extend word backward) must extend every cursor's selection, not just the
// primary one.
//
// Reported from a real session: with several cursors open, B moved only the
// first. Every other extend* command — E, W, X, shift+arrows, shift+home/end —
// routes through applyToAllCursors; executeExtendWordBackward alone did not.
func TestExtendWordBackwardAppliesToAllCursors(t *testing.T) {
	m := extendAllFixture()

	m2, _ := executeExtendWordBackward(m)
	got := m2.(Model)

	if got.sel == nil {
		t.Fatal("primary cursor has no selection")
	}
	if len(got.extraCursors) != 1 {
		t.Fatalf("got %d extra cursors, want 1", len(got.extraCursors))
	}
	extra := got.extraCursors[0]
	if extra.sel == nil {
		t.Fatal("extra cursor has no selection: B applied to the primary cursor only")
	}
	// Same text on both lines, so the columns must match line for line.
	if extra.sel.Anchor.Col != got.sel.Anchor.Col || extra.sel.Head.Col != got.sel.Head.Col {
		t.Errorf("extra selection cols = %d..%d, primary = %d..%d; want identical",
			extra.sel.Anchor.Col, extra.sel.Head.Col, got.sel.Anchor.Col, got.sel.Head.Col)
	}
	if extra.sel.Anchor.Line != 1 || extra.sel.Head.Line != 1 {
		t.Errorf("extra selection lines = %d..%d, want 1..1 (its own line)",
			extra.sel.Anchor.Line, extra.sel.Head.Line)
	}
	if extra.pos.Col != got.cursor.Col {
		t.Errorf("extra cursor col = %d, primary = %d; want identical", extra.pos.Col, got.cursor.Col)
	}
}

// E is the sibling the report compared against, and was already correct.
// Pinned so the pair cannot drift apart again.
func TestExtendWordForwardAppliesToAllCursors(t *testing.T) {
	m := newTestModel("alpha beta\nalpha beta\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 0}, goalCol: -1}}

	m2, _ := executeExtendWordForward(m)
	got := m2.(Model)

	if got.sel == nil {
		t.Fatal("primary cursor has no selection")
	}
	if got.extraCursors[0].sel == nil {
		t.Fatal("extra cursor has no selection: E applied to the primary cursor only")
	}
	if got.extraCursors[0].sel.Head.Col != got.sel.Head.Col {
		t.Errorf("extra head col = %d, primary = %d; want identical",
			got.extraCursors[0].sel.Head.Col, got.sel.Head.Col)
	}
}
