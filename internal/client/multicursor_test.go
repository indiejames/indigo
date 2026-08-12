package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// TestApplyInsertToAllCursorsShiftsLSPOverlays is a regression test:
// applyInsertToAllCursors used to reimplement the edit-apply path inline
// without ever calling shiftLSPOverlayLines/scheduleLSPOverlayRefresh, so
// cached semantic-token/inlay-hint data went stale (pointing at pre-edit
// line numbers) after multi-cursor typing that changes the line count.
func TestApplyInsertToAllCursorsShiftsLSPOverlays(t *testing.T) {
	m := newTestModel("aaa\nbbb\nccc\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 0}}}
	// A cached semantic span on line 2 ("ccc"), the line after both cursors.
	m.semanticSpans = highlight.LineSpans{2: []highlight.Span{{StartCol: 0, EndCol: 3, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 2, Col: 0, Label: "hint"}}

	m2, cmd := applyInsertToAllCursors(m, "\n")

	// Two newlines inserted (one per cursor) push line 2's content down to
	// line 4.
	if _, stillAt2 := m2.semanticSpans[2]; stillAt2 {
		t.Error("semanticSpans still has a stale entry at line 2 — not shifted")
	}
	if spans, ok := m2.semanticSpans[4]; !ok || len(spans) != 1 {
		t.Errorf("semanticSpans[4] = %v, want the span shifted down from line 2", m2.semanticSpans[4])
	}
	if len(m2.inlayHints) != 1 || m2.inlayHints[0].Line != 4 {
		t.Errorf("inlayHints = %+v, want a single hint shifted to line 4", m2.inlayHints)
	}
	if m2.lspOverlaySeq != m.lspOverlaySeq+1 {
		t.Errorf("lspOverlaySeq = %d, want %d (scheduleLSPOverlayRefresh never called)", m2.lspOverlaySeq, m.lspOverlaySeq+1)
	}
	if cmd == nil {
		t.Error("expected a non-nil batched command (includes the overlay-refresh tick)")
	}
}

// TestApplyInsertToAllCursorsShiftsOverlayBetweenEditsOnce is a regression
// test: shifting overlays with one combined (min atLine, summed delta) call
// over-shifts a line sitting *between* two cursors' edit points. Cursors at
// lines 1 and 4 each insert a newline; an overlay at line 3 sits between
// them and must end up shifted by the first edit only (+1, to line 4), not
// by both (+2, to line 5) — the second edit (adjusted to line 5) lands
// below the overlay's already-shifted position and shouldn't touch it.
func TestApplyInsertToAllCursorsShiftsOverlayBetweenEditsOnce(t *testing.T) {
	m := newTestModel("line0\nline1\nline2\nline3\nline4\nline5\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 1, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 4, Col: 0}}}
	m.semanticSpans = highlight.LineSpans{3: []highlight.Span{{StartCol: 0, EndCol: 5, ANSI: "x"}}}

	m2, _ := applyInsertToAllCursors(m, "\n")

	if _, stillAt3 := m2.semanticSpans[3]; stillAt3 {
		t.Error("semanticSpans still has a stale entry at line 3 — not shifted")
	}
	if spans, ok := m2.semanticSpans[4]; !ok || len(spans) != 1 {
		t.Errorf("semanticSpans = %v, want the span shifted to line 4 (once, by the first edit only)", m2.semanticSpans)
	}
	if _, overShifted := m2.semanticSpans[5]; overShifted {
		t.Error("semanticSpans has an entry at line 5 — over-shifted by both edits instead of just the first")
	}
}

// TestApplyBackspaceToAllCursorsShiftsOverlayBetweenEditsOnce is the
// backspace counterpart: cursors at lines 2 and 5 each join with the
// previous line (back-to-front processing order). An overlay at line 3
// sits between the two join points and must shift by the second-processed
// edit only (-1, to line 2), not by both (-2, to line 1).
func TestApplyBackspaceToAllCursorsShiftsOverlayBetweenEditsOnce(t *testing.T) {
	m := newTestModel("line0\nline1\nline2\nline3\nline4\nline5\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 2, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 5, Col: 0}}}
	m.semanticSpans = highlight.LineSpans{3: []highlight.Span{{StartCol: 0, EndCol: 5, ANSI: "x"}}}

	m2, _ := applyBackspaceToAllCursors(m)

	if _, stillAt3 := m2.semanticSpans[3]; stillAt3 {
		t.Error("semanticSpans still has a stale entry at line 3 — not shifted")
	}
	if spans, ok := m2.semanticSpans[2]; !ok || len(spans) != 1 {
		t.Errorf("semanticSpans = %v, want the span shifted to line 2 (once, by the second-processed edit only)", m2.semanticSpans)
	}
	if _, overShifted := m2.semanticSpans[1]; overShifted {
		t.Error("semanticSpans has an entry at line 1 — over-shifted by both edits instead of just one")
	}
}

// TestApplyBackspaceToAllCursorsShiftsLSPOverlays is applyBackspaceToAllCursors's
// counterpart: backspacing a line join (deleting across a newline) with
// multiple cursors must also shift cached overlay data down by the number
// of lines removed.
func TestApplyBackspaceToAllCursorsShiftsLSPOverlays(t *testing.T) {
	m := newTestModel("aaa\nbbb\nccc\nddd\n")
	m.rpc = &RPC{}
	// Both cursors sit at column 0 of a line, so backspacing joins that
	// line with the previous one (a line-count-changing delete).
	m.cursor = document.Pos{Line: 1, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 3, Col: 0}}}
	m.semanticSpans = highlight.LineSpans{3: []highlight.Span{{StartCol: 0, EndCol: 3, ANSI: "x"}}}

	m2, cmd := applyBackspaceToAllCursors(m)

	// Backspacing at line 1 col 0 joins lines 0+1 (removes one line).
	// Backspacing at line 3 col 0 (processed first, back-to-front) joins
	// lines 2+3 (removes one line). Line 3's cached span should end up
	// shifted to line 1.
	if spans, ok := m2.semanticSpans[1]; !ok || len(spans) != 1 {
		t.Errorf("semanticSpans[1] = %v, semanticSpans = %v, want the span shifted up to line 1", m2.semanticSpans[1], m2.semanticSpans)
	}
	if m2.lspOverlaySeq != m.lspOverlaySeq+1 {
		t.Errorf("lspOverlaySeq = %d, want %d (scheduleLSPOverlayRefresh never called)", m2.lspOverlaySeq, m.lspOverlaySeq+1)
	}
	if cmd == nil {
		t.Error("expected a non-nil batched command (includes the overlay-refresh tick)")
	}
}
