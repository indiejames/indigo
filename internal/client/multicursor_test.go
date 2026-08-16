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

// TestAddCursorBelowUsesLastExtraCursorAsReference verifies a second C press
// advances from the most recently added extra cursor, not the primary
// cursor's original column.
func TestAddCursorBelowUsesLastExtraCursorAsReference(t *testing.T) {
	m := newTestModel("aaaa\nbb\ncccc\ndddd\n")
	m.cursor = document.Pos{Line: 0, Col: 3}

	addCursorBelow(&m)
	if len(m.extraCursors) != 1 || m.extraCursors[0].pos.Line != 1 {
		t.Fatalf("after first addCursorBelow: extraCursors = %+v", m.extraCursors)
	}
	// Line 1 ("bb") is shorter than col 3, so the cursor clamps to its last column.
	if m.extraCursors[0].pos.Col != 1 {
		t.Errorf("clamped col = %d, want 1 (last index of \"bb\")", m.extraCursors[0].pos.Col)
	}

	addCursorBelow(&m)
	if len(m.extraCursors) != 2 || m.extraCursors[1].pos.Line != 2 {
		t.Fatalf("after second addCursorBelow: extraCursors = %+v", m.extraCursors)
	}
	// Reference column carries forward from the clamped col 1, not the original 3.
	if m.extraCursors[1].pos.Col != 1 {
		t.Errorf("second cursor col = %d, want 1 (carried from clamped reference)", m.extraCursors[1].pos.Col)
	}
}

// TestAddCursorBelowNoopPastLastLine verifies C on the last line does nothing.
func TestAddCursorBelowNoopPastLastLine(t *testing.T) {
	// No trailing newline: LineCount() == 1, so there really is no line below.
	m := newTestModel("only")
	m.cursor = document.Pos{Line: 0, Col: 0}
	addCursorBelow(&m)
	if len(m.extraCursors) != 0 {
		t.Errorf("extraCursors = %+v, want none (no line below the last)", m.extraCursors)
	}
}

// TestAddCursorBelowEmptyLineGetsColZero verifies landing on an empty line
// clamps to column 0 rather than -1.
func TestAddCursorBelowEmptyLineGetsColZero(t *testing.T) {
	m := newTestModel("abcd\n\n")
	m.cursor = document.Pos{Line: 0, Col: 2}
	addCursorBelow(&m)
	if len(m.extraCursors) != 1 || m.extraCursors[0].pos != (document.Pos{Line: 1, Col: 0}) {
		t.Errorf("extraCursors = %+v, want a single cursor at {1,0}", m.extraCursors)
	}
}

// TestSplitSelectionIntoCursorsNoopOnSingleLine verifies a same-line
// selection is left untouched (nothing to split).
func TestSplitSelectionIntoCursorsNoopOnSingleLine(t *testing.T) {
	m := newTestModel("hello world\n")
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 4}}
	before := *m.sel
	splitSelectionIntoCursors(&m)
	if len(m.extraCursors) != 0 {
		t.Errorf("extraCursors = %+v, want none for a single-line selection", m.extraCursors)
	}
	if *m.sel != before {
		t.Errorf("sel = %+v, want unchanged %+v", *m.sel, before)
	}
}

// TestSplitSelectionIntoCursorsMultiLine verifies a 3-line selection becomes
// a primary cursor on line 0 and one extra cursor per remaining line, each
// with its own per-line selection.
func TestSplitSelectionIntoCursorsMultiLine(t *testing.T) {
	m := newTestModel("aaaa\nbb\ncccccc\n")
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 1}, Head: document.Pos{Line: 2, Col: 3}}
	splitSelectionIntoCursors(&m)

	if len(m.extraCursors) != 2 {
		t.Fatalf("extraCursors = %+v, want 2 (one per remaining line)", m.extraCursors)
	}
	// Primary cursor covers line 0, from col 1 to end of line ("aaaa" → idx 3).
	if m.cursor != (document.Pos{Line: 0, Col: 3}) {
		t.Errorf("cursor = %+v, want {0,3} (end of line 0)", m.cursor)
	}
	// Line 1 ("bb") extra cursor spans the whole short line.
	ec0 := m.extraCursors[0]
	if ec0.pos != (document.Pos{Line: 1, Col: 1}) || ec0.sel == nil || ec0.sel.Anchor.Col != 0 {
		t.Errorf("extraCursors[0] = %+v, want full-line selection on line 1", ec0)
	}
	// Line 2 ("cccccc") extra cursor stops at the original selection's end.Col (3).
	ec1 := m.extraCursors[1]
	if ec1.pos != (document.Pos{Line: 2, Col: 3}) {
		t.Errorf("extraCursors[1].pos = %+v, want {2,3} (clamped to original selection end)", ec1.pos)
	}
}

