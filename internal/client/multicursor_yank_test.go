package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// twoCursorSelections puts a selection on "alpha" (line 0) and on "gamma"
// (line 1), with the primary cursor on the *second* one so document order and
// cursor-creation order disagree.
func twoCursorSelections() Model {
	m := newTestModel("alpha beta\ngamma delta\n")
	m.cursor = document.Pos{Line: 1, Col: 4}
	m.sel = &Selection{Anchor: document.Pos{Line: 1, Col: 0}, Head: document.Pos{Line: 1, Col: 4}}
	m.extraCursors = []ExtraCursor{{
		pos:     document.Pos{Line: 0, Col: 4},
		sel:     &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 4}},
		goalCol: -1,
	}}
	return m
}

// y with several cursors must copy every cursor's selection, not just the
// primary one.
//
// Reported by the user. Yank had no multi-cursor path at all, so it copied the
// primary selection and silently dropped the rest — the same "all but one
// cursor's text is lost" failure the cut path already had a comment warning
// about.
func TestYankCopiesEveryCursorSelection(t *testing.T) {
	fakeClipboardContent = ""
	m := twoCursorSelections()

	m2, _ := executeYank(m)
	got := m2.(Model)

	if fakeClipboardContent != "alpha\ngamma" {
		t.Errorf("clipboard = %q, want %q (both selections, in document order)",
			fakeClipboardContent, "alpha\ngamma")
	}
	if got.sel != nil {
		t.Error("primary selection not cleared after yank")
	}
	if got.extraCursors[0].sel != nil {
		t.Error("extra cursor's selection not cleared after yank")
	}
	if len(got.extraCursors) != 1 {
		t.Errorf("got %d extra cursors, want 1: yank must not drop cursors", len(got.extraCursors))
	}
}

// Document order, not cursor-creation order: the fixture's primary cursor is
// on the later line, so a clipboard built in cursor order would read
// "gamma\nalpha".
func TestYankUsesDocumentOrderNotCursorOrder(t *testing.T) {
	fakeClipboardContent = ""
	if _, _ = executeYank(twoCursorSelections()); fakeClipboardContent == "gamma\nalpha" {
		t.Error("clipboard is in cursor-creation order; must be document order")
	}
}

// y and v must agree about what N cursors mean. They are separate commands
// with separate entry points, and B-vs-E showed how easily such a pair drifts.
func TestYankAndCutAgreeAcrossCursors(t *testing.T) {
	fakeClipboardContent = ""
	_, _ = executeYank(twoCursorSelections())
	yanked := fakeClipboardContent

	fakeClipboardContent = ""
	mc := twoCursorSelections()
	mc.rpc = &RPC{}
	_, _ = executeCutSelection(mc)
	cut := fakeClipboardContent

	if yanked != cut {
		t.Errorf("yank put %q on the clipboard, cut put %q; the two must agree", yanked, cut)
	}
}

// A cursor with no selection contributes the character under it, exactly as
// single-cursor yank does — so one cursor is the N=1 case of the same rule.
func TestYankIncludesCharUnderASelectionlessCursor(t *testing.T) {
	fakeClipboardContent = ""
	m := newTestModel("abc\nxyz\n")
	m.cursor = document.Pos{Line: 0, Col: 0} // no selection
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 0}, goalCol: -1}}

	if _, _ = executeYank(m); fakeClipboardContent != "a\nx" {
		t.Errorf("clipboard = %q, want %q", fakeClipboardContent, "a\nx")
	}
}
