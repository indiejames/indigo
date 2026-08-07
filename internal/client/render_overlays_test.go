package client

import "testing"

// TestBuildSearchOverlaysSkipsStaleMatchPastEndOfLine is a regression test
// for a crash: search matches are computed once, at search time, and are
// never recalculated when the buffer changes afterward — e.g. an LSP
// refactor edit (Extract Function) shrinking a line out from under a still-
// active search. Rendering used the stale match's column directly as a
// slice index into the line's current (now shorter) runes with no bounds
// check, panicking with "slice bounds out of range" the next time the view
// redrew. The match must be skipped instead.
func TestBuildSearchOverlaysSkipsStaleMatchPastEndOfLine(t *testing.T) {
	m := newTestModel("short\n")
	// Recorded before an edit shrank this line down to "short" (5 runes) —
	// column 9 no longer exists on the line.
	m.searchMatches = []substituteMatch{{line: 0, col: 9, length: 5}}
	m.searchIdx = -1
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildSearchOverlays(layout, cw) // must not panic
	if len(rows) != 1 || len(rows[0]) != 0 {
		t.Errorf("buildSearchOverlays() = %v, want one row with no overlays (stale match skipped)", rows)
	}
}

// TestBuildSearchOverlaysRendersValidMatch is a sanity check alongside the
// regression test above: a match that still fits within its line must
// still render normally, so the staleness guard isn't over-broad.
func TestBuildSearchOverlaysRendersValidMatch(t *testing.T) {
	m := newTestModel("hello world\n")
	m.searchMatches = []substituteMatch{{line: 0, col: 6, length: 5}} // "world"
	m.searchIdx = -1
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildSearchOverlays(layout, cw)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("buildSearchOverlays() = %v, want one row with one overlay", rows)
	}
	if rows[0][0].col != 6 || rows[0][0].w != 5 {
		t.Errorf("overlay = %+v, want col=6 w=5", rows[0][0])
	}
}

// TestBuildSearchOverlaysSkipsMatchOnDeletedLine covers the buffer shrinking
// in *line count* (not just line length) out from under a stale match.
func TestBuildSearchOverlaysSkipsMatchOnDeletedLine(t *testing.T) {
	m := newTestModel("only line\n")
	m.searchMatches = []substituteMatch{{line: 5, col: 2, length: 3}} // line 5 no longer exists
	m.searchIdx = -1
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildSearchOverlays(layout, cw) // must not panic
	if len(rows) != 1 || len(rows[0]) != 0 {
		t.Errorf("buildSearchOverlays() = %v, want one row with no overlays", rows)
	}
}