// TestSplitSelectionIntoCursorsEmptyLineGetsCursorOnly verifies an empty
// line in the middle of the split range gets a bare cursor (no selection),
// since there's nothing to select on it.
func TestSplitSelectionIntoCursorsEmptyLineGetsCursorOnly(t *testing.T) {
	m := newTestModel("abc\n\nxyz\n")
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 2, Col: 1}}
	splitSelectionIntoCursors(&m)

	if len(m.extraCursors) != 2 {
		t.Fatalf("extraCursors = %+v, want 2", m.extraCursors)
	}
	empty := m.extraCursors[0]
	if empty.pos != (document.Pos{Line: 1, Col: 0}) || empty.sel != nil {
		t.Errorf("extraCursors[0] = %+v, want a bare cursor at {1,0} with no selection", empty)
	}
}

// TestDeleteAllCursorSelectionsDeletesEachSelectionOnce verifies a delete
// with a primary and one extra cursor selection removes both, processed
// back-to-front so the earlier (higher-line) delete doesn't shift the
// later one's coordinates.
func TestDeleteAllCursorSelectionsDeletesEachSelectionOnce(t *testing.T) {
	m := newTestModel("aXXXb\nc\ndYYYe\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 1}, Head: document.Pos{Line: 0, Col: 3}}
	m.extraCursors = []ExtraCursor{{
		pos: document.Pos{Line: 2, Col: 1},
		sel: &Selection{Anchor: document.Pos{Line: 2, Col: 1}, Head: document.Pos{Line: 2, Col: 3}},
	}}

	m2, cmd := deleteAllCursorSelections(m)

	if got := m2.buf.Line(0); got != "ab" {
		t.Errorf("line 0 = %q, want %q (XXX deleted)", got, "ab")
	}
	if got := m2.buf.Line(2); got != "de" {
		t.Errorf("line 2 = %q, want %q (YYY deleted)", got, "de")
	}
	if len(m2.extraCursors) != 1 {
		t.Errorf("extraCursors = %+v, want exactly 1 remaining cursor", m2.extraCursors)
	}
	if m2.sel != nil {
		t.Error("sel should be cleared after delete")
	}
	if cmd == nil {
		t.Error("expected a non-nil batched command")
	}
}

// TestDeleteAllCursorSelectionsOpensOwnUndoGroupWhenNoneActive verifies a
// standalone normal-mode multi-cursor delete (currentGroup nil beforehand)
// opens and closes its own transient undo group rather than leaking one
// open across calls.
func TestDeleteAllCursorSelectionsOpensOwnUndoGroupWhenNoneActive(t *testing.T) {
	m := newTestModel("aXb\ncYd\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 1}, Head: document.Pos{Line: 0, Col: 1}}

	m2, _ := deleteAllCursorSelections(m)

	if m2.currentGroup != nil {
		t.Error("currentGroup should be nil again after a standalone delete closes its transient group")
	}
	if len(m2.undoStack) != 1 {
		t.Fatalf("undoStack = %+v, want exactly one entry from the closed transient group", m2.undoStack)
	}
}

// TestApplyToAllCursorsAppliesFnToEachCursorIndependently verifies fn runs
// once for the primary cursor (which still sees the real extraCursors slice)
// and once per extra cursor (with extraCursors nulled out so fn can't see
// sibling cursors), and that each cursor's mutation is written back to the
// right slot.
func TestApplyToAllCursorsAppliesFnToEachCursorIndependently(t *testing.T) {
	m := newTestModel("aaaa\nbbbb\ncccc\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.extraCursors = []ExtraCursor{
		{pos: document.Pos{Line: 1, Col: 0}, goalCol: -1},
		{pos: document.Pos{Line: 2, Col: 0}, goalCol: -1},
	}

	var seenExtraCountDuringFn []int
	m.applyToAllCursors(func(mm *Model) {
		seenExtraCountDuringFn = append(seenExtraCountDuringFn, len(mm.extraCursors))
		mm.cursor.Col = 2 // mutate — should be written back per-cursor
	})

	if len(seenExtraCountDuringFn) != 3 {
		t.Fatalf("fn called %d times, want 3 (primary + 2 extra)", len(seenExtraCountDuringFn))
	}
	if seenExtraCountDuringFn[0] != 2 {
		t.Errorf("primary call saw %d extra cursors, want 2 (extraCursors intact during the primary call)", seenExtraCountDuringFn[0])
	}
	if seenExtraCountDuringFn[1] != 0 || seenExtraCountDuringFn[2] != 0 {
		t.Errorf("extra-cursor calls saw %v extra cursors, want [0 0] (isolated from siblings)", seenExtraCountDuringFn[1:])
	}
	if m.cursor.Col != 2 {
		t.Errorf("primary cursor.Col = %d, want 2", m.cursor.Col)
	}
	if len(m.extraCursors) != 2 || m.extraCursors[0].pos.Col != 2 || m.extraCursors[1].pos.Col != 2 {
		t.Errorf("extraCursors = %+v, want both cols mutated to 2", m.extraCursors)
	}
}

// TestApplyToAllCursorsRestoresViewportBetweenCalls verifies only the
// primary cursor's call is allowed to move the viewport permanently — an
// extra cursor's fn-induced scroll change must be reverted once that call
// returns, rather than compounding across cursors.
func TestApplyToAllCursorsRestoresViewportBetweenCalls(t *testing.T) {
	m := newTestModel("a\nb\nc\n")
	m.topLine = 0
	m.topChunk = 0
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 0}, goalCol: -1}}

	m.applyToAllCursors(func(mm *Model) {
		mm.topLine++ // simulate a scroll side effect on every call
	})

	// Primary call: 0 -> 1 (sticks). Extra-cursor call: 1 -> 2 during the
	// call, then reverted back to the saved 1. Final topLine must be 1, not
	// 2 — if the extra cursor's bump had stuck, it would compound.
	if m.topLine != 1 {
		t.Errorf("topLine = %d, want 1 (only the primary call's viewport change should persist)", m.topLine)
	}
}

