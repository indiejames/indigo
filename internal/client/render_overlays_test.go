package client

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/config"
)

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

// TestOverlayRulerColumnUsesRealGlyphAtBoundaryColumn is a regression test
// for an off-by-one that existed in the old lineOverlay-based ruler
// implementation: RulerColumn is 1-indexed, so the 0-based index into a
// line's runes is RulerColumn-1. A line whose length exactly equals
// RulerColumn does have a real character at that index (it's the line's
// last one) — overlayRulerColumn (a plain string splice) has no such
// boundary condition to get wrong, but this keeps the case covered.
func TestOverlayRulerColumnUsesRealGlyphAtBoundaryColumn(t *testing.T) {
	got := ansi.Strip(overlayRulerColumn("123456789X", 9)) // 0-based col 9 = 'X'
	if want := "123456789X"; got != want {
		t.Errorf("overlayRulerColumn at line-length boundary = %q, want %q (the real trailing character retained, just restyled)", got, want)
	}
}

// TestOverlayRulerColumnBlankPastEndOfLine is a sanity check alongside the
// regression test above: a line shorter than the ruler column gets padded
// with a blank placeholder rather than an out-of-range read/panic.
func TestOverlayRulerColumnBlankPastEndOfLine(t *testing.T) {
	got := ansi.Strip(overlayRulerColumn("short", 9))
	if want := "short     "; got != want { // "short" + 4 padding spaces to reach col 9, then the ruler's own blank glyph
		t.Errorf("overlayRulerColumn past end of line = %q, want %q", got, want)
	}
}

// TestApplyRulerColumnStaysFixedDespiteInlayHint is a regression test for
// the bug this rewrite fixes: the ruler used to be a lineOverlay positioned
// by buffer column, but an inlay hint earlier on the same row is a w:0
// overlay — it renders real, visible glyphs without advancing the
// buffer-column bookkeeping those overlays compete on, so the ruler ended
// up rendered further right on the terminal than intended, by exactly the
// hint's rendered width (confirmed by temporarily reverting to the old
// lineOverlay-based implementation, which fails this test). What lands
// under the ruler can legitimately differ line to line (it might now be
// part of the hint's own text) — what must never change is that exactly
// rulerCol real visual columns precede it, wherever that content came from.
func TestApplyRulerColumnStaysFixedDespiteInlayHint(t *testing.T) {
	render := func(withHint bool) string {
		m := newTestModel("let x = 1\n")
		m.cfg = &config.Config{InlayHints: true, RulerColumn: 7} // 0-based col 6
		if withHint {
			m.inlayHints = []ClientInlayHint{
				{Line: 0, Col: 5, Label: ": number", Kind: 1, PaddingLeft: true},
			}
		}
		cw := 80
		layout := m.buildScreenLayout(1, cw)
		rowOverlays := m.buildRowOverlays(layout, cw)
		if inlayOverlays := m.buildInlayHintOverlays(layout, cw); inlayOverlays != nil {
			for i := range layout {
				if len(inlayOverlays[i]) > 0 {
					rowOverlays[i] = mergeOverlays(rowOverlays[i], inlayOverlays[i])
				}
			}
		}
		lines := make([]string, len(layout))
		for i := range layout {
			lines[i] = m.renderLineChunk(layout[i], cw, rowOverlays[i], -1, -1, false)
		}
		m.applyRulerColumn(lines, layout, cw)
		return lines[0]
	}

	without := render(false)
	with := render(true)

	if ansi.Strip(without) == ansi.Strip(with) {
		t.Fatal("sanity check failed: the inlay hint didn't actually change the rendered row")
	}
	for name, line := range map[string]string{"without hint": without, "with hint": with} {
		if w := lipgloss.Width(ansi.Truncate(line, 6, "")); w != 6 {
			t.Errorf("%s: %d real visual columns precede the ruler cell, want exactly 6 (ruler column drifted)", name, w)
		}
	}
}

// TestApplyRulerColumnSkipsCursorCell verifies the ruler never overwrites
// the cursor's own cell — otherwise the cursor would be invisible whenever
// it coincides with the ruler column.
func TestApplyRulerColumnSkipsCursorCell(t *testing.T) {
	m := newTestModel("0123456789\n")
	m.cfg = &config.Config{RulerColumn: 8} // 0-based col 7
	m.cursor.Col = 7
	cw := 80
	layout := m.buildScreenLayout(1, cw)
	lines := []string{m.renderLineChunk(layout[0], cw, nil, m.cursor.Line, m.cursor.Col, true)}
	before := lines[0]
	m.applyRulerColumn(lines, layout, cw)
	if lines[0] != before {
		t.Errorf("ruler overwrote the cursor's cell: before = %q, after = %q", before, lines[0])
	}
}