// TestBuildExtraCursorOverlaysNoExtraCursorsReturnsNil verifies the
// documented empty-case contract.
func TestBuildExtraCursorOverlaysNoExtraCursorsReturnsNil(t *testing.T) {
	m := newTestModel("hello\n")
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	if got := m.buildExtraCursorOverlays(layout, cw); got != nil {
		t.Errorf("buildExtraCursorOverlays() = %v, want nil with no extra cursors", got)
	}
}

// TestBuildExtraCursorOverlaysBareCursor verifies a single extra cursor with
// no selection produces exactly one overlay entry, on its own screen row.
func TestBuildExtraCursorOverlaysBareCursor(t *testing.T) {
	m := newTestModel("aaaa\nbbbb\ncccc\n")
	m.height = 10
	m.width = 80
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 2}, goalCol: -1}}
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)

	rows := m.buildExtraCursorOverlays(layout, cw)
	if rows == nil {
		t.Fatal("buildExtraCursorOverlays() = nil, want overlays for the extra cursor")
	}
	total := 0
	for _, r := range rows {
		total += len(r)
	}
	if total != 1 {
		t.Errorf("total overlay entries = %d, want exactly 1 for one bare extra cursor", total)
	}
	if len(rows[1]) != 1 {
		t.Errorf("row 1 (bufLine 1, where the extra cursor sits) has %d overlays, want 1", len(rows[1]))
	}
}

// TestBuildExtraCursorOverlaysCursorPastLastLineSkipped verifies a stale
// extra cursor pointing past the current line count (e.g. after an undo
// shrank the buffer) is skipped rather than panicking on an out-of-range
// buffer line.
func TestBuildExtraCursorOverlaysCursorPastLastLineSkipped(t *testing.T) {
	m := newTestModel("a\n")
	m.height = 10
	m.width = 80
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 50, Col: 0}, goalCol: -1}}
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)

	rows := m.buildExtraCursorOverlays(layout, cw)
	for i, r := range rows {
		if len(r) != 0 {
			t.Errorf("row %d = %+v, want no overlays for an out-of-range extra cursor", i, r)
		}
	}
}
